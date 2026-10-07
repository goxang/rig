package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/engine"
)

// AIChat opens the terminal UI with the assistant's chat open on session id ("" a new one). Set by main.
var AIChat func(ctx context.Context, a *engine.App, session string) error

// PickSession lets a person choose one of rows (id, kind, saved, what) in the terminal, closing
// others with close on the way; -1 is no choice. Set by main.
var PickSession func(rows [][]string, close func(i int) error) (int, error)

func aiCommand() *cobra.Command {
	var session string
	var cont bool
	c := &cobra.Command{
		Use:   "ai [question]",
		Short: "the assistant: your opencode or Claude Code over rig's tools, bound to this environment; without a question, the UI with its chat open",
		Long: `rig ai runs your own opencode or Claude Code with rig as its only tools, the project directory as its
only workspace, and bound to one environment. Set it up once with rig ai config (opencode's free
models work with no setup; an existing opencode or Claude Code login is used as it is).

  rig ai                         the UI with the chat open (@ in the UI opens it on any screen)
  rig ai why is parsersvc failing?
  rig ai -c "and restart it"     continue the last conversation
  rig ai config provider=deepseek api_key=sk-… proxy=localhost:10808
  rig resume                     pick a conversation (or a saved UI session) to continue or close

Changes are checked before they run: other environments are out of reach, reads run at once, and
every change shows its command and waits for your yes (or "always" for that command, this session).
rig ai log lists what the assistant ran, who confirmed it and how it went.`,
		Args: cobra.ArbitraryArgs,
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if cont && session == "" {
				session = "last"
			}
			if len(args) == 0 {
				if AIChat == nil {
					return errors.New("no UI in this build")
				}
				return AIChat(ctx, a, session)
			}
			return askOnce(ctx, a, strings.Join(args, " "), session)
		}),
	}
	c.Flags().StringVarP(&session, "session", "s", "", "continue this conversation")
	c.Flags().BoolVarP(&cont, "continue", "c", false, "continue the last conversation")
	c.AddCommand(aiConfigCommand(), aiConnectCommand(), aiLogCommand(), &cobra.Command{
		Use:   "sessions",
		Short: "conversations of this project, newest first",
		RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
			ss, err := ai.ListSessions(a.AIDir())
			var rows [][]string
			for _, s := range ss {
				rows = append(rows, []string{s.ID, s.Updated.Format("2006-01-02 15:04"), s.Env, fmt.Sprint(len(s.Messages)), s.Title})
			}
			printTable(os.Stdout, []string{"SESSION", "UPDATED", "ENV", "MESSAGES", "TITLE"}, rows)
			return err
		}),
	}, &cobra.Command{
		Use:   "check",
		Short: "ask the configured model for a one-word completion, to see the setup works",
		RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
			r, err := a.AI("")
			if err != nil {
				return err
			}
			fmt.Println(dim("setup: ") + r.Setup.Describe())
			if !r.Setup.Enabled() {
				return errors.New(r.Setup.Why)
			}
			start := time.Now()
			out, err := r.Complete(ctx, "a shell command", "echo hello wor")
			if err != nil {
				return err
			}
			fmt.Printf("%s %q %s\n", green("✓ completed \"echo hello wor\" with"), out, dim(time.Since(start).Round(100*time.Millisecond).String()))
			return nil
		}),
	})
	return c
}

func aiConnectCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "connect [opencode|claude]",
		Short:     "find the installed opencode / Claude Code and use it as you have it set up (its login, config and model)",
		ValidArgs: []string{ai.BackendOpencode, ai.BackendClaude},
		Args:      cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			found := ai.Detect()
			if len(found) == 0 {
				return fmt.Errorf("neither opencode nor claude is installed: install one (https://opencode.ai, https://claude.com/claude-code)")
			}
			for _, f := range found {
				fmt.Println(dim("found ") + f.Describe())
			}
			pick := found[0]
			if len(args) > 0 {
				i := slices.IndexFunc(found, func(f ai.Found) bool { return f.Backend == args[0] })
				if i < 0 {
					return fmt.Errorf("%s is not installed", args[0])
				}
				pick = found[i]
			}
			if err := ai.Connect(pick); err != nil {
				return err
			}
			c, _ := ai.LoadConfig()
			fmt.Println(green("● ") + ai.Resolve(c).Describe() + dim("  (rig ai check tests it)"))
			return nil
		},
	}
}

func aiConfigCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "config [key=value ...]",
		Short: "show or change the AI setup (~/.config/rig/ai.json): backend, provider, model, effort, fast_model, fast_url, url, api_key, proxy, autocomplete, disabled",
		Long: `Without arguments, prints the setup and what it resolves to on this machine. key= clears a setting.

  backend       opencode | claude (default: opencode when installed, else claude)
  provider      own | opencode | openai | 9router | deepseek | anthropic | ollama | lmstudio
                ollama, lmstudio: a model on this machine, no key (rig ai config lists what they serve)
                own: your opencode config or Claude Code login as they are (default when opencode has a model set)
                opencode: opencode's free models (default otherwise)
  model         e.g. opencode/big-pickle, deepseek-chat, sonnet
  effort        reasoning level of chat turns: Claude Code's --effort (low … max), opencode's --variant
  fast_model    model for inline completions (default: haiku on claude, else model)
  fast_url, fast_api_key
                an OpenAI-compatible endpoint completions call directly: one HTTP request (~1s)
                instead of starting the backend (several seconds), whatever the chat uses
  url, api_key  the endpoint and key of openai, 9router, deepseek or anthropic
  proxy         every AI request goes through it, e.g. localhost:10808 or socks5://127.0.0.1:1080
  autocomplete  false turns off suggestions while typing queries
  disabled      true turns the assistant off

In the UI: /model, /effort, /fast and /autocomplete in the chat (@), ctrl+t in a query prompt.
RIG_AI_<KEY> (RIG_AI_MODEL, RIG_AI_FAST_URL, ...) overrides a setting for one run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ai.LoadConfig()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				for _, kv := range args {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return fmt.Errorf("%q: give key=value", kv)
					}
					if err := c.Set(strings.TrimSpace(k), v); err != nil {
						return err
					}
				}
				if err := ai.SaveConfig(c); err != nil {
					return err
				}
			}
			var rows [][]string
			for _, k := range ai.Keys {
				rows = append(rows, []string{k, c.Get(k)})
			}
			printTable(os.Stdout, []string{"SETTING", "VALUE"}, rows)
			f, _ := ai.ConfigFile()
			s := ai.Resolve(c)
			line := green("● ") + s.Describe()
			if !s.Enabled() {
				line = red("○ ") + s.Describe()
			}
			fmt.Println("\n" + line + "\n" + dim(f))
			for _, f := range ai.Detect() {
				fmt.Printf("\n%s %s\n  %s\n", green("◆"), f.Describe(), dim("use it: rig ai connect "+f.Backend))
			}
			for _, l := range ai.DetectLocal(cmd.Context()) {
				if len(l.Models) == 0 {
					fmt.Printf("\n%s %s at %s serves no model yet: %s\n", amber("◆"), l.Provider, l.URL, map[string]string{"ollama": "ollama pull qwen2.5-coder", "lmstudio": "load one in LM Studio"}[l.Provider])
					continue
				}
				fmt.Printf("\n%s %s at %s serves: %s\n  %s\n", green("◆"), l.Provider, l.URL, strings.Join(l.Models, ", "),
					dim("use it: rig ai config provider="+l.Provider+" model="+l.Models[0]))
			}
			return nil
		},
	}
}

// askOnce runs one turn in the terminal: answers stream to stdout, approvals are asked on it.
func askOnce(ctx context.Context, a *engine.App, question, session string) error {
	c, err := converse(a, session)
	if err != nil {
		return err
	}
	defer c.close()
	_, err = c.turn(ctx, question)
	c.hint()
	return err
}

// conversation is a terminal conversation with the assistant over rig's tools: each change it
// wants waits for a yes typed here.
type conversation struct {
	r     *ai.Runner
	s     *ai.Session
	in    *bufio.Reader
	tty   bool
	close func()
}

func converse(a *engine.App, session string) (*conversation, error) {
	sock := ai.SocketPath()
	c := &conversation{tty: term.IsTerminal(int(os.Stdin.Fd())), in: bufio.NewReader(os.Stdin)}
	var mu sync.Mutex
	always := map[string]bool{}
	stop, err := ai.Serve(sock, func(r ai.Request) ai.Reply {
		switch r.Op {
		case "audit":
			fmt.Fprintln(os.Stderr, dim("  "+r.Text))
			return ai.Reply{OK: true}
		case "approve":
		default:
			return ai.Reply{Text: "no rig UI here: run `rig ai` without a question for the UI"}
		}
		if !c.tty {
			return ai.Reply{NoOne: true, Text: "no terminal to ask on"}
		}
		mu.Lock()
		defer mu.Unlock()
		if always[r.Command] {
			return ai.Reply{OK: true, Text: "allowed for this session"}
		}
		fmt.Fprint(os.Stderr, "\n"+amber("? "+r.Text)+" [y/N/a=always this session] ")
		line, _ := c.in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return ai.Reply{OK: true, Text: "the user, on the terminal"}
		case "a", "always":
			always[r.Command] = true
			return ai.Reply{OK: true, Text: "the user, on the terminal (always this session)"}
		}
		return ai.Reply{}
	})
	if err != nil {
		return nil, err
	}
	c.close = stop
	if c.r, err = a.AI(sock); err != nil {
		stop()
		return nil, err
	}
	if !c.r.Setup.Enabled() {
		stop()
		return nil, errors.New("AI is " + c.r.Setup.Describe())
	}
	if session != "" {
		if c.s, err = ai.LoadSession(a.AIDir(), session); err != nil {
			stop()
			return nil, err
		}
		if c.s.Env != a.Env.Name {
			stop()
			return nil, fmt.Errorf("session %s is bound to environment %s: rig -e %s ai -s %s", c.s.ID, c.s.Env, c.s.Env, c.s.ID)
		}
	} else {
		c.s = ai.NewSession(a.AIDir(), c.r.Setup.Backend, a.Env.Name)
	}
	fmt.Fprintln(os.Stderr, dim("◆ "+c.r.Setup.Describe()+" · env "+a.Env.Name))
	return c, nil
}

// turn sends text and prints the answer as it comes; it returns the answer's text.
func (c *conversation) turn(ctx context.Context, text string) (string, error) {
	var said []string
	err := c.r.Turn(ctx, c.s, text, "", func(e ai.Event) {
		switch e.Kind {
		case "text":
			said = append(said, strings.TrimSpace(e.Text))
			fmt.Println(strings.TrimSpace(e.Text) + "\n")
		case "tool":
			fmt.Fprintln(os.Stderr, dim("  → "+e.Text))
		case "error":
			fmt.Fprintln(os.Stderr, red("✖ "+e.Text))
		}
	})
	return strings.Join(said, "\n\n"), err
}

// ask reads one line typed on the terminal; ok is false when there is no terminal or input ended.
func (c *conversation) ask(prompt string) (string, bool) {
	if !c.tty {
		return "", false
	}
	fmt.Fprint(os.Stderr, amber(prompt))
	line, err := c.in.ReadString('\n')
	return strings.TrimSpace(line), err == nil
}

func (c *conversation) hint() {
	fmt.Fprintln(os.Stderr, dim("continue: rig ai -s "+c.s.ID+" \"…\"  ·  rig resume "+c.s.ID))
}

func aiLogCommand() *cobra.Command {
	var n int
	var all, asJSON bool
	c := &cobra.Command{
		Use:   "log",
		Short: "what the assistant ran on this environment: time, command, result, who confirmed",
		Args:  cobra.NoArgs,
		RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
			es, err := ai.ReadAudit(a.AuditFile())
			if err != nil {
				return err
			}
			if !all {
				kept := es[:0]
				for _, e := range es {
					if e.Risk != ai.Read.String() || e.Result != "ok" {
						kept = append(kept, e)
					}
				}
				es = kept
			}
			if n > 0 && len(es) > n {
				es = es[len(es)-n:]
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(es)
			}
			if len(es) == 0 {
				fmt.Println(dim("the assistant changed nothing on " + a.Env.Name + " yet (--all shows its reads too)"))
				return nil
			}
			var rows [][]string
			for _, e := range es {
				rows = append(rows, []string{e.At.Local().Format("01-02 15:04:05"), e.Result, e.Risk, e.By, e.Command})
			}
			printTable(os.Stdout, []string{"TIME", "RESULT", "RISK", "CONFIRMED BY", "COMMAND"}, rows)
			return nil
		}),
	}
	c.Flags().IntVarP(&n, "last", "n", 50, "the newest n entries (0: all)")
	c.Flags().BoolVar(&all, "all", false, "reads too, not only changes and refusals")
	c.Flags().BoolVar(&asJSON, "json", false, "entries as JSON")
	return c
}
