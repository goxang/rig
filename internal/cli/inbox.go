package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/inbox"
)

func inboxCommand() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "inbox [approve|reject <id>]",
		Short: "writes waiting for your approval (the AI's and agents', by default): what each would do, then approve or reject",
		Args:  cobra.MaximumNArgs(2),
		RunE: withEngine(func(ctx context.Context, e api.Engine, args []string) error {
			if len(args) == 2 && (args[0] == "approve" || args[0] == "reject") {
				res, err := e.Decide(ctx, args[1], args[0] == "approve")
				if err != nil {
					return err
				}
				msg := args[1] + " " + strings.TrimSuffix(args[0], "e") + "ed"
				if res != nil && res.Message != "" {
					msg += ": " + res.Message
				}
				fmt.Println(green("✓") + " " + msg)
				return nil
			}
			if len(args) > 0 {
				return fmt.Errorf("usage: rig inbox [approve|reject <id>]")
			}
			var rows [][]string
			for _, it := range e.Inbox(ctx) {
				if !all && it.Status != inbox.Pending {
					continue
				}
				rows = append(rows, []string{it.ID, it.Status, it.Subject.String(), it.Action, argsText(it.Args), planText(it)})
			}
			if len(rows) == 0 {
				fmt.Println(dim("nothing waits for approval"))
				return nil
			}
			printTable(os.Stdout, []string{"ID", "STATUS", "WHO", "ACTION", "ARGS", "PLAN"}, rows)
			return nil
		}),
	}
	c.Flags().BoolVar(&all, "all", false, "decided items too")
	return c
}

func argsText(a map[string]any) string {
	if len(a) == 0 {
		return ""
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func planText(it inbox.Item) string {
	if it.Plan == nil {
		return ""
	}
	var parts []string
	for _, c := range it.Plan.Changes {
		b, _ := json.Marshal(c)
		parts = append(parts, string(b))
	}
	if len(parts) == 0 {
		return it.Plan.Message
	}
	return strings.Join(parts, "; ")
}
