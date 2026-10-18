package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/TaJirax/CottenRouter/internal/installer"
	"github.com/TaJirax/CottenRouter/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
)

func TestOverviewRendersOperationalData(t *testing.T) {
	model := New("127.0.0.1:9088", "/tmp/config.json")
	model.width = 110
	model.stats = telemetry.Snapshot{
		UptimeSec: 65, MemoryBytes: 2 << 20, Goroutines: 8,
		Protocols: []telemetry.Metric{{Protocol: "dns/udp", Route: "cottendns", BytesIn: 1024, BytesOut: 2048, Sessions: 4, Active: 1}},
	}
	model.services = []serviceState{{name: "CottenRouter", state: "active"}}
	view := model.View()
	for _, want := range []string{"COTTENROUTER CONTROL DECK", "ONLINE", "CottenRouter", "dns/udp", "cottendns", "2.0 MiB RAM"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, view)
		}
	}
}

func TestInstallFormKeepsNativeProjectChoices(t *testing.T) {
	model := New("", "")
	model.tab = 1
	model.cursor = 3 // thefeed
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if !got.form || len(got.fields) != 4 {
		t.Fatalf("thefeed form fields = %d, open=%v", len(got.fields), got.form)
	}
}

func TestSoarFormAllowsOnlyOneZoneAndUDP(t *testing.T) {
	for _, id := range []string{"soar", "soar22"} {
		t.Run(id, func(t *testing.T) {
			model := New("", "")
			model.tab = 1
			for i, spec := range model.projects {
				if spec.ID == id {
					model.cursor = i
				}
			}
			if model.projects[model.cursor].ID != id {
				t.Fatalf("%s is missing from the project manager", id)
			}
			// Unsupported flags from an older state must not enter a new form.
			model.projectStates[id] = installer.ProjectState{ID: id, Installed: true, EnableTCP: true, EnableDoT: true, EnableDoH: true}
			updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			got := updated.(Model)
			if !got.form || len(got.fields) != 2 || got.fields[0].key != "domain" || got.fields[1].key != "port" {
				t.Fatalf("%s needs exactly a zone and private port: %+v", id, got.fields)
			}
			for _, key := range []tea.KeyType{tea.KeyCtrlT, tea.KeyCtrlD, tea.KeyCtrlH} {
				updated, _ = got.Update(tea.KeyMsg{Type: key})
				got = updated.(Model)
			}
			if got.enableTCP || got.enableDoT || got.enableDoH {
				t.Fatal("enabled an unsupported transport")
			}
			view := got.formView(110)
			if strings.Contains(view, "Ctrl+T") || !strings.Contains(view, "UDP-only") {
				t.Fatalf("form advertises unsupported transport controls:\n%s", view)
			}
			want := "latest official Lantern source"
			if id == "soar22" {
				want = "older Lantern engine v0.2.2"
			}
			if !strings.Contains(view, want) {
				t.Fatalf("form does not identify its engine source (%q):\n%s", want, view)
			}
		})
	}
}

func TestProjectManagerDistinguishesSoarVersions(t *testing.T) {
	model := New("", "")
	view := model.installView(140)
	for _, want := range []string{"Soar 2.2", ":5305", ":5306", "latest Lantern", "WhiteDNS", "Lantern v0.2.2", "amd64"} {
		if !strings.Contains(view, want) {
			t.Fatalf("project manager does not distinguish Soar versions (%q):\n%s", want, view)
		}
	}
}

func TestProjectManagerSelectionAndInstalledState(t *testing.T) {
	model := New("", "")
	model.width, model.tab = 120, 1
	model.projectStates["cottendns"] = installer.ProjectState{ID: "cottendns", Installed: true, Integrated: true, Domain: "dns.example", PrivatePort: 5301}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got := updated.(Model)
	view := got.View()
	for _, want := range []string{"[x]", "integrated", "dns.example", "advanced", "detach", "purge"} {
		if !strings.Contains(view, want) {
			t.Fatalf("project manager missing %q:\n%s", want, view)
		}
	}
}

func TestCancellingFormClearsQueuedInstalls(t *testing.T) {
	model := New("", "")
	model.tab = 1
	model.cursor = 0
	model.queue = []int{1, 2}
	model.beginForm("install")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(Model)
	if got.form || len(got.queue) != 0 {
		t.Fatalf("cancel left form=%v queue=%v", got.form, got.queue)
	}
	if !strings.Contains(strings.ToLower(got.notice), "cancelled") {
		t.Fatalf("cancel notice = %q", got.notice)
	}
}

func TestFailedInstallClearsQueueAndNamesProject(t *testing.T) {
	model := New("", "")
	model.queue = []int{1, 2}
	updated, _ := model.Update(installDoneMsg{
		err:       errors.New("installer exited"),
		operation: "install",
		project:   "CottenDNS",
	})
	got := updated.(Model)
	if len(got.queue) != 0 {
		t.Fatalf("failed install left queue=%v", got.queue)
	}
	for _, want := range []string{"Install", "CottenDNS", "installer exited"} {
		if !strings.Contains(got.notice, want) {
			t.Fatalf("contextual notice missing %q: %q", want, got.notice)
		}
	}
}

func TestUninstalledProjectActionsAreGated(t *testing.T) {
	for _, key := range []rune{'u', 'x', 'v', 'V', 'a', 's'} {
		model := New("", "")
		model.tab = 1
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		got := updated.(Model)
		if cmd != nil || got.confirm != "" {
			t.Fatalf("key %q launched unavailable action: cmd=%v confirm=%q", key, cmd != nil, got.confirm)
		}
		if got.notice == "" {
			t.Fatalf("key %q did not explain why it was unavailable", key)
		}
	}
}

func TestInstalledSlipGateUsesNativeAdvancedSettings(t *testing.T) {
	model := New("", "")
	model.tab = 1
	model.cursor = 4
	model.projectStates["slipgate"] = installer.ProjectState{ID: "slipgate", Installed: true, Integrated: true}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.form || cmd == nil || got.operation != "advanced settings" {
		t.Fatalf("SlipGate did not use Advanced: form=%v cmd=%v operation=%q", got.form, cmd != nil, got.operation)
	}

	model = got
	model.busy = false // the interactive Advanced child has returned
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	got = updated.(Model)
	if cmd != nil || !strings.Contains(got.notice, "individual tunnel") {
		t.Fatalf("SlipGate exposed a fake aggregate restart: cmd=%v notice=%q", cmd != nil, got.notice)
	}
}

func TestFreshSlipGateStartsInstallerWithoutSinglePortForm(t *testing.T) {
	model := New("", "")
	model.tab = 1
	model.cursor = 4
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.form || cmd == nil || got.operation != "install" {
		t.Fatalf("fresh SlipGate opened misleading form: form=%v cmd=%v operation=%q", got.form, cmd != nil, got.operation)
	}
	got.width = 120
	view := got.View()
	if strings.Contains(view, ":5310") || !strings.Contains(view, "multi") {
		t.Fatalf("SlipGate row still presents a single port:\n%s", view)
	}
}

func TestSecretRevealWarnsAboutTerminalScrollback(t *testing.T) {
	model := New("", "")
	model.tab = 1
	model.projectStates["cottendns"] = installer.ProjectState{ID: "cottendns", Installed: true}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	got := updated.(Model)
	if cmd != nil || got.confirm != "reveal" || !strings.Contains(strings.ToLower(got.notice), "scrollback") {
		t.Fatalf("secret warning is incomplete: cmd=%v confirm=%q notice=%q", cmd != nil, got.confirm, got.notice)
	}
}

func TestSlipGateStatusFollowsItsTunnelsNotItsDisabledDNSRouter(t *testing.T) {
	running := "slipgate-dnsrouter.service loaded inactive dead SlipGate DNS Router\n" +
		"slipgate-dnstt-socks.service loaded active running SlipGate tunnel\n" +
		"slipgate-vaydns-ssh.service loaded active running SlipGate tunnel\n"
	if got := slipGateTunnelState(running); got != "active" {
		t.Fatalf("running tunnels reported as %q", got)
	}
	crashed := "slipgate-dnsrouter.service loaded inactive dead x\nslipgate-dnstt-socks.service loaded failed failed x\n"
	if got := slipGateTunnelState(crashed); got != "failed" {
		t.Fatalf("failed tunnel reported as %q", got)
	}
	if got := slipGateTunnelState("slipgate-dnsrouter.service loaded inactive dead x\n"); got != "" {
		t.Fatalf("DNS router alone should not count as a tunnel, got %q", got)
	}
}
