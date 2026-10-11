package tui

import (
	"bufio"
	"cmp"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/internal/sh"
)

// opener is an app that can open a document: the label shown and the command that opens it.
type opener struct {
	name, how string
	run       []string
}

// openWith offers the apps installed here that open link (a file:// or web address): the
// system's default first, then the others registered for its type, the browsers, and the editor.
func (m *model) openWith(title, link string) tea.Cmd {
	return func() tea.Msg {
		target := link
		if u, err := url.Parse(link); err == nil && u.Scheme == "file" {
			target = u.Path
		}
		apps := openers(target)
		return thenMsg(func() tea.Cmd {
			var names, desc []string
			byName := map[string]opener{}
			for _, a := range apps {
				if _, dup := byName[a.name]; dup {
					continue
				}
				byName[a.name] = a
				names, desc = append(names, a.name), append(desc, a.how)
			}
			names, desc = append(names, "copy the link"), append(desc, link)
			m.pick("open "+cmp.Or(title, filepath.Base(target))+" with", names, desc, 0, false, func(c []string) tea.Cmd {
				if len(c) == 0 {
					return nil
				}
				if c[0] == "copy the link" {
					copyText(link)
					m.setStatus("copied "+link, false)
					return nil
				}
				a := byName[c[0]]
				if a.how == "editor" {
					return openFileAt(target, 1)
				}
				x := sh.New(a.run[0], a.run[1:]...).Exec(context.Background())
				sh.Detach(x)
				if err := x.Start(); err != nil {
					m.setStatus(a.name+": "+err.Error(), true)
					return nil
				}
				go func() { _ = x.Wait() }()
				m.setStatus("opened in "+a.name, false)
				return nil
			})
			return nil
		})
	}
}

func openers(target string) []opener {
	local := !strings.Contains(target, "://")
	var out []opener
	if runtime.GOOS == "darwin" {
		out = append(out, opener{"default app", "open", []string{"open", target}})
		for _, app := range []string{"Microsoft Word", "Microsoft Excel", "Microsoft PowerPoint", "LibreOffice", "Pages", "Preview",
			"Google Chrome", "Firefox", "Safari", "Microsoft Edge", "Visual Studio Code", "TextEdit"} {
			if _, err := os.Stat("/Applications/" + app + ".app"); err == nil {
				out = append(out, opener{app, "open -a", []string{"open", "-a", app, target}})
			}
		}
	} else {
		out = append(out, desktopOpeners(target, local)...)
	}
	if local {
		out = append(out, opener{"editor in this terminal", "editor", nil})
	}
	return out
}

// desktopOpeners are the .desktop apps registered for target's MIME type (text/plain ones too for
// any text), then the web browsers, default first, launched with gtk-launch.
func desktopOpeners(target string, local bool) []opener {
	mime := "x-scheme-handler/https"
	if local {
		if b, err := sh.New("xdg-mime", "query", "filetype", target).Output(context.Background()); err == nil {
			mime = strings.TrimSpace(string(b))
		}
	}
	want := []string{mime}
	if strings.HasPrefix(mime, "text/") && mime != "text/plain" {
		want = append(want, "text/plain")
	}
	if local {
		want = append(want, "x-scheme-handler/https")
	}
	def := map[string]bool{}
	for _, mt := range want {
		if b, err := sh.New("xdg-mime", "query", "default", mt).Output(context.Background()); err == nil {
			def[strings.TrimSpace(string(b))] = true
		}
	}
	apps := desktopApps()
	var out []opener
	for _, mt := range want {
		var tier []opener
		for _, a := range apps {
			if slices.Contains(a.mimes, mt) {
				how := strings.Replace(mt, "x-scheme-handler/https", "web browser", 1)
				o := opener{a.name, how, []string{"gtk-launch", a.id, target}}
				if def[a.id] {
					o.how += ", default"
					tier = append([]opener{o}, tier...)
				} else {
					tier = append(tier, o)
				}
			}
		}
		out = append(out, tier...)
	}
	if len(out) == 0 && sh.Have("xdg-open") {
		out = append(out, opener{"default app", "xdg-open", []string{"xdg-open", target}})
	}
	return out
}

type desktopApp struct {
	id, name string
	mimes    []string
}

// desktopApps reads the .desktop files of $XDG_DATA_HOME and $XDG_DATA_DIRS; a user's file hides
// the system one of the same id.
func desktopApps() []desktopApp {
	home, _ := os.UserHomeDir()
	dirs := []string{cmp.Or(os.Getenv("XDG_DATA_HOME"), filepath.Join(home, ".local/share"))}
	dirs = append(dirs, filepath.SplitList(cmp.Or(os.Getenv("XDG_DATA_DIRS"), "/usr/local/share:/usr/share"))...)
	seen := map[string]bool{}
	var out []desktopApp
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "applications", "*.desktop"))
		for _, f := range files {
			id := filepath.Base(f)
			if seen[id] {
				continue
			}
			seen[id] = true
			if a, ok := readDesktop(f); ok {
				a.id = id
				out = append(out, a)
			}
		}
	}
	slices.SortFunc(out, func(a, b desktopApp) int { return strings.Compare(a.name, b.name) })
	return out
}

func readDesktop(file string) (desktopApp, bool) {
	f, err := os.Open(file)
	if err != nil {
		return desktopApp{}, false
	}
	defer f.Close()
	var a desktopApp
	entry := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			entry = l == "[Desktop Entry]"
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !entry || !ok {
			continue
		}
		switch k {
		case "Name":
			a.name = v
		case "MimeType":
			a.mimes = strings.FieldsFunc(v, func(r rune) bool { return r == ';' })
		case "NoDisplay", "Hidden":
			if v == "true" {
				return a, false
			}
		}
	}
	return a, a.name != "" && len(a.mimes) > 0
}
