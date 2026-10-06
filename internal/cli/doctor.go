package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/engine"
)

func doctorCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "doctor",
		Short: "check this environment: rig.yaml, tools, cluster or daemon, ${VARS}, ports, components, AI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var checks []engine.Check
			a, err := open()
			if err != nil {
				checks = append(checks, engine.Check{Name: "rig.yaml", Status: engine.CheckFail, Detail: err.Error(), Fix: "rig init writes one; rig schema validates it in your editor"})
			} else {
				defer a.Close()
				checks = a.Doctor(cmd.Context())
			}
			checks = append(checks, aiCheck())
			failed := 0
			for _, c := range checks {
				if c.Status == engine.CheckFail {
					failed++
				}
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(checks); err != nil {
					return err
				}
			} else {
				for _, c := range checks {
					mark := map[engine.CheckStatus]string{engine.CheckOK: green("ok  "), engine.CheckWarn: amber("warn"), engine.CheckFail: red("fail")}[c.Status]
					fmt.Printf("%s  %-28s %s\n", mark, c.Name, c.Detail)
					if c.Fix != "" && c.Status != engine.CheckOK {
						fmt.Printf("      %s\n", dim("fix: "+c.Fix))
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the checks as JSON")
	return c
}

func aiCheck() engine.Check {
	cfg, err := ai.LoadConfig()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return engine.Check{Name: "AI", Status: engine.CheckWarn, Detail: err.Error(), Fix: "rig ai config"}
	}
	s := ai.Resolve(cfg)
	if !s.Enabled() {
		return engine.Check{Name: "AI", Status: engine.CheckWarn, Detail: s.Why, Fix: "rig ai config (optional: rig works without it)"}
	}
	return engine.Check{Name: "AI", Status: engine.CheckOK, Detail: s.Describe()}
}
