package tui

import (
	"strings"
	"testing"

	"github.com/TaJirax/CottenRouter/internal/installer"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func assertFits(t *testing.T, m Model, width, height int) {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	view := updated.(Model).View()
	if lipgloss.Width(view) > width || lipgloss.Height(view) > height {
		t.Fatalf("rendered %dx%d into %dx%d:\n%s", lipgloss.Width(view), lipgloss.Height(view), width, height, view)
	}
	if !strings.Contains(view, "quit") {
		t.Fatalf("exit control is not visible:\n%s", view)
	}
}

func TestLayoutsFitOrdinaryTerminalsAfterResize(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {60, 20}} {
		m := New("", "")
		m.services = append(m.services, serviceState{"CottenRouter", "active"})
		for _, spec := range m.projects {
			m.services = append(m.services, serviceState{spec.Name, "active"})
		}
		for tab := 0; tab < 4; tab++ {
			m.tab = tab
			assertFits(t, m, size[0], size[1])
		}
		for index, spec := range m.projects {
			if spec.Kind == installer.ConfigSlipGate {
				continue
			}
			m.cursor = index
			m.beginForm("install")
			assertFits(t, m, size[0], size[1])
			m.form = false
		}
	}
}

func TestAllSevenProjectsAndInstallControlsAreVisibleAt80x24(t *testing.T) {
	m := New("", "")
	m.width, m.height, m.tab = 80, 24, 1
	view := m.View()
	for _, spec := range m.projects {
		if !strings.Contains(view, spec.Name) {
			t.Fatalf("%s missing:\n%s", spec.Name, view)
		}
	}
	for _, want := range []string{"Space select", "i easy install", "WhiteDNS", "v0.2.2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
}
