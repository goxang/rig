package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

Changes are checked before they run: other environments are out of reach, and on protected or
Kubernetes environments a dangerous step you did not ask for in so many words waits for your yes.`,
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
	c.AddCommand(aiConfigCommand(), &cobra.Command{
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

func aiConfigCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "config [key=value ...]",
		Short: "show or change the AI setup (~/.config/rig/ai.json): backend, provider, model, fast_model, url, api_key, proxy, autocomplete, disabled",
		Long: `Without arguments, prints the setup and what it resolves to on this machine. key= clears a setting.

  backend       opencode | claude (default: opencode when installed, else claude)
  provider      own | opencode | openai | 9router | deepseek | anthropic
                own: your opencode config or Claude Code login as they are (default when opencode has a model set)
                opencode: opencode's free models (default otherwise)
  model         e.g. opencode/big-pickle, deepseek-chat, sonnet
  fast_model    model for inline completions (default: haiku on claude, else model)
  url, api_key  the endpoint and key of openai, 9router, deepseek or anthropic
  proxy         every AI request goes through it, e.g. localhost:10808 or socks5://127.0.0.1:1080
  autocomplete  false turns off suggestions while typing queries
  disabled      true turns the assistant off

RIG_AI_BACKEND, RIG_AI_PROVIDER, RIG_AI_MODEL, RIG_AI_URL, RIG_AI_API_KEY and RIG_AI_PROXY override it for one run.`,
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
			return nil
		},
	}
}

// askOnce runs one turn in the terminal: answers stream to stdout, approvals are asked on it.
func askOnce(ctx context.Context, a *engine.App, question, session string) error {
	sock := ai.SocketPath()
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	in := bufio.NewReader(os.Stdin)
	stop, err := ai.Serve(sock, func(r ai.Request) ai.Reply {
		if r.Op != "approve" {
			return ai.Reply{Text: "no rig UI here: run `rig ai` without a question for the UI"}
		}
		if !tty {
			return ai.Reply{Text: "no terminal to ask on"}
		}
		fmt.Fprint(os.Stderr, "\n"+amber("? "+r.Text)+" [y/N] ")
		line, _ := in.ReadString('\n')
		return ai.Reply{OK: strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")}
	})
	if err != nil {
		return err
	}
	defer stop()
	r, err := a.AI(sock)
	if err != nil {
		return err
	}
	if !r.Setup.Enabled() {
		return errors.New("AI is " + r.Setup.Describe())
	}
	var s *ai.Session
	if session != "" {
		if s, err = ai.LoadSession(a.AIDir(), session); err != nil {
			return err
		}
		if s.Env != a.Env.Name {
			return fmt.Errorf("session %s is bound to environment %s: rig -e %s ai -s %s", s.ID, s.Env, s.Env, s.ID)
		}
	} else {
		s = ai.NewSession(a.AIDir(), r.Setup.Backend, a.Env.Name)
	}
	fmt.Fprintln(os.Stderr, dim("◆ "+r.Setup.Describe()+" · env "+a.Env.Name))
	err = r.Turn(ctx, s, question, "", func(e ai.Event) {
		switch e.Kind {
		case "text":
			fmt.Println(strings.TrimSpace(e.Text) + "\n")
		case "tool":
			fmt.Fprintln(os.Stderr, dim("  → "+e.Text))
		case "error":
			fmt.Fprintln(os.Stderr, red("✖ "+e.Text))
		}
	})
	fmt.Fprintln(os.Stderr, dim("continue: rig ai -s "+s.ID+" \"…\"  ·  rig resume "+s.ID))
	return err
}
