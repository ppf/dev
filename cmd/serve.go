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
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/serve"
	"github.com/ppf/dev/internal/tmux"
)

func newServeCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the board to the phone page over HTTP",
		Long: fmt.Sprintf(`Serve every dev boss's to do list, as dev todo --all --json has it, to a page on the phone: GET /v1/health,
GET /v1/board, and POST /v1/answer and /v1/done, which answer and tick as dev todo answer --question and done --text do. It listens on the Mac's tailnet address (100.64.0.0/10) when it has one, else on its private LAN
address, port %d; --addr takes a loopback, tailnet or private LAN ip:port, never 0.0.0.0, :: or a public
address. Plain HTTP. Until pairing lands, /v1/board answers on a loopback bind only.`, serve.DefaultPort),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ap, kind, err := bind(addr)
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
			if kind != serve.Loopback {
				fmt.Fprintln(out, "⚠ /v1/board answers on a loopback bind only until pairing lands")
			}
			return listenAndServe(cmd.Context(), ln, serveConfig(cmd.ErrOrStderr(), kind))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "the ip:port to listen on: loopback, tailnet or private LAN")
	return cmd
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

// serveConfig reads the board from dev todo --all --json's code on the default tmux server.
func serveConfig(stderr io.Writer, kind serve.Kind) serve.Config {
	return serve.Config{
		Version: buildVersion(),
		Now:     time.Now,
		Lists: func() (json.RawMessage, error) {
			lists, err := todoAll(tmux.New(), stderr, time.Now(), true)
			if err != nil {
				return nil, err
			}
			return json.Marshal(lists)
		},
		Answer: pageAnswer,
		Done:   pageDone(stderr),
		Guard:  serve.LoopbackOnly(kind),
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
