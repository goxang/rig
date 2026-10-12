package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/goxang/rig/spec"
)

const secretZone = "viewsecret"

// checkSecret records whether the tab's plugin still lacks its credential.
func (t *viewTab) checkSecret(m *model) {
	t.needSecret = m.app != nil && len(t.secrets) > 0 && !m.app.CredentialSet(t.plugin, t.secrets)
}

// secretBanner is the row above a tab whose plugin has no credential: a click or S asks for it.
func (t *viewTab) secretBanner(m *model, w int) string {
	text := fmt.Sprintf(" ● %s is not set: its screens stay empty. Press S or click here to set it ", t.secrets[0])
	m.zone(secretZone, 0, 0, w, 1)
	return sAmber.Render(truncate(text, w))
}

// askSecret asks for the plugin's credential without echo and keeps it with rig secret set.
func (t *viewTab) askSecret(m *model) tea.Cmd {
	name := t.secrets[0]
	m.ask("set "+name+" (a personal access token; kept in your config directory)", "", func(v string) tea.Cmd {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		if err := spec.SetSecret(m.app.Spec.Name, name, v); err != nil {
			m.setStatus(name+": "+err.Error(), true)
			return nil
		}
		t.needSecret = false
		m.setStatus(name+" saved", false)
		if t.fetch != nil {
			t.fetch.Forget()
		}
		t.walkVisible(func(wn *wnode) { wn.loading, wn.err = false, nil })
		return t.refresh(m)
	})
	m.prompt.input.EchoMode = textinput.EchoPassword
	return nil
}
