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
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/serve"
	"github.com/ppf/dev/internal/serve/web"
	"github.com/ppf/dev/internal/tmux"
)

func newServeCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the board to the phone page over HTTP",
		Long: fmt.Sprintf(`Serve every dev boss's to do list, as dev todo --all --json has it, to a page on the phone: GET /v1/health,
GET /v1/board, POST /v1/pair, and POST /v1/answer and /v1/done, which answer and tick as dev todo answer --question and done --text do. It listens on the Mac's tailnet address (100.64.0.0/10) when it has one, else on its private LAN
address, port %d; --addr takes a loopback, tailnet or private LAN ip:port, never 0.0.0.0, :: or a public
address. Plain HTTP. /v1/board needs a paired phone on every bind, loopback too: dev serve pair.`, serve.DefaultPort),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "dev serve: open http://"+ln.Addr().String()+"/ on the phone")
			if kind == serve.LAN {
				fmt.Fprintln(out, "⚠ "+serve.LANNotice)
			}
			fmt.Fprintln(out, "pair a phone: dev serve pair --addr "+ln.Addr().String())
			return listenAndServe(cmd.Context(), ln, serveConfig(cmd.ErrOrStderr(), dir))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "the ip:port to listen on: loopback, tailnet or private LAN")
	cmd.AddCommand(newServePairCmd(), newServeUnpairCmd())
	return cmd
}

func newServePairCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Print a one-time code that pairs a phone with dev serve",
		Long: fmt.Sprintf(`Print the URL to open on the phone and a one-time code to enter there, valid %d minutes and once (5 wrong
codes or a new dev serve pair void it): the running
dev serve then sets the phone's session cookie. The URL is dev serve's default bind, or --addr as dev serve was
given it. Refused in a Claude Code session ($CLAUDECODE set) and outside a terminal: only the user pairs.`, int(serve.CodeTTL.Minutes())),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case os.Getenv("CLAUDECODE") != "":
				return errors.New("refused in a Claude Code session: only the user pairs (in a shell)")
			case !hasTerminal():
				return errors.New("refused outside a terminal: run dev serve pair in a shell")
			}
			ap, _, err := bind(addr)
			if err != nil {
				return err
			}
			dir, err := authDir()
			if err != nil {
				return err
			}
			code, err := serve.NewCode(dir, time.Now())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "open http://%s/ on the phone and enter the code %s (valid %d minutes, once)\n", ap, code, int(serve.CodeTTL.Minutes()))
			return err
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "the ip:port dev serve listens on, when not its default")
	return cmd
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
		Version: buildVersion(),
		Now:     time.Now,
		Lists:   boardLists(tmux.New(), stderr, time.Now),
		Answer:  pageAnswer,
		Done:    pageDone(stderr),
		Guard:   auth.Session,
		Pair:    http.HandlerFunc(auth.Pair),
		Page:    web.Handler(),
	}
}

// boardLists is dev todo --all --json's lists, a list error's home folder shown as ~: the board leaves the Mac.
func boardLists(c *tmux.Client, stderr io.Writer, now func() time.Time) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		lists, err := todoAll(c, stderr, now(), true)
		if err != nil {
			return nil, err
		}
		for i := range lists {
			lists[i].Error = strings.ReplaceAll(lists[i].Error, filepath.Clean(home)+"/", "~/")
		}
		return json.Marshal(lists)
	}
}

// listenAndServe serves on ln until ctx ends or SIGINT/SIGTERM, then shuts down.
func listenAndServe(ctx context.Context, ln net.Listener, cfg serve.Config) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: serve.Handler(cfg), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
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
