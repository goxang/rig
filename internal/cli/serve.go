package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/server"
	"github.com/goxang/rig/spec"
)

func serverDir() string { return filepath.Join(host.StateDir(), "server") }

func serveCommand() *cobra.Command {
	var addr, cert, key, pull string
	var hosts []string
	var oidc server.OIDC
	var plain, quiet bool
	c := &cobra.Command{
		Use:   "serve",
		Short: "serve this project to remote CLIs, MCP clients and the web inbox, over HTTPS, with accounts and server-side policy",
		Long: `Serves the project over HTTPS: the Engine API for rig -r, and the inbox at /. Without --cert, a
self-signed certificate is made once and clients pin its fingerprint. Accounts live under the
rig state directory: rig admin users add <name> makes one. Agents and scheduled reports run here
for as long as the server does.`,
		Example: `  rig admin users add alice --role dev --totp
  rig serve --addr :7443 --host rig.example.local
  rig serve --oidc-issuer https://sso.example.local/realms/dev --oidc-client-id rig --oidc-redirect https://rig.example.local:7443/v1/oidc/callback`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			file := g.file
			if file == "" {
				var err error
				if file, err = spec.Find("."); err != nil {
					return err
				}
			}
			st, err := server.OpenStore(filepath.Join(serverDir(), "users.json"))
			if err != nil {
				return err
			}
			if len(st.Users()) == 0 {
				fmt.Fprintln(os.Stderr, amber("no accounts yet: rig admin users add <name> --role admin"))
			}
			s := &server.Server{File: file, Store: st}
			defer s.Close()
			if oidc.Issuer != "" {
				oidc.ClientSecret = os.Getenv("RIG_OIDC_CLIENT_SECRET")
				s.OIDC = &oidc
			}
			l, err := s.Engine(g.env)
			if err != nil {
				return err
			}
			if !quiet {
				go func() { _ = watchAgents(ctx, l.App, l) }()
				go func() {
					_ = watchReports(ctx, l.App, "", 15*time.Minute, func(ctx context.Context, a *engine.App, rep *engine.ReportResult) error {
						return saveReport(ctx, a, rep, "", "", true)
					})
				}()
			}
			if pull != "" {
				every, err := time.ParseDuration(pull)
				if err != nil {
					return fmt.Errorf("--pull: %w", err)
				}
				go keepPulled(ctx, filepath.Dir(file), every, s)
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
			go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
			if plain {
				if host, _, _ := net.SplitHostPort(ln.Addr().String()); !net.ParseIP(host).IsLoopback() {
					return errors.New("--plain-http only on a loopback address, behind a TLS proxy")
				}
				fmt.Printf("rig serving %s on http://%s (plain HTTP, loopback only)\n", l.App.Spec.Name, ln.Addr())
				err = srv.Serve(ln)
			} else {
				crt, fp, cerr := server.Certificate(cert, key, serverDir(), hosts)
				if cerr != nil {
					return cerr
				}
				srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{crt}, MinVersion: tls.VersionTLS12}
				fmt.Printf("rig serving %s on https://%s\n", l.App.Spec.Name, ln.Addr())
				if cert == "" {
					fmt.Printf("certificate fingerprint (SHA-256): %s\n", fp)
				}
				fmt.Println(dim("sign in: rig login https://<this host>" + portOf(ln.Addr().String())))
				err = srv.ServeTLS(ln, "", "")
			}
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
	c.Flags().StringVar(&addr, "addr", ":7443", "address to listen on")
	c.Flags().StringVar(&cert, "cert", "", "TLS certificate (default: a self-signed one kept by the server)")
	c.Flags().StringVar(&key, "key", "", "TLS key for --cert")
	c.Flags().StringSliceVar(&hosts, "host", nil, "names the self-signed certificate is for (besides this host and localhost)")
	c.Flags().BoolVar(&plain, "plain-http", false, "no TLS, for a loopback address behind a TLS-terminating proxy")
	c.Flags().StringVar(&pull, "pull", "", "git pull --ff-only the project this often (1m), reopening it when it changes")
	c.Flags().BoolVar(&quiet, "no-agents", false, "do not run agents and report schedules")
	c.Flags().StringVar(&oidc.Issuer, "oidc-issuer", "", "OIDC issuer URL (Keycloak realm, GitLab, Google, Okta); the secret comes from $RIG_OIDC_CLIENT_SECRET")
	c.Flags().StringVar(&oidc.ClientID, "oidc-client-id", "", "OIDC client id")
	c.Flags().StringVar(&oidc.RedirectURL, "oidc-redirect", "", "this server's /v1/oidc/callback URL as the provider redirects to it")
	c.Flags().StringSliceVar(&oidc.Roles, "oidc-roles", []string{"viewer"}, "roles of an account first made by an OIDC sign-in")
	c.Flags().StringVar(&oidc.GroupsClaim, "oidc-groups-claim", "groups", "ID token claim holding the groups policy binds")
	return c
}

func portOf(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	if p == "443" {
		return ""
	}
	return ":" + p
}

func keepPulled(ctx context.Context, dir string, every time.Duration, s *server.Server) {
	head := func() string {
		out, _ := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
		return strings.TrimSpace(string(out))
	}
	for at := head(); ; {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only", "-q").CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, red("✗")+" git pull: "+strings.TrimSpace(string(out)))
			continue
		}
		if now := head(); now != at {
			at = now
			fmt.Println(dim("project changed (" + now[:min(8, len(now))] + "): reopening"))
			// ponytail: agents and report schedules keep the project as it was at start; restart rig serve for theirs
			s.Close()
		}
	}
}

func adminCommand() *cobra.Command {
	c := &cobra.Command{Use: "admin", Short: "server accounts, tokens, roles and the audit log"}
	var roles, groups []string
	var totp, noPassword bool
	users := &cobra.Command{
		Use:   "users [add|rm|passwd|totp <name>]",
		Short: "accounts of the rig server on this machine",
		Example: `  rig admin users
  rig admin users add alice --role dev --group backend --totp
  rig admin users passwd alice`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := server.OpenStore(filepath.Join(serverDir(), "users.json"))
			if err != nil {
				return err
			}
			if len(args) == 0 {
				all := st.Users()
				var rows [][]string
				for _, n := range engine.SortedKeys(all) {
					u := all[n]
					how := "password"
					if u.OIDC {
						how = "oidc"
					} else if u.TOTP != "" {
						how += "+totp"
					}
					rows = append(rows, []string{n, strings.Join(u.Roles, ","), strings.Join(u.Groups, ","), how})
				}
				printTable(os.Stdout, []string{"USER", "ROLES", "GROUPS", "SIGN-IN"}, rows)
				return nil
			}
			if len(args) != 2 {
				return errors.New("usage: rig admin users add|rm|passwd|totp <name>")
			}
			name := args[1]
			u, exists := st.User(name)
			switch args[0] {
			case "rm":
				return st.RemoveUser(name)
			case "add", "passwd", "totp":
				if args[0] == "add" && exists {
					return fmt.Errorf("%s exists", name)
				}
				if args[0] != "add" && !exists {
					return fmt.Errorf("no user %s", name)
				}
			default:
				return fmt.Errorf("unknown %q: add, rm, passwd or totp", args[0])
			}
			if cmd.Flags().Changed("role") || args[0] == "add" {
				u.Roles = roles
			}
			if cmd.Flags().Changed("group") {
				u.Groups = groups
			}
			pw := ""
			if args[0] != "totp" && !noPassword {
				if pw, err = readSecret("password for " + name); err != nil {
					return err
				}
				if len(pw) < 10 {
					return errors.New("a password needs at least 10 characters")
				}
			}
			if totp || args[0] == "totp" {
				u.TOTP = server.NewTOTP()
				fmt.Printf("TOTP secret: %s\n%s\n", u.TOTP, server.TOTPURL("rig", name, u.TOTP))
			}
			if err := st.PutUser(name, u, pw); err != nil {
				return err
			}
			fmt.Println(green("✓") + " " + name)
			return nil
		},
	}
	users.Flags().StringSliceVar(&roles, "role", []string{"viewer"}, "roles (built in: viewer, dev, operator, admin; or the project's policy roles)")
	users.Flags().StringSliceVar(&groups, "group", nil, "groups, for policy bind: group:<name>")
	users.Flags().BoolVar(&totp, "totp", false, "also require a TOTP code (prints the secret to scan)")
	users.Flags().BoolVar(&noPassword, "no-password", false, "keep the password as it is")

	var ttl string
	tokens := &cobra.Command{
		Use:   "tokens [create <name> | rm <id>]",
		Short: "your API tokens on a server (-r): for CI and scripts; they cannot reauth or approve",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.remote == "" {
				return errors.New("tokens are a server's: rig -r <server> admin tokens")
			}
			c, err := remoteClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			switch {
			case len(args) == 0:
				var ts []server.Token
				if err := c.Do(ctx, "/v1/tokens", nil, nil, &ts); err != nil {
					return err
				}
				sort.Slice(ts, func(i, j int) bool { return ts[i].Expires.Before(ts[j].Expires) })
				var rows [][]string
				for _, t := range ts {
					rows = append(rows, []string{t.ID, t.Name, t.Expires.Format("2006-01-02 15:04")})
				}
				printTable(os.Stdout, []string{"ID", "NAME", "EXPIRES"}, rows)
				return nil
			case len(args) == 2 && args[0] == "create":
				var out struct{ Token, ID string }
				if err := c.Do(ctx, "/v1/tokens", nil, map[string]string{"name": args[1], "ttl": ttl}, &out); err != nil {
					return err
				}
				fmt.Println(out.Token)
				fmt.Fprintln(os.Stderr, dim("shown once; id "+out.ID+" (rig admin tokens rm "+out.ID+" revokes it)"))
				return nil
			case len(args) == 2 && args[0] == "rm":
				return c.Do(ctx, "DELETE /v1/tokens/"+args[1], nil, nil, nil)
			}
			return errors.New("usage: rig admin tokens [create <name> | rm <id>]")
		},
	}
	tokens.Flags().StringVar(&ttl, "ttl", "720h", "how long a new token lasts")

	rolesCmd := &cobra.Command{
		Use:   "roles",
		Short: "the roles policy knows on a server: the project's and the built-in ones",
		Args:  cobra.NoArgs,
		RunE: withApp(func(_ context.Context, a *engine.App, _ []string) error {
			p := server.WithDefaults(a.Spec.Policy)
			var rows [][]string
			for _, n := range engine.SortedKeys(p.Roles) {
				r := p.Roles[n]
				rows = append(rows, []string{n, r.Extends, strings.Join(r.Allow, " "), strings.Join(r.Confirm, " "),
					strings.Join(r.Reauth, " "), strings.Join(r.Approve, " "), strings.Join(r.Deny, " ")})
			}
			printTable(os.Stdout, []string{"ROLE", "EXTENDS", "ALLOW", "CONFIRM", "REAUTH", "APPROVE", "DENY"}, rows)
			return nil
		}),
	}

	var who, action string
	var limit int
	audit := &cobra.Command{
		Use:   "audit",
		Short: "who changed what, when, decided how (on the server with -r)",
		Args:  cobra.NoArgs,
		RunE: withEngine(func(ctx context.Context, e api.Engine, _ []string) error {
			f, err := e.Query(ctx, "audit", map[string]any{"who": who, "action": action, "limit": limit})
			if err != nil {
				return err
			}
			printFrame(f)
			return nil
		}),
	}
	audit.Flags().StringVar(&who, "who", "", "only subjects containing this")
	audit.Flags().StringVar(&action, "action", "", "only actions containing this")
	audit.Flags().IntVar(&limit, "limit", 50, "newest this many")
	c.AddCommand(users, tokens, rolesCmd, audit)
	return c
}
