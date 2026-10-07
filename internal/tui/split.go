package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// splitGeo is a draggable border between two panes, in screen cells: the panes span total cells
// from start, across (x) or down (y).
type splitGeo struct {
	start, total, minA, minB int
	down                     bool
	// fromEnd sizes the second pane (the chat on the right), not the first
	fromEnd bool
}

// paneSize is the first pane's size of split name (the second's when fromEnd): the user's dragged
// share, else def; the border at its edge becomes draggable. x, y, h or w place that border.
func (m *model) paneSize(name string, g splitGeo, def, x, y, length int) int {
	size := def
	if f, ok := m.splitFrac[name]; ok {
		size = int(f*float64(g.total) + 0.5)
	}
	size = max(g.minA, min(size, g.total-g.minB))
	at := size
	if g.fromEnd {
		at = g.total - size
	}
	if m.splitGeo == nil {
		m.splitGeo = map[string]splitGeo{}
	}
	if g.down {
		g.start += m.originY
		m.splitGeo[name] = g
		m.zone("split:"+name, x, y+at, length, 1)
	} else {
		m.splitGeo[name] = g
		m.zone("split:"+name, x+at-1, y, 1, length)
	}
	return size
}

// borderAt is the split whose border is at x, y; borders win over whatever else is drawn there.
func (m *model) borderAt(x, y int) string {
	for _, z := range m.zones {
		if name, ok := strings.CutPrefix(z.id, "split:"); ok && x >= z.x && x < z.x+z.w && y >= z.y && y < z.y+z.h {
			return name
		}
	}
	return ""
}

// splitDrag moves the border being dragged; the release saves every split for the next session.
func (m *model) splitDrag(e tea.MouseMsg) {
	g := m.splitGeo[m.splitting]
	pos := e.X
	if g.down {
		pos = e.Y
	}
	size := pos - g.start
	if !g.down {
		size++
	}
	if g.fromEnd {
		size = g.total - size
	}
	size = max(g.minA, min(size, g.total-g.minB))
	if m.splitFrac == nil {
		m.splitFrac = map[string]float64{}
	}
	m.splitFrac[m.splitting] = float64(size) / float64(max(1, g.total))
	if e.Action == tea.MouseActionRelease {
		m.splitting = ""
		saveSplits(m.splitFrac)
	}
}

func splitsFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "rig", "panes.json")
}

func loadSplits() map[string]float64 {
	out := map[string]float64{}
	if f := splitsFile(); f != "" {
		if raw, err := os.ReadFile(f); err == nil {
			_ = json.Unmarshal(raw, &out)
		}
	}
	return out
}

func saveSplits(s map[string]float64) {
	f := splitsFile()
	if f == "" {
		return
	}
	raw, _ := json.MarshalIndent(s, "", "  ")
	_ = os.MkdirAll(filepath.Dir(f), 0o755)
	_ = os.WriteFile(f, raw, 0o644)
}
