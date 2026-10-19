package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/internal/tui"
	"github.com/goxang/rig/internal/web"
	"github.com/goxang/rig/spec"
)

func webCommand() *cobra.Command {
	var addr string
	var noOpen bool
	c := &cobra.Command{
		Use:   "web",
		Short: "open the control plane in your browser: services, logs, metrics, tasks, queries, jobs and the inbox",
		Long: `Serves the control plane on this machine and opens it in the browser. The link carries a token
the page keeps, so a bookmark of the address works while the token file stays
(<state dir>/web-token). Themes are the UI's (rig theme); the layout (sidebar, top bar or
terminal-like) is picked in the page's settings.`,
		Example: `  rig web
  rig web --addr 127.0.0.1:8080 --no-open`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file := g.file
			if file == "" {
				var err error
				if file, err = spec.Find("."); err != nil {
					return err
				}
			}
			ensureOfficial()
			if _, err := ensurePlugins(filepath.Dir(file)); err != nil {
				return err
			}
			token, err := webToken()
			if err != nil {
				return err
			}
			h := &web.Handler{File: file, Token: token, Open: engine.Open,
				Info: map[string]any{"version": Version, "theme": tui.CurrentTheme(), "themes": tui.Palettes()}}
			defer h.Close()
			if _, err := h.Engine(g.env); err != nil {
				return err
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil && addr == "127.0.0.1:7979" {
				ln, err = net.Listen("tcp", "127.0.0.1:0")
			}
			if err != nil {
				return err
			}
			srv := &http.Server{Handler: h.Routes(), ReadHeaderTimeout: 10 * time.Second}
			host := ln.Addr().String()
			if strings.HasPrefix(host, "0.0.0.0:") || strings.HasPrefix(host, "[::]:") {
				host = "localhost" + host[strings.LastIndex(host, ":"):]
			}
			u := "http://" + host + "/#token=" + token
			if g.env != "" {
				u += "&env=" + g.env
			}
			fmt.Println("rig web on " + u)
			fmt.Println(dim("ctrl+c stops it"))
			if !noOpen {
				openBrowser(u)
			}
			go func() {
				<-cmd.Context().Done()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			}()
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	c.Flags().StringVar(&addr, "addr", "127.0.0.1:7979", "address to listen on (a free port when the default is taken)")
	c.Flags().BoolVar(&noOpen, "no-open", false, "print the link only")
	return c
}

// webToken is the page's token, kept so a restarted rig web serves the open tabs again.
func webToken() (string, error) {
	f := filepath.Join(host.StateDir(), "web-token")
	if raw, err := os.ReadFile(f); err == nil && len(strings.TrimSpace(string(raw))) >= 20 {
		return strings.TrimSpace(string(raw)), nil
	}
	t := rand.Text()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return "", err
	}
	return t, os.WriteFile(f, []byte(t+"\n"), 0o600)
}
