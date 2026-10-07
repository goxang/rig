package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
)

func whyCommand() *cobra.Command {
	var since time.Duration
	var out string
	var report, noAI, asJSON, fix bool
	c := &cobra.Command{
		Use:   "why <service or symptom>",
		Short: "incident mode: gather logs, traces, metrics, alerts and changes about a symptom, rank the likely causes, and have the AI find the root cause and propose a fix; answer \"fix\" to apply it",
		Long: `rig why collects, without any AI: the state, restarts and health of the service the symptom names
(every service when it names none) and everything it depends on, whether the components they host
answer, error lines in their logs (and which services those lines name), failed and slow traces,
the dashboards' metrics about them, firing alerts, and recent deploys and changes. It ranks the
suspects by that evidence, then the assistant (secrets redacted, with rig's tools to look further)
writes the root cause and a proposed fix. In a terminal the conversation goes on: answer "fix" to
have it apply the fix (each change waits for your yes), ask a follow-up, or press enter to leave.
--fix applies the fix without asking first. /why <service> in the UI's chat does the same.`,
		Example: `  rig why api
  rig why checkout is slow since the deploy   # a symptom: the service it names, else every service
  rig why --fix api returns 502               # investigate, then fix
  rig why api --since 1h --report             # also save a Markdown incident report
  rig why api --no-ai --json                  # the evidence and ranking only`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeServices,
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			fmt.Fprintln(os.Stderr, dim("gathering evidence about "+strings.Join(args, " ")+" on "+a.Env.Name+"…"))
			inc, err := a.InvestigateSymptom(ctx, args, since)
			if err != nil {
				return err
			}
			save := func(analysis string) error {
				if !report && out == "" {
					return nil
				}
				file := out
				if file == "" {
					file = a.ReportFile("why-"+strings.ReplaceAll(strings.Join(args, "-"), "/", "-"), inc.At)
				}
				if err := engine.WriteReportFile(file, func(w io.Writer) { inc.Markdown(w, analysis) }); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, green("✓")+" report: "+file)
				return nil
			}
			var conv *conversation
			if !noAI && !asJSON {
				if conv, err = converse(a, ""); err != nil {
					fmt.Fprintln(os.Stderr, amber("◆ no AI analysis ("+firstLineOf(err.Error())+"): rig's own ranking below"))
				}
			}
			if conv == nil {
				analysis := ""
				if asJSON && !noAI {
					analysis, _ = whyAnalysis(ctx, a, inc)
				}
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(map[string]any{"incident": inc, "analysis": analysis})
				}
				inc.Markdown(os.Stdout, "")
				return save("")
			}
			defer conv.close()
			fmt.Fprintln(os.Stderr, dim(inc.Brief()))
			analysis, err := conv.turn(ctx, inc.Prompt())
			if err != nil {
				conv.hint()
				return err
			}
			if err := save(analysis); err != nil {
				return err
			}
			next := ""
			if fix {
				next = "fix"
			}
			for {
				if next == "" {
					var ok bool
					if next, ok = conv.ask(`"fix" applies it, or ask a follow-up (enter leaves) › `); !ok || next == "" {
						break
					}
				}
				text := next
				if strings.EqualFold(next, "fix") {
					text = engine.FixPrompt
				}
				if _, err := conv.turn(ctx, text); err != nil {
					fmt.Fprintln(os.Stderr, red("✖ "+err.Error()))
				}
				next = ""
			}
			conv.hint()
			return nil
		}),
	}
	c.Flags().DurationVar(&since, "since", 15*time.Minute, "how far back logs, traces, metrics and changes are read")
	c.Flags().BoolVar(&report, "report", false, "also save a Markdown incident report under the project's data dir")
	c.Flags().StringVarP(&out, "out", "o", "", "save the report to this file instead")
	c.Flags().BoolVar(&noAI, "no-ai", false, "evidence and rig's ranking only")
	c.Flags().BoolVar(&asJSON, "json", false, "the incident as JSON")
	c.Flags().BoolVar(&fix, "fix", false, "after the analysis, apply the proposed fix (each change still waits for your yes)")
	return c
}

// whyAnalysis asks the assistant about an incident, without tools; its answer cites the evidence IDs.
func whyAnalysis(ctx context.Context, a *engine.App, inc *engine.Incident) (string, error) {
	r, err := a.AI("")
	if err != nil {
		return "", err
	}
	if !r.Setup.Enabled() {
		return "", fmt.Errorf("AI is %s", r.Setup.Describe())
	}
	fmt.Fprintln(os.Stderr, dim("◆ asking "+r.Setup.Describe()+"…"))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return r.Ask(ctx, inc.Prompt())
}

func firstLineOf(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}
