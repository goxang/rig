package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Theme is the UI's colours. Text, Dim, Placeholder, Panel, Bar and the three backgrounds are for a
// dark terminal; Light holds their light-terminal values (empty: the default theme's).
type Theme struct {
	// Base is the theme this one starts from; a theme file sets only what it changes.
	Base string `yaml:"base,omitempty"`

	Accent      string `yaml:"accent,omitempty"`
	OnAccent    string `yaml:"on_accent,omitempty"`
	Green       string `yaml:"green,omitempty"`
	Amber       string `yaml:"amber,omitempty"`
	Red         string `yaml:"red,omitempty"`
	Purple      string `yaml:"purple,omitempty"`
	themeShades `yaml:",inline"`
	Light       themeShades `yaml:"light,omitempty"`
	// Series are the chart colours, in order.
	Series []string `yaml:"series,omitempty,flow"`
}

type themeShades struct {
	Text        string `yaml:"text,omitempty"`
	Dim         string `yaml:"dim,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty"`
	Panel       string `yaml:"panel,omitempty"`
	Bar         string `yaml:"bar,omitempty"`
	Cursor      string `yaml:"cursor,omitempty"`
	Selected    string `yaml:"selected,omitempty"`
	Hover       string `yaml:"hover,omitempty"`
}

var themes = map[string]Theme{
	"default": {Accent: "#5794F2", OnAccent: "#FFFFFF", Green: "#73BF69", Amber: "#FF9830", Red: "#F2495C", Purple: "#B877D9",
		themeShades: themeShades{Text: "#D8D9DA", Dim: "#7B7F85", Placeholder: "#A8ADB3", Panel: "#2C3235", Bar: "#181B1F", Cursor: "#2F5A9E", Selected: "#22344F", Hover: "#1C2430"},
		Light:       themeShades{Text: "#1F1F1F", Dim: "#7A7A7A", Placeholder: "#5A5A5A", Panel: "#D0D0D0", Bar: "#ECECEC", Cursor: "#B4CCF5", Selected: "#DCE7FB", Hover: "#EEF2F8"},
		Series:      []string{"#73BF69", "#F2CC0C", "#5794F2", "#FF780A", "#F2495C", "#B877D9", "#8AB8FF", "#FADE2A", "#96D98D", "#FFB357"}},
	"nord": {Base: "default", Accent: "#88C0D0", OnAccent: "#2E3440", Green: "#A3BE8C", Amber: "#EBCB8B", Red: "#BF616A", Purple: "#B48EAD",
		themeShades: themeShades{Text: "#ECEFF4", Dim: "#7B88A1", Placeholder: "#D8DEE9", Panel: "#434C5E", Bar: "#2E3440", Cursor: "#4C566A", Selected: "#3B4252", Hover: "#363E4C"},
		Series:      []string{"#A3BE8C", "#EBCB8B", "#88C0D0", "#D08770", "#BF616A", "#B48EAD", "#81A1C1", "#8FBCBB", "#5E81AC", "#E5E9F0"}},
	"dracula": {Base: "default", Accent: "#BD93F9", OnAccent: "#282A36", Green: "#50FA7B", Amber: "#FFB86C", Red: "#FF5555", Purple: "#FF79C6",
		themeShades: themeShades{Text: "#F8F8F2", Dim: "#6272A4", Placeholder: "#BFBFBF", Panel: "#44475A", Bar: "#21222C", Cursor: "#6272A4", Selected: "#44475A", Hover: "#343746"},
		Series:      []string{"#50FA7B", "#F1FA8C", "#BD93F9", "#FFB86C", "#FF5555", "#FF79C6", "#8BE9FD", "#69FF94", "#D6ACFF", "#FFFFA5"}},
	"gruvbox": {Base: "default", Accent: "#83A598", OnAccent: "#282828", Green: "#B8BB26", Amber: "#FABD2F", Red: "#FB4934", Purple: "#D3869B",
		themeShades: themeShades{Text: "#EBDBB2", Dim: "#928374", Placeholder: "#D5C4A1", Panel: "#504945", Bar: "#1D2021", Cursor: "#665C54", Selected: "#3C3836", Hover: "#32302F"},
		Series:      []string{"#B8BB26", "#FABD2F", "#83A598", "#FE8019", "#FB4934", "#D3869B", "#8EC07C", "#D79921", "#458588", "#B16286"}},
	"catppuccin": {Base: "default", Accent: "#89B4FA", OnAccent: "#1E1E2E", Green: "#A6E3A1", Amber: "#FAB387", Red: "#F38BA8", Purple: "#CBA6F7",
		themeShades: themeShades{Text: "#CDD6F4", Dim: "#7F849C", Placeholder: "#BAC2DE", Panel: "#45475A", Bar: "#181825", Cursor: "#585B70", Selected: "#313244", Hover: "#2A2B3C"},
		Light:       themeShades{Text: "#4C4F69", Dim: "#8C8FA1", Placeholder: "#5C5F77", Panel: "#BCC0CC", Bar: "#E6E9EF", Cursor: "#ACB0BE", Selected: "#CCD0DA", Hover: "#DCE0E8"},
		Series:      []string{"#A6E3A1", "#F9E2AF", "#89B4FA", "#FAB387", "#F38BA8", "#CBA6F7", "#94E2D5", "#F5C2E7", "#74C7EC", "#EBA0AC"}},
	"mono": {Base: "default", Accent: "#E0E0E0", OnAccent: "#000000", Green: "#A8A8A8", Amber: "#D8D8D8", Red: "#FFFFFF", Purple: "#C0C0C0",
		themeShades: themeShades{Text: "#E0E0E0", Dim: "#808080", Placeholder: "#B0B0B0", Panel: "#4A4A4A", Bar: "#1A1A1A", Cursor: "#5A5A5A", Selected: "#3A3A3A", Hover: "#2A2A2A"},
		Light:       themeShades{Text: "#111111", Dim: "#777777", Placeholder: "#444444", Panel: "#BBBBBB", Bar: "#EEEEEE", Cursor: "#BBBBBB", Selected: "#DDDDDD", Hover: "#EEEEEE"},
		Series:      []string{"#FFFFFF", "#C0C0C0", "#909090", "#E0E0E0", "#A8A8A8", "#787878", "#D0D0D0", "#B8B8B8", "#989898", "#F0F0F0"}},
}

func themeDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "rig", "themes")
}

func themeChoiceFile() string {
	if d := themeDir(); d != "" {
		return filepath.Join(filepath.Dir(d), "theme")
	}
	return ""
}

// ThemeNames are the built-in themes and the files in ~/.config/rig/themes (name.yaml), sorted.
func ThemeNames() []string {
	seen := map[string]bool{}
	for n := range themes {
		seen[n] = true
	}
	files, _ := filepath.Glob(filepath.Join(themeDir(), "*.yaml"))
	for _, f := range files {
		seen[strings.TrimSuffix(filepath.Base(f), ".yaml")] = true
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// LoadTheme resolves a theme by name: a file in the themes folder wins over a built-in, and a theme
// starts from its base (default: "default"), so it needs only the colours it changes.
func LoadTheme(name string) (Theme, error) {
	return loadTheme(name, 0)
}

func loadTheme(name string, depth int) (Theme, error) {
	if depth > 5 {
		return Theme{}, fmt.Errorf("theme %s: bases loop", name)
	}
	t, ok := themes[name]
	if raw, err := os.ReadFile(filepath.Join(themeDir(), name+".yaml")); err == nil {
		t = Theme{}
		if err := yaml.Unmarshal(raw, &t); err != nil {
			return Theme{}, fmt.Errorf("theme %s: %w", name, err)
		}
		ok = true
	}
	if !ok {
		return Theme{}, fmt.Errorf("no theme %q (have %s)", name, strings.Join(ThemeNames(), ", "))
	}
	if name == "default" && t.Base == "" {
		return t, nil
	}
	base, err := loadTheme(cmpOr(t.Base, "default"), depth+1)
	if err != nil {
		return Theme{}, err
	}
	return overlay(base, t), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// overlay is base with every colour t sets put over it.
func overlay(base, t Theme) Theme {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	shades := func(dst *themeShades, s themeShades) {
		set(&dst.Text, s.Text)
		set(&dst.Dim, s.Dim)
		set(&dst.Placeholder, s.Placeholder)
		set(&dst.Panel, s.Panel)
		set(&dst.Bar, s.Bar)
		set(&dst.Cursor, s.Cursor)
		set(&dst.Selected, s.Selected)
		set(&dst.Hover, s.Hover)
	}
	out := base
	out.Base = ""
	set(&out.Accent, t.Accent)
	set(&out.OnAccent, t.OnAccent)
	set(&out.Green, t.Green)
	set(&out.Amber, t.Amber)
	set(&out.Red, t.Red)
	set(&out.Purple, t.Purple)
	shades(&out.themeShades, t.themeShades)
	if t.Light != (themeShades{}) || t.themeShades != (themeShades{}) {
		// a theme that sets its dark shades but not light ones takes the default light ones (fitTheme)
		out.Light = t.Light
	}
	if len(t.Series) > 0 {
		out.Series = t.Series
	}
	return out
}

// CurrentTheme is $RIG_THEME, else what `rig theme <name>` chose, else "default".
func CurrentTheme() string {
	if n := os.Getenv("RIG_THEME"); n != "" {
		return n
	}
	if f := themeChoiceFile(); f != "" {
		if raw, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw))
		}
	}
	return "default"
}

// UseTheme makes name the theme rig opens with from now on.
func UseTheme(name string) error {
	if _, err := LoadTheme(name); err != nil {
		return err
	}
	f := themeChoiceFile()
	if f == "" {
		return fmt.Errorf("no config directory for the theme choice")
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(name+"\n"), 0o644)
}

// SaveThemeFile writes theme name, in full, to the themes folder as a starting point to edit.
func SaveThemeFile(name, from string) (string, error) {
	t, err := LoadTheme(from)
	if err != nil {
		return "", err
	}
	raw, err := yaml.Marshal(t)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(themeDir(), 0o755); err != nil {
		return "", err
	}
	f := filepath.Join(themeDir(), name+".yaml")
	head := "# rig theme: colours as #RRGGBB. Keep only what you change and set base: to start from another theme.\n# Text, dim, panel, bar and the backgrounds are for dark terminals; light: holds light-terminal values.\n"
	return f, os.WriteFile(f, append([]byte(head), raw...), 0o644)
}

// The lightest dark and the darkest light terminal background a theme must read on.
const darkTerm, lightTerm = "#262626", "#F2F2F2"

func rgb(hex string) (r, g, b float64, ok bool) {
	var x uint32
	if _, err := fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%06x", &x); err != nil || len(strings.TrimPrefix(hex, "#")) != 6 {
		return 0, 0, 0, false
	}
	return float64(x>>16) / 255, float64(x>>8&0xff) / 255, float64(x&0xff) / 255, true
}

func luminance(hex string) float64 {
	r, g, b, _ := rgb(hex)
	lin := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrast is the WCAG contrast ratio of two colours, 1 to 21.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// readable is fg, mixed toward white (dark backgrounds) or black (light ones) just enough to reach
// ratio against every background; colours that are not #RRGGBB pass through.
func readable(fg string, ratio float64, bgs ...string) string {
	r, g, b, ok := rgb(fg)
	if !ok || len(bgs) == 0 {
		return fg
	}
	worst := func(c string) float64 {
		w := 21.0
		for _, bg := range bgs {
			if _, _, _, ok := rgb(bg); ok {
				w = min(w, contrast(c, bg))
			}
		}
		return w
	}
	target := 1.0
	if luminance(bgs[0]) > 0.4 {
		target = 0
	}
	best, bestW := fg, worst(fg)
	for i := 1; i <= 20 && bestW < ratio; i++ {
		f := float64(i) / 20
		mix := func(c float64) int { return int(math.Round((c + (target-c)*f) * 255)) }
		c := fmt.Sprintf("#%02X%02X%02X", mix(r), mix(g), mix(b))
		if w := worst(c); w > bestW {
			best, bestW = c, w
		}
	}
	return best
}
