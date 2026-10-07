package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadThemeFileOverBase(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", os.Getenv("XDG_CONFIG_HOME"))
	if err := os.MkdirAll(themeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(themeDir(), name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mine", "base: catppuccin\naccent: '#FF00FF'\n")
	got, err := LoadTheme("mine")
	if err != nil {
		t.Fatal(err)
	}
	if got.Accent != "#FF00FF" || got.Green != themes["catppuccin"].Green || got.Text != themes["catppuccin"].Text || len(got.Series) == 0 {
		t.Fatalf("mine = %+v", got)
	}
	if got.Light != themes["catppuccin"].Light {
		t.Fatalf("a file that sets no shades keeps its base's light ones, got %+v", got.Light)
	}
	write("a", "base: b\n")
	write("b", "base: a\n")
	if _, err := LoadTheme("a"); err == nil {
		t.Fatal("bases loop loaded")
	}
	if err := UseTheme("nope"); err == nil {
		t.Fatal("unknown theme chosen")
	}
	if err := UseTheme("mine"); err != nil || CurrentTheme() != "mine" {
		t.Fatalf("UseTheme: %v, current %s", err, CurrentTheme())
	}
}

func TestThemesReadable(t *testing.T) {
	for _, n := range []string{"default", "nord", "dracula", "gruvbox", "catppuccin", "mono"} {
		th, err := LoadTheme(n)
		if err != nil {
			t.Fatal(err)
		}
		d, l := fitTheme(th)
		for _, f := range []fitted{d, l} {
			bgs := []string{f.term, f.sh.Bar, f.sh.Cursor, f.sh.Selected}
			for _, c := range []struct {
				name, fg string
				min      float64
			}{{"text", f.text, 4.5}, {"dim", f.dim, 2.5}, {"placeholder", f.ph, 3}, {"accent", f.accent, 3}, {"green", f.green, 3}, {"amber", f.amber, 3}, {"red", f.red, 3}, {"purple", f.purple, 3}} {
				for _, bg := range bgs {
					if r := contrast(c.fg, bg); r < c.min {
						t.Errorf("%s on %s: %s %s over %s is %.1f:1, want %.1f", n, f.term, c.name, c.fg, bg, r, c.min)
					}
				}
			}
			if r := contrast(f.onAccent, f.accent); r < 3 {
				t.Errorf("%s on %s: active tab %.1f:1", n, f.term, r)
			}
		}
	}
}
