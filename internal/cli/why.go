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
	var report, noAI, asJSON bool
	c := &cobra.Command{
		Use:   "why <service>",
		Short: "incident mode: gather evidence about a misbehaving service and its dependencies, rank the likely causes, ask the AI for the root cause",
		Long: `rig why collects, without any AI: the state, restarts and health of the service and everything it
depends on, whether the components they host answer, error lines in their logs (and which services
those lines name), failed and slow traces, firing alerts, and recent restarts and changes. It ranks
the suspects by that evidence, then asks the assistant (secrets redacted) for a root-cause summary
with hypotheses citing the evidence. /why <service> in the UI's chat does the same.`,
		Example: `  rig why api
  rig why api --since 1h --report         # also save a Markdown incident report
  rig why api -o incident.md              # ... to this file
  rig why api --no-ai --json              # the evidence and ranking only`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeServices,
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			fmt.Fprintln(os.Stderr, dim("gathering evidence about "+args[0]+" on "+a.Env.Name+"…"))
			inc, err := a.Investigate(ctx, args[0], since)
			if err != nil {
				return err
			}
			analysis := ""
			if !noAI {
				analysis, err = whyAnalysis(ctx, a, inc)
				if err != nil {
					fmt.Fprintln(os.Stderr, amber("◆ no AI analysis ("+firstLineOf(err.Error())+"): rig's own ranking below"))
				}
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"incident": inc, "analysis": analysis})
			}
			inc.Markdown(os.Stdout, analysis)
			if report || out != "" {
				file := out
				if file == "" {
					file = a.ReportFile("why-"+args[0], inc.At)
				}
				if err := engine.WriteReportFile(file, func(w io.Writer) { inc.Markdown(w, analysis) }); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, green("✓")+" report: "+file)
			}
			return nil
		}),
	}
	c.Flags().DurationVar(&since, "since", 15*time.Minute, "how far back logs, traces and changes are read")
	c.Flags().BoolVar(&report, "report", false, "also save a Markdown incident report under the project's data dir")
	c.Flags().StringVarP(&out, "out", "o", "", "save the report to this file instead")
	c.Flags().BoolVar(&noAI, "no-ai", false, "evidence and rig's ranking only")
	c.Flags().BoolVar(&asJSON, "json", false, "the incident as JSON")
	return c
}

// whyAnalysis asks the assistant about an incident; its answer cites the evidence IDs.
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
