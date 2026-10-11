package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
)

// Remote is a rig server this user signed in to.
type Remote struct {
	URL         string `json:"url"`
	User        string `json:"user,omitempty"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func remotesPath() string { return filepath.Join(host.ConfigDir(), "remotes.json") }

func loadRemotes() map[string]Remote {
	rs := map[string]Remote{}
	if b, err := os.ReadFile(remotesPath()); err == nil {
		_ = json.Unmarshal(b, &rs)
	}
	return rs
}

func saveRemotes(rs map[string]Remote) error {
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(remotesPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(remotesPath(), b, 0o600)
}

// remoteCommands work with -r; the rest need the project here.
var remoteCommands = map[string]bool{"call": true, "inbox": true, "lock": true, "unlock": true, "admin": true,
	"tokens": true, "audit": true, "mcp": true, "whoami": true, "logout": true, "login": true, "rig": false}

func checkRemote(cmd *cobra.Command) error {
	if g.remote == "" {
		return nil
	}
	for c := cmd; c != nil; c = c.Parent() {
		if remoteCommands[c.Name()] {
			return nil
		}
	}
	return fmt.Errorf("rig %s runs on this machine only; with -r: call, inbox, lock, unlock, admin tokens|audit, mcp, whoami", cmd.Name())
}

func remoteClient() (*api.Client, error) {
	r, ok := loadRemotes()[g.remote]
	if !ok {
		return nil, fmt.Errorf("no remote %q: rig login <url> --name %s", g.remote, g.remote)
	}
	return &api.Client{URL: r.URL, Token: r.Token, Env: g.env, Fingerprint: r.Fingerprint, Ask: askServer}, nil
}

// askServer answers a server's confirm or reauth on the terminal.
func askServer(hint, message string) (string, bool) {
	if hint == api.HintConfirm {
		return "", g.yes || term.IsTerminal(int(os.Stdin.Fd())) && askYes(message+": go ahead?")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", false
	}
	fmt.Fprintln(os.Stderr, amber(message))
	s, err := readSecret("password or TOTP code")
	return s, err == nil && s != ""
}

// engineFor is the remote server's engine with -r, else this project's.
func engineFor(ctx context.Context) (api.Engine, func(), error) {
	if g.remote != "" {
		c, err := remoteClient()
		return c, func() {}, err
	}
	a, err := open()
	if err != nil {
		return nil, nil, err
	}
	if a.Env == nil {
		a.Close()
		return nil, nil, errors.New("no environment: define one under environments: and set default")
	}
	e := engine.NewLocal(a, &jobs.Manager{})
	return e, func() { e.Close(); a.Close() }, nil
}

func withEngine(f func(ctx context.Context, e api.Engine, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		e, done, err := engineFor(cmd.Context())
		if err != nil {
			return err
		}
		defer done()
		return f(cmd.Context(), e, args)
	}
}

func loginCommand() *cobra.Command {
	var name, user, token, pin string
	var oidc bool
	c := &cobra.Command{
		Use:   "login <url>",
		Short: "sign in to a rig server; then rig -r <name> ... runs there",
		Example: `  rig login https://rig.example.local            # password, then a TOTP code if the account has one
  rig login https://rig.example.local --oidc     # through the identity provider, in a browser
  rig login https://rig.example.local --token $RIG_TOKEN --name ci`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			u, err := url.Parse(args[0])
			if err != nil || u.Host == "" {
				return fmt.Errorf("%q is not a URL", args[0])
			}
			if u.Scheme != "https" && os.Getenv("RIG_INSECURE_HTTP") == "" {
				return errors.New("rig servers speak HTTPS only")
			}
			if name == "" {
				name = strings.Split(u.Hostname(), ".")[0]
			}
			c := &api.Client{URL: strings.TrimSuffix(u.String(), "/"), Fingerprint: pin}
			for _, r := range loadRemotes() {
				if r.URL == c.URL && pin == "" {
					c.Fingerprint = r.Fingerprint // trusted already, under another name
				}
			}
			var info struct {
				Project string
				Env     string
				OIDC    bool
			}
			if err := c.Do(ctx, "/v1/info", nil, nil, &info); err != nil {
				var unverified *tls.CertificateVerificationError
				if pin != "" || !errors.As(err, &unverified) {
					return err
				}
				// a self-signed certificate: show its fingerprint and pin it once trusted
				fp, ferr := peerFingerprint(u.Host)
				if ferr != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "the server's certificate is not signed by a known authority.\nSHA-256 fingerprint: %s\n", fp)
				if !g.yes && !askYes("trust it (compare with what rig serve printed)?") {
					return errors.New("not trusted")
				}
				c = &api.Client{URL: c.URL, Fingerprint: fp}
				if err := c.Do(ctx, "/v1/info", nil, nil, &info); err != nil {
					return err
				}
			}
			var out struct {
				Token string
				User  string
			}
			switch {
			case token != "":
				out.Token = token
			case oidc:
				if !info.OIDC {
					return errors.New("this server has no OIDC sign-in")
				}
				state := randomState()
				fmt.Fprintf(os.Stderr, "open this in a browser to sign in:\n  %s/v1/oidc/start?state=%s\n", c.URL, state)
				openBrowser(c.URL + "/v1/oidc/start?state=" + state)
				for deadline := time.Now().Add(5 * time.Minute); ; {
					var p struct {
						Done  bool
						Token string
					}
					if err := c.Do(ctx, "/v1/oidc/poll", nil, map[string]string{"state": state}, &p); err != nil {
						return err
					}
					if p.Done {
						out.Token = p.Token
						break
					}
					if time.Now().After(deadline) {
						return errors.New("sign-in timed out")
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(time.Second):
					}
				}
			default:
				in := bufio.NewReader(os.Stdin)
				if user == "" {
					user = promptLine(in, "user", false)
				}
				pw := promptLine(in, "password", true)
				code := promptLine(in, "TOTP code (enter if none)", false)
				if err := c.Do(ctx, "/v1/login", nil, map[string]string{"user": user, "password": pw, "code": code}, &out); err != nil {
					return err
				}
			}
			c.Token = out.Token
			me := c.Session(ctx)
			if me.Name == "" {
				return errors.New("the server did not accept the token")
			}
			rs := loadRemotes()
			rs[name] = Remote{URL: c.URL, User: me.String(), Token: out.Token, Fingerprint: c.Fingerprint}
			if err := saveRemotes(rs); err != nil {
				return err
			}
			fmt.Printf("%s signed in to %s (%s) as %s; rig -r %s ...\n", green("✓"), name, info.Project, me, name)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "name for this server in -r (default: its first host label)")
	c.Flags().StringVar(&user, "user", "", "account name")
	c.Flags().StringVar(&token, "token", "", "an API token instead of a password")
	c.Flags().BoolVar(&oidc, "oidc", false, "sign in through the server's identity provider")
	c.Flags().StringVar(&pin, "fingerprint", "", "the server certificate's SHA-256, to pin it without asking")
	return c
}

func peerFingerprint(addr string) (string, error) {
	if !strings.Contains(addr, ":") {
		addr += ":443"
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // only to read the certificate shown for trust
	if err != nil {
		return "", err
	}
	defer conn.Close()
	sum := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

func logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout [name]",
		Short: "sign out of a rig server and forget it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				g.remote = args[0]
			}
			if g.remote == "" {
				return errors.New("which server? rig logout <name>")
			}
			c, err := remoteClient()
			if err != nil {
				return err
			}
			if err := c.Do(cmd.Context(), "/v1/logout", nil, map[string]string{}, nil); err != nil {
				fmt.Fprintln(os.Stderr, dim("server: "+err.Error()))
			}
			rs := loadRemotes()
			delete(rs, g.remote)
			return saveRemotes(rs)
		},
	}
}

func whoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "who rig acts as, with which roles (on the server with -r)",
		Args:  cobra.NoArgs,
		RunE: withEngine(func(ctx context.Context, e api.Engine, _ []string) error {
			me := e.Session(ctx)
			fmt.Println(me.String(), dim(strings.Join(me.Roles, ",")+" "+strings.Join(me.Groups, ",")))
			return nil
		}),
	}
}

// callCommand reaches any action or source by name, here or on a server.
func callCommand() *cobra.Command {
	var dry bool
	return &cobra.Command{
		Use:   "call [action|source] [name=value...]",
		Short: "run an action or read a source by name, through policy (on the server with -r)",
		Example: `  rig call                              # every action and source
  rig call services.status
  rig -r work call runtime.restart service=api`,
		RunE: withEngine(func(ctx context.Context, e api.Engine, args []string) error {
			p := e.Provides(ctx)
			if len(args) == 0 {
				var rows [][]string
				for _, n := range engine.SortedKeys(p.Actions) {
					rows = append(rows, []string{"action", n, p.Actions[n].Effect, p.Actions[n].Description})
				}
				for _, n := range engine.SortedKeys(p.Sources) {
					rows = append(rows, []string{"source", n, "read", p.Sources[n].Description})
				}
				printTable(os.Stdout, []string{"KIND", "NAME", "EFFECT", "WHAT"}, rows)
				return nil
			}
			in := kvArgs(args[1:])
			if _, ok := p.Actions[args[0]]; ok {
				j, res, err := e.Run(ctx, args[0], in, api.RunOpts{DryRun: dry})
				if j != nil {
					for _, l := range j.Output() {
						fmt.Println(l)
					}
				}
				if err != nil {
					return err
				}
				if res != nil {
					if res.Message != "" {
						fmt.Println(green("✓") + " " + res.Message)
					}
					if res.Frame != nil {
						printFrame(res.Frame)
					}
				}
				return nil
			}
			f, err := e.Query(ctx, args[0], in)
			if err != nil {
				return err
			}
			printFrame(f)
			return nil
		}),
	}
}

func printFrame(f *protocol.Frame) {
	var cols []string
	for _, fl := range f.Fields {
		cols = append(cols, strings.ToUpper(fl.Name))
	}
	var rows [][]string
	for i := range frames.Len(f) {
		var row []string
		for _, fl := range f.Fields {
			row = append(row, frames.Format(fl, frames.Row(f, i)[fl.Name]))
		}
		rows = append(rows, row)
	}
	printTable(os.Stdout, cols, rows)
}

// kvArgs reads name=value pairs; a value that is JSON (a number, true, a list) keeps its type.
func kvArgs(args []string) protocol.Args {
	out := protocol.Args{}
	for _, a := range args {
		k, v, _ := strings.Cut(a, "=")
		var j any
		if json.Unmarshal([]byte(v), &j) == nil {
			out[k] = j
		} else {
			out[k] = v
		}
	}
	return out
}

func lockCommands() []*cobra.Command {
	var dur, note string
	var force bool
	envArg := func(args []string) {
		if len(args) == 2 && args[0] == "env" {
			g.env = args[1]
		} else if len(args) == 1 {
			g.env = args[0]
		}
	}
	run := func(action string, args protocol.Args) func(context.Context, api.Engine, []string) error {
		return func(ctx context.Context, e api.Engine, _ []string) error {
			_, res, err := e.Run(ctx, action, args, api.RunOpts{})
			if err != nil {
				return err
			}
			fmt.Println(green("✓") + " " + res.Message)
			return nil
		}
	}
	lock := &cobra.Command{
		Use:   "lock [env <name>]",
		Short: "hold an environment so nobody else changes it (a lease; default 2h)",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			envArg(args)
			return withEngine(run("env.lock", protocol.Args{"for": dur, "note": note}))(cmd, args)
		},
	}
	lock.Flags().StringVar(&dur, "for", "2h", "how long")
	lock.Flags().StringVar(&note, "note", "", "why, for whoever finds it locked")
	unlock := &cobra.Command{
		Use:   "unlock [env <name>]",
		Short: "release your lease on an environment (--force breaks someone else's)",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			envArg(args)
			action := "env.unlock"
			if force {
				action = "env.unlock.force"
			}
			return withEngine(run(action, nil))(cmd, args)
		},
	}
	unlock.Flags().BoolVar(&force, "force", false, "break another person's lease")
	return []*cobra.Command{lock, unlock}
}

func randomState() string { return rand.Text() }

func openBrowser(u string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" || runtime.GOOS == "darwin" {
		_ = exec.Command(name, u).Start()
	}
}

// promptLine reads one line from in, without echo for a secret at a terminal.
func promptLine(in *bufio.Reader, name string, secret bool) string {
	fmt.Fprintf(os.Stderr, "%s: ", name)
	if secret && term.IsTerminal(int(os.Stdin.Fd())) {
		raw, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(raw)
	}
	line, _ := in.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}
