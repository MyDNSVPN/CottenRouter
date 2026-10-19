package tui

import (
	"github.com/TaJirax/CottenRouter/internal/installer"
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

func TestBackendUpdateActionStaysSeparateFromConfigureAndDetach(t *testing.T) {
	m := New("", "/custom/router.json")
	m.tab = 1
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	m = updated.(Model)
	if command != nil || m.busy {
		t.Fatal("update started for a missing backend")
	}
	m.projectStates["cottendns"] = installer.ProjectState{ID: "cottendns", Installed: true, Integrated: true}
	updated, command = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	m = updated.(Model)
	if command == nil || !m.busy || m.form || m.operation != "update" || m.confirm != "" {
		t.Fatalf("wrong update action: %+v", m)
	}
}
