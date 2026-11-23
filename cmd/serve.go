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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

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
GET /v1/board, GET /v1/events (the board changed: an event stream), POST /v1/pair, and POST /v1/answer, /v1/done, /v1/start and /v1/doing, which answer, tick, start and mark in progress or end it as dev todo answer --question, done --text, start and doing do. It listens on the Mac's tailnet address (100.64.0.0/10) when it has one, else on its private LAN
address, port %d; --addr takes a loopback, tailnet or private LAN ip:port, never 0.0.0.0, :: or a public
address. Plain HTTP. On a tailnet or LAN address, /v1/board, /v1/events and the answer, done, start and doing POSTs
need a paired phone: dev serve pair. On a loopback address they need no pairing: a request a browser marks as
another origin's (Sec-Fetch-Site, Origin) is refused instead.`, serve.DefaultPort),
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
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			// After a reload the caffeinate started before it still waits on this pid, and the alive lock it kept is open
			reloaded, pid := reexecMarker(os.Getenv(envServeReexec), os.Getpid())
			kept := 0
			if v := os.Getenv(selfreload.EnvKept); v != "" {
				if kept, err = strconv.Atoi(v); err != nil {
					return fmt.Errorf("%s=%q: %w", selfreload.EnvKept, v, err)
				}
			}
			for _, k := range []string{envServeReexec, selfreload.EnvKept} {
				if err := os.Unsetenv(k); err != nil {
					return err
				}
			}
			// Before the listen, so a dev boss sees this one starting
			alive, err := serve.Alive(dir)
			if err != nil {
				return err
			}
			defer func() { _ = alive.Close() }()
			if os.Getenv(envStartFd) == "3" && alive.Fd() != 3 { // the dev boss's start lock: one holds now
				_ = os.NewFile(3, "start").Close()
			}
			if err := os.Unsetenv(envStartFd); err != nil {
				return err
			}
			if reloaded && kept > 2 {
				closeKept(kept, alive, cmd.ErrOrStderr())
			}
			ln, err := net.Listen("tcp", ap.String())
			if err != nil {
				return err
			}
			forget, err := serve.RecordAddr(dir, netip.AddrPortFrom(ap.Addr(), uint16(ln.Addr().(*net.TCPAddr).Port)), reloaded)
			if err != nil {
				_ = ln.Close()
				return err
			}
			defer func() {
				if err := forget(); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev serve: its address record stays: "+err.Error())
				}
			}()
			out := cmd.OutOrStdout()
			loopback := ap.Addr().IsLoopback()
			where := "on the phone"
			if loopback {
				where = "in a browser on this Mac, no pairing needed (a phone can't reach it)"
			}
			fmt.Fprintln(out, "dev serve: open http://"+ln.Addr().String()+"/ "+where)
			switch {
			case reloaded && awakeAlive(pid):
				caffeinate.Store(int64(pid))
				go watchAwake(pid, cmd.ErrOrStderr())
			case awake:
				if err := keepAwake(out, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			if kind == serve.LAN {
				fmt.Fprintln(out, "⚠ "+serve.LANNotice)
			}
			if !loopback {
				fmt.Fprintln(out, "pair a phone: dev serve pair")
			}
			// Off the serving path: a hung tmux server must not hold up the page
			go func() {
				if _, err := tmux.New().Run("list-sessions"); tmux.IsNoServer(err) {
					fmt.Fprintln(cmd.ErrOrStderr(), "⚠ dev serve: no tmux server running: the board stays empty until it starts")
				}
			}()
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			o := selfreload.Options{Exe: exe, Own: selfreload.Version(), Tmux: tmux.New(), Keep: alive, // no dev boss sees none running meanwhile
				Warn: func(err error) { fmt.Fprintln(stderr, "⚠ dev serve: "+err.Error()) }}
			bound := ln.Addr().(*net.TCPAddr).AddrPort()
			args := func() ([]string, error) { return reexecArgs(os.Args, addr, bound, ownIP(bound.Addr())) }
			return serveReloading(cmd.Context(), ln, serveConfig(stderr, dir, loopback), o, args, out, stderr)
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
dev serve then sets the phone's session cookie. The URL is the address the running dev serve listens on (with several,
each one a phone can reach); when that is loopback only, it needs no pairing and a phone can't reach it, so pair writes no code and says so and how to restart
dev serve (one from an older dev, which still wants pairing there, it says to restart). Errors when dev serve is not
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
			live, err := pairServed(dir, ownIP, time.Second)
			if err != nil {
				return err
			}
			urls := phoneURLs(live)
			if len(urls) == 0 {
				var errs []error
				for _, r := range live { // one an older dev runs may come first
					if err := unpairedBoard(r.Addr); err != nil {
						errs = append(errs, err)
						continue
					}
					_, err = fmt.Fprint(cmd.OutOrStdout(), loopbackMessage(r.Addr, restartHint()))
					return err
				}
				return errors.Join(errs...)
			}
			code, err := serve.NewCode(dir, time.Now())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "open %s on the phone and enter the code %s (valid %d minutes, once)\n", strings.Join(urls, " or "), code, int(serve.CodeTTL.Minutes()))
			return err
		},
	}
	return cmd
}

var pairServed = serve.Served // test seam: a phone-reachable dev serve would need a LAN bind

// phoneURLs is the page's URL on each dev serve a phone can reach (not loopback): any of them takes the code, the
// state folder's.
func phoneURLs(live []serve.Running) []string {
	var urls []string
	for _, r := range live {
		if !r.Addr.Addr().IsLoopback() {
			urls = append(urls, "http://"+r.Addr.String()+"/")
		}
	}
	return urls
}

// unpairedBoard errors when the dev serve on loopback ap still wants a session: one from before loopback needed no
// pairing, which a reload hasn't replaced. An empty tick asks it without a board read: its Guard answers 401 before
// the body's 400.
func unpairedBoard(ap netip.AddrPort) error {
	req, err := http.NewRequest(http.MethodPost, "http://"+ap.String()+"/v1/done", strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	req.Header.Set("X-Dev-Page", "1")
	resp, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return fmt.Errorf("dev serve on %s: %w", ap, err)
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("the dev serve on %s is from an older dev and still wants pairing on loopback: restart it", ap)
	}
	return fmt.Errorf("dev serve on %s answered %s to an empty tick, not 400: restart it", ap, resp.Status)
}

// loopbackMessage is pair's answer for a dev serve on loopback only: no pairing there, and a phone can't reach it.
func loopbackMessage(ap netip.AddrPort, restartHint string) string {
	return fmt.Sprintf("dev serve listens on %s only: no pairing needed, open http://%s/ in a browser on this Mac.\nA phone can't reach it: %s\n", ap.Addr(), ap, restartHint)
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

// serveConfig reads the board from dev todo --all --json's code on the default tmux server: for paired phones; when the
// bound address is loopback, for any request a browser doesn't mark as another origin's.
func serveConfig(stderr io.Writer, dir string, loopback bool) serve.Config {
	auth := &serve.Auth{Dir: dir, Now: time.Now}
	guard := auth.Session
	if loopback {
		guard = serve.SameOrigin
	}
	return serve.Config{
		Version:     selfreload.Version(),
		Now:         time.Now,
		Lists:       boardLists(tmux.New(), &unrepeated{w: stderr, now: time.Now}, time.Now),
		ManagerRuns: func() bool { return running(tmux.New(), sidebar.Manager) },
		Answer:      pageAnswer,
		Done:        pageDone(stderr),
		Start:       pageStart(stderr),
		Doing:       pageDoing(stderr),
		Reopen:      pageReopen(stderr),
		Guard:       guard,
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
				lists[i].Items[j].Note = scrub(lists[i].Items[j].Note)
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

// closeKept closes fd, the alive lock a reload kept open across its exec, once this process holds alive: only when
// it is that file (raw fd, no *os.File whose finalizer would close a reused number later); else it only gets
// close-on-exec, so no child keeps it.
func closeKept(fd int, alive *os.File, stderr io.Writer) {
	var a, b unix.Stat_t
	if err := unix.Fstat(fd, &a); err != nil {
		fmt.Fprintf(stderr, "⚠ dev serve: kept alive lock fd %d: %v\n", fd, err)
		return
	}
	if err := unix.Fstat(int(alive.Fd()), &b); err != nil || a.Dev != b.Dev || a.Ino != b.Ino {
		syscall.CloseOnExec(fd)
		return
	}
	if err := syscall.Close(fd); err != nil {
		fmt.Fprintf(stderr, "⚠ dev serve: kept alive lock fd %d: %v\n", fd, err)
	}
}

// envServeReexec is "<pid> <caffeinate pid>" across a reload: that process doesn't start a second caffeinate.
const envServeReexec = "DEV_SERVE_REEXEC"

// reexecMarker reads envServeReexec: whether this pid re-exec'd itself, and its caffeinate's pid (0: none).
func reexecMarker(v string, pid int) (bool, int) {
	self, caff, _ := strings.Cut(v, " ")
	if self != strconv.Itoa(pid) {
		return false, 0
	}
	n, _ := strconv.Atoi(caff)
	return true, n
}

// reexecArgs is args with --addr pinned to bound (before a "--"), so the new process binds there, not a fresh pick.
// Once bound's ip is no longer this Mac's (own false) the new process could not bind it: picked, it picks afresh;
// given by --addr, an error (the reload waits).
func reexecArgs(args []string, addr string, bound netip.AddrPort, own bool) ([]string, error) {
	switch {
	case !own && addr != "":
		return nil, fmt.Errorf("%s is no longer this Mac's address: restart dev serve with another --addr", bound.Addr())
	case !own || addr == bound.String():
		return args, nil
	}
	i := slices.Index(args, "--")
	if i < 0 {
		i = len(args)
	}
	return slices.Concat(args[:i], []string{"--addr", bound.String()}, args[i:]), nil
}

// ownIP: ip is loopback or one of this Mac's interface addresses.
func ownIP(ip netip.Addr) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(addrs, func(a net.Addr) bool {
		n, ok := a.(*net.IPNet)
		return ok && n.IP.Equal(net.IP(ip.AsSlice()))
	})
}

// caffeinate is the running caffeinate's pid (0: none), handed to the process a reload execs.
var caffeinate atomic.Int64

// Test seams
var (
	watchReload  = selfreload.Watch
	execReload   = selfreload.Exec
	shutdownWait = 5 * time.Second
)

// serveReloading serves on ln until ctx ends or SIGINT/SIGTERM; when o.Exe holds a new release it shuts down and
// re-execs into it (same pid) with args(). Not a tmux pane, it takes no part in the canary: it reloads after its own
// checks even when that version's canary pane died. A phone action still running (on any server so far) or args()
// failing holds the reload before the shutdown, as do requests still open 5 s into it and ErrNotNow (kill switch on,
// the file changed since checked): it serves again and watches again. An exec that fails serves again and watches no
// more. The socket stays bound throughout: only the exec closes it.
func serveReloading(parent context.Context, ln net.Listener, cfg serve.Config, o selfreload.Options, args func() ([]string, error), out, stderr io.Writer) error {
	var actions atomic.Int64
	lastHeld := ""
	hold := func(version, why string) bool {
		if why != "" && why != lastHeld {
			fmt.Fprintf(stderr, "⚠ dev serve: reload into %s held: %s\n", version, why)
		}
		lastHeld = why
		return why != ""
	}
	mayGo := func(r selfreload.Ready) bool {
		why := ""
		if actions.Load() > 0 {
			why = "a phone action is still running"
		} else if _, err := args(); err != nil {
			why = err.Error()
		}
		return !hold(r.Version, why)
	}
	watching := true
	for {
		ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		tl, ok := ln.(*net.TCPListener)
		if !ok {
			stop()
			return fmt.Errorf("dev serve: %T is no TCP listener", ln)
		}
		keep, err := tl.File() // close-on-exec
		if err != nil {
			stop()
			return err
		}
		wctx, stopWatch := context.WithCancel(ctx)
		var watch func() <-chan selfreload.Ready
		if watching {
			watch = func() <-chan selfreload.Ready { return watchReload(wctx, o) }
		}
		r, reload, err := listenAndServe(ctx, stop, ln, cfg, &actions, watch, mayGo)
		stopWatch()
		if !reload || ctx.Err() != nil || (err != nil && !errors.Is(err, context.DeadlineExceeded)) {
			stop()
			_ = keep.Close()
			return err
		}
		var a []string
		if err == nil && actions.Load() == 0 {
			a, err = args()
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			hold(r.Version, "requests still open 5 s into the shutdown")
		case err != nil:
			hold(r.Version, err.Error())
		case actions.Load() > 0:
			hold(r.Version, "a phone action is still running")
		default:
			stop() // a signal from here on ends the process: the exec can't be called off
			fmt.Fprintf(out, "↻ dev serve: reloading into %s\n", r.Version)
			if err := os.Setenv(envServeReexec, fmt.Sprintf("%d %d", os.Getpid(), caffeinate.Load())); err != nil {
				return err
			}
			err = execReload(r, a)
			if uerr := os.Unsetenv(envServeReexec); uerr != nil {
				return uerr
			}
			if errors.Is(err, selfreload.ErrNotNow) {
				hold(r.Version, err.Error())
			} else {
				fmt.Fprintf(stderr, "❌ reload: %v: dev serve stays on %s; restart it to update\n", err, o.Own)
				watching = false
			}
		}
		stop()
		ln, err = net.FileListener(keep)
		if cerr := keep.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("serve again after the reload: %w", err)
		}
	}
}

// listenAndServe serves on ln until ctx ends (false; stop then restores the signals' default), or a Ready from watch
// that mayGo lets through (it, true; one it holds watches again), then shuts down. actions counts the requests
// running that change something (not GET or HEAD). A reload's shutdown that times out leaves the open requests
// running and returns its error with the Ready; a stop during that shutdown wins over the reload.
func listenAndServe(ctx context.Context, stop func(), ln net.Listener, cfg serve.Config, actions *atomic.Int64,
	watch func() <-chan selfreload.Ready, mayGo func(selfreload.Ready) bool) (selfreload.Ready, bool, error) {
	// Shutdown waits for open requests: the pages' event streams end when it starts
	base, closeStreams := context.WithCancel(context.Background())
	defer closeStreams()
	h := serve.Handler(cfg)
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			actions.Add(1)
			defer actions.Add(-1)
		}
		h.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: counted, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, BaseContext: func(net.Listener) context.Context { return base }}
	srv.RegisterOnShutdown(closeStreams)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	var ready <-chan selfreload.Ready
	if watch != nil {
		ready = watch()
	}
	var r selfreload.Ready
	reload := false
wait:
	for {
		select {
		case err := <-errc:
			return r, false, err
		case <-ctx.Done():
			stop() // a second Ctrl-C kills it
			break wait
		case r, reload = <-ready:
			switch {
			case !reload:
				ready = nil // closed: the watch ended
			case !mayGo(r):
				reload, ready = false, watch()
			default:
				break wait
			}
		}
	}
	shut, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	err := srv.Shutdown(shut)
	if reload && ctx.Err() != nil {
		reload = false
	}
	if err != nil {
		return r, reload, fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return r, reload, err
	}
	return r, reload, nil
}
