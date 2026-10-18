//go:build linux

package installer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// systemd-analyze verify loads units locally; it does not require systemd as
// PID 1 and does not start services. This catches unit syntax errors on WSL1
// and in ordinary Linux CI as well as on production systemd hosts.
func TestSoarSystemdUnitSyntax(t *testing.T) {
	analyzer, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	spec, _ := FindSpec("soar")
	spec.WorkDir = t.TempDir()
	spec.ConfigPath = filepath.Join(spec.WorkDir, "soar.env")
	for path, contents := range map[string]string{
		filepath.Join(spec.WorkDir, "soar-server"): "#!/bin/sh\nexit 0\n",
		spec.ConfigPath: "SOAR_ZONE=syntax.example\nSOAR_LISTEN=127.0.0.1:5305\nSOAR_ALLOW_PORTS=80,443\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	unitPath := filepath.Join(spec.WorkDir, "soar-syntax.service")
	if err := os.WriteFile(unitPath, []byte(soarUnit(spec)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{"verify", "--man=no"}
	// Since v250, this makes warnings in the target unit fail verification
	// without inheriting unrelated warnings from the host's dependency units.
	help, _ := exec.CommandContext(ctx, analyzer, "--help").CombinedOutput()
	if strings.Contains(string(help), "--recursive-errors=") {
		args = append(args, "--recursive-errors=no")
	}
	args = append(args, unitPath)
	command := exec.CommandContext(ctx, analyzer, args...)
	command.Env = append(os.Environ(), "SYSTEMD_LOG_TARGET=console", "SYSTEMD_LOG_LEVEL=warning")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated Soar unit failed systemd verification: %v\n%s", err, output)
	}
	// Older systemd versions can return zero despite parser warnings. Inspect
	// target-unit diagnostics too, while allowing unrelated host-unit warnings.
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, unitPath) || strings.Contains(line, filepath.Base(unitPath)) {
			t.Fatalf("generated Soar unit produced a systemd warning: %s", line)
		}
	}
}
