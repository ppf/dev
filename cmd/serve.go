package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/serve"
	"github.com/ppf/dev/internal/serve/web"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func newServeCmd() *cobra.Command {
	var addr string
	var install, uninstall, awake bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the board to the phone page over HTTP",
		Long: fmt.Sprintf(`Serve every dev boss's to do list, as dev todo --all --json has it, to a page on the phone: GET /v1/health,
GET /v1/board, GET /v1/events (the board changed: an event stream), POST /v1/pair, and POST /v1/answer and /v1/done, which answer and tick as dev todo answer --question and done --text do. It listens on the Mac's tailnet address (100.64.0.0/10) when it has one, else on its private LAN
address, port %d; --addr takes a loopback, tailnet or private LAN ip:port, never 0.0.0.0, :: or a public
address. Plain HTTP. /v1/board and /v1/events need a paired phone on every bind, loopback too: dev serve pair.`, serve.DefaultPort),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if install || uninstall {
				return launchAgent(cmd.OutOrStdout(), install, addr, awake)
			}
			ap, kind, err := bind(addr)
			if err != nil {
				return err
			}
			dir, err := authDir()
			if err != nil {
				return err
			}
			ln, err := net.Listen("tcp", ap.String())
			if err != nil {
				return err
			}
			forget, err := serve.RecordAddr(dir, netip.AddrPortFrom(ap.Addr(), uint16(ln.Addr().(*net.TCPAddr).Port)))
			if err != nil {
				_ = ln.Close()
				return err
			}
			defer forget()
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "dev serve: open http://"+ln.Addr().String()+"/ on the phone")
			if awake {
				if err := keepAwake(out, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			if kind == serve.LAN {
				fmt.Fprintln(out, "⚠ "+serve.LANNotice)
			}
			fmt.Fprintln(out, "pair a phone: dev serve pair")
			// Off the serving path: a hung tmux server must not hold up the page
			go func() {
				if _, err := tmux.New().Run("list-sessions"); tmux.IsNoServer(err) {
					fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev serve: no tmux server running: the board stays empty until it starts")
				}
			}()
			return listenAndServe(cmd.Context(), ln, serveConfig(cmd.ErrOrStderr(), dir))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "the ip:port to listen on: loopback, tailnet or private LAN")
	cmd.Flags().BoolVar(&install, "install", false, "write a launchd agent that runs dev serve (with --addr) at login, and print the launchctl line to start it")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "remove dev serve's launchd agent")
	cmd.Flags().BoolVar(&awake, "awake", runtime.GOOS == "darwin", "keep the Mac from sleeping while serving, on mains power (caffeinate -s)")
	cmd.MarkFlagsMutuallyExclusive("install", "uninstall")
	cmd.MarkFlagsMutuallyExclusive("uninstall", "addr")
	cmd.MarkFlagsMutuallyExclusive("uninstall", "awake")
	cmd.AddCommand(newServePairCmd(), newServeUnpairCmd())
	return cmd
}

func newServePairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Print a one-time code that pairs a phone with dev serve",
		Long: fmt.Sprintf(`Print the URL to open on the phone and a one-time code to enter there, valid %d minutes and once (5 wrong
codes or a new dev serve pair void it): the running
dev serve then sets the phone's session cookie. The URL is the address the running dev serve listens on; when that is
loopback only, a phone can't reach it, so pair says so and how to restart dev serve. Errors when dev serve is not
running. Refused in a Claude Code session ($CLAUDECODE set) and outside a terminal: only the user pairs.`, int(serve.CodeTTL.Minutes())),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case os.Getenv("CLAUDECODE") != "":
				return errors.New("refused in a Claude Code session: only the user pairs (in a shell)")
			case !hasTerminal():
				return errors.New("refused outside a terminal: run dev serve pair in a shell")
			}
			dir, err := authDir()
			if err != nil {
				return err
			}
			ap, err := serve.ServedAddr(dir)
			if err != nil {
				return err
			}
			code, err := serve.NewCode(dir, time.Now())
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), pairMessage(ap, code, restartHint()))
			return err
		},
	}
	return cmd
}

// pairMessage tells where to open the page: the phone's URL when ap is reachable from it, else why not and restartHint.
func pairMessage(ap netip.AddrPort, code, restartHint string) string {
	ttl := int(serve.CodeTTL.Minutes())
	if ap.Addr().IsLoopback() {
		return fmt.Sprintf("dev serve listens on %s only: a phone can't reach it. %s\nOn this Mac: open http://%s/ and enter the code %s (valid %d minutes, once)\n", ap.Addr(), restartHint, ap, code, ttl)
	}
	return fmt.Sprintf("open http://%s/ on the phone and enter the code %s (valid %d minutes, once)\n", ap, code, ttl)
}

// restartHint is how to restart dev serve so a phone can reach it: its default bind when this Mac has one, else --addr.
func restartHint() string {
	if ap, _, err := bind(""); err == nil {
		return fmt.Sprintf("Restart it with dev serve (it listens on %s by default) or dev serve --addr <tailnet or private LAN ip:port>.", ap)
	}
	return "Restart it with dev serve --addr <tailnet or private LAN ip:port>."
}

func newServeUnpairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unpair [device]",
		Short: "Drop a paired phone's session, or every one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := authDir()
			if err != nil {
				return err
			}
			device := ""
			if len(args) == 1 {
				if device = args[0]; device == "" {
					return errors.New("the device name is blank: dev serve unpair without one drops every device")
				}
			}
			dropped, err := serve.Unpair(dir, device)
			switch {
			case err != nil:
				return err
			case len(dropped) == 0 && device != "":
				return fmt.Errorf("no paired device named %q", device)
			case len(dropped) == 0:
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "no paired devices")
			default:
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "unpaired: "+strings.Join(dropped, ", "))
			}
			return err
		},
	}
}

// authDir is where pairing codes and sessions are kept: dev's state directory's serve folder.
func authDir() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "serve"), nil
}

// bind is --addr checked, else the default bind.
func bind(addr string) (netip.AddrPort, serve.Kind, error) {
	if addr != "" {
		return serve.Check(addr)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	return serve.Default(addrs, serve.DefaultRoute)
}

// serveConfig reads the board from dev todo --all --json's code on the default tmux server, for paired phones.
func serveConfig(stderr io.Writer, dir string) serve.Config {
	auth := &serve.Auth{Dir: dir, Now: time.Now}
	return serve.Config{
		Version:     selfreload.Version(),
		Now:         time.Now,
		Lists:       boardLists(tmux.New(), &unrepeated{w: stderr, now: time.Now}, time.Now),
		ManagerRuns: func() bool { return running(tmux.New(), sidebar.Manager) },
		Answer:      pageAnswer,
		Done:        pageDone(stderr),
		Start:       pageStart(stderr),
		Guard:       auth.Session,
		Pair:        http.HandlerFunc(auth.Pair),
		Page:        web.Handler(),
	}
}

// boardLists is dev todo --all --json's lists, the home folder in a list's error and an item's now shown as ~: the board leaves the Mac.
func boardLists(c *tmux.Client, stderr io.Writer, now func() time.Time) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		lists, err := todoAll(c, stderr, now(), true, true, true)
		if err != nil {
			return nil, err
		}
		// The home folder whole, not a sibling sharing its prefix (/Users/ann, not /Users/anna)
		homeRe := regexp.MustCompile(regexp.QuoteMeta(filepath.Clean(home)) + `(/|$|[^\w.-])`)
		scrub := func(s string) string { return homeRe.ReplaceAllString(s, "~$1") }
		for i := range lists {
			lists[i].Error = scrub(lists[i].Error)
			for j := range lists[i].Items {
				lists[i].Items[j].Now = scrub(lists[i].Items[j].Now)
			}
		}
		return json.Marshal(lists)
	}
}

// unrepeated drops a line written again within a minute: an open page's stream reads the board every second.
type unrepeated struct {
	w    io.Writer
	now  func() time.Time
	mu   sync.Mutex
	seen map[string]time.Time
}

func (u *unrepeated) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for line, at := range u.seen {
		if now.Sub(at) >= time.Minute {
			delete(u.seen, line)
		}
	}
	if _, ok := u.seen[string(p)]; ok {
		return len(p), nil
	}
	if u.seen == nil {
		u.seen = map[string]time.Time{}
	}
	u.seen[string(p)] = now
	return u.w.Write(p)
}

// listenAndServe serves on ln until ctx ends or SIGINT/SIGTERM, then shuts down.
func listenAndServe(ctx context.Context, ln net.Listener, cfg serve.Config) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Shutdown waits for open requests: the pages' event streams end when it starts
	base, closeStreams := context.WithCancel(context.Background())
	defer closeStreams()
	srv := &http.Server{Handler: serve.Handler(cfg), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, BaseContext: func(net.Listener) context.Context { return base }}
	srv.RegisterOnShutdown(closeStreams)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		stop() // a second Ctrl-C kills it
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
