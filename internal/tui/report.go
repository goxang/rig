package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/goxang/rig/engine"
)

// saveReport measures a reports: entry: over a window until now, or by its reporter (start now,
// stop and measure since then), and writes it to a file.
func saveReport(m *model, window time.Duration) {
	names := m.app.ReportNames()
	if len(names) == 0 {
		m.setStatus("no reports: in "+m.app.Spec.File+" (name the metrics, traces and queries to report there)", true)
		return
	}
	var desc []string
	for _, n := range names {
		d := m.app.Spec.Reports[n].Help
		if at, ok := m.app.ReporterStarted(n); ok {
			d = "reporter running since " + at.Format("15:04:05") + " · " + d
		}
		desc = append(desc, d)
	}
	m.pick("report", names, desc, 0, false, func(c []string) tea.Cmd {
		if len(c) == 0 {
			return nil
		}
		name := c[0]
		a := m.app
		measure := func(what string, f func(context.Context) (*engine.ReportResult, error)) tea.Cmd {
			m.busy++
			m.setStatus(what+"…", false)
			ctx := m.ctx
			return func() tea.Msg {
				c, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				rep, err := f(c)
				if err != nil {
					return statusMsg{text: "report " + name + ": " + err.Error(), err: true}
				}
				file, err := a.SaveReport(rep, "")
				if err != nil {
					return statusMsg{text: "report " + name + ": " + err.Error(), err: true}
				}
				return statusMsg{text: "report saved: " + file}
			}
		}
		if at, ok := a.ReporterStarted(name); ok {
			return measure("measuring "+name+" since "+at.Format("15:04:05"), func(c context.Context) (*engine.ReportResult, error) {
				return a.StopReporter(c, name, "")
			})
		}
		opts := []string{"start the reporter", "the last " + window.String()}
		m.pick(name, opts, []string{"its window opens now; W again stops it and saves the report", "measure a window until now"}, 0, false, func(c []string) tea.Cmd {
			if len(c) == 0 {
				return nil
			}
			if c[0] == opts[0] {
				at, err := a.StartReporter(name)
				if err != nil {
					m.setStatus(err.Error(), true)
					return nil
				}
				m.setStatus(name+" reporter started at "+at.Format("15:04:05")+" · W stops it", false)
				return nil
			}
			m.ask("window (until now)", window.String(), func(v string) tea.Cmd {
				d, err := time.ParseDuration(strings.TrimSpace(v))
				if err != nil || d <= 0 {
					m.setStatus("window: want a duration like 20m", true)
					return nil
				}
				return measure("measuring "+name, func(c context.Context) (*engine.ReportResult, error) {
					end := time.Now()
					return a.MeasureReport(c, name, "", end.Add(-d), end)
				})
			})
			return nil
		})
		return nil
	})
}
