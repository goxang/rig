package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/engine"
)

// saveReport measures a reports: entry over the window the user confirms and writes it to a file.
func saveReport(m *model, window time.Duration) {
	names := m.app.ReportNames()
	if len(names) == 0 {
		m.setStatus("no reports: in "+m.app.Spec.File+" (name the metrics to report there)", true)
		return
	}
	var desc []string
	for _, n := range names {
		desc = append(desc, m.app.Spec.Reports[n].Help)
	}
	m.pick("report", names, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		name := c[0]
		m.ask("window (until now)", window.String(), func(v string) tea.Cmd {
			d, err := time.ParseDuration(strings.TrimSpace(v))
			if err != nil || d <= 0 {
				m.setStatus("window: want a duration like 20m", true)
				return nil
			}
			a, ctx := m.app, m.ctx
			m.busy++
			m.setStatus("measuring "+name+"…", false)
			return func() tea.Msg {
				c, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				end := time.Now()
				rep, err := a.MeasureReport(c, name, "", end.Add(-d), end)
				if err != nil {
					return statusMsg{text: "report " + name + ": " + err.Error(), err: true}
				}
				file := a.ReportFile(name, end)
				if err := engine.WriteReportFile(file, rep.Markdown); err != nil {
					return statusMsg{text: "report " + name + ": " + err.Error(), err: true}
				}
				return statusMsg{text: "report saved: " + file}
			}
		})
		return nil
	})
}
