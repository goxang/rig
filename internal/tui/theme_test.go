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
