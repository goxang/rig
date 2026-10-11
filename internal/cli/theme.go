package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/internal/tui"
)

func themeCommand() *cobra.Command {
	var save string
	c := &cobra.Command{
		Use:   "theme [name]",
		Short: "list the UI's colour themes, pick one, or --save a copy to edit (ctrl+p in the UI)",
		Long: "Themes are built in (default, nord, dracula, gruvbox, catppuccin, clay, ember, mono) or files in ~/.config/rig/themes/<name>.yaml\n" +
			"that set base: and only the colours they change. $RIG_THEME overrides the choice for one run.\n" +
			"Fonts and their size belong to the terminal; rig sets colours only.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if save != "" {
				from := tui.CurrentTheme()
				if len(args) == 1 {
					from = args[0]
				}
				f, err := tui.SaveThemeFile(save, from)
				if err != nil {
					return err
				}
				fmt.Printf("wrote %s from %s; edit it, then rig theme %s\n", f, from, save)
				return nil
			}
			if len(args) == 1 {
				if err := tui.UseTheme(args[0]); err != nil {
					return err
				}
				fmt.Println("theme", args[0])
				return nil
			}
			cur := tui.CurrentTheme()
			for _, n := range tui.ThemeNames() {
				mark := "  "
				if n == cur {
					mark = green("● ")
				}
				fmt.Println(mark + n)
			}
			return nil
		},
	}
	c.Flags().StringVar(&save, "save", "", "write the theme (the current one, or the one named) to ~/.config/rig/themes/<save>.yaml to edit")
	return c
}
