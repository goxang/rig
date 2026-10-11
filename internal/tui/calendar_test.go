package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/goxang/rig/protocol"
)

func TestCalendarCountsEvents(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.Local)
	f := &protocol.Frame{Fields: []protocol.Field{{Name: "time", Type: "time"}}, Columns: [][]any{{
		now.Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), now.AddDate(0, 0, 3).Format(time.RFC3339), now.AddDate(0, 1, 0).Format(time.RFC3339),
	}}}
	got := ansi.Strip(calendarWidget(f, protocol.Node{Widget: "calendar"}, 70, now))
	for _, want := range []string{"October 2026", "11•2", "14•1", "31"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "32") || strings.Count(got, "•") != 2 {
		t.Errorf("calendar:\n%s", got)
	}
}

// A widget version this rig lacks renders the fallback a plugin gave.
func TestNewerWidgetFallsBack(t *testing.T) {
	tab := newViewTab("x", nil, "")
	fb := protocol.Node{Widget: "text", Props: map[string]any{"text": "fallback here"}}
	for widget, want := range map[string]string{"calendar@2": "fallback here", "calendar@1": "October", "agenda": "fallback here"} {
		n := protocol.Node{Widget: widget, Fallback: &fb}
		got := ansi.Strip(tab.leaf(nil, &wnode{n: n}, 0, 0, 70, 10, false))
		if widget == "calendar@1" {
			want = time.Now().Format("January")
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q", widget, got)
		}
	}
}

func TestTableFilter(t *testing.T) {
	tab := newViewTab("x", nil, "")
	f := &protocol.Frame{Fields: []protocol.Field{{Name: "title"}}, Columns: [][]any{{"psw-deck-tabesh", "Shetab", "Overview"}}}
	wn := &wnode{n: protocol.Node{Widget: "table"}, frame: f}
	tab.focus = wn
	for _, k := range []string{"/", "t", "a"} {
		tab.key(nil, tea.KeyPressMsg{Code: rune(k[0]), Text: k})
	}
	got := ansi.Strip(tab.tableBody(&model{}, wn, 0, 0, 60, 10, true))
	if !strings.Contains(got, "psw-deck-tabesh") || !strings.Contains(got, "Shetab") || strings.Contains(got, "Overview") || !strings.Contains(got, "2 of 3") {
		t.Fatalf("%s", got)
	}
	if !tab.clearFilter() || tab.focus.filter != "" {
		t.Fatal("esc did not clear the filter")
	}
}
