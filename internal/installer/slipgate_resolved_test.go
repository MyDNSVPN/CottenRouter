package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSlipGateResolvedPermissionsBeforeNativeRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native service compatibility")
	}
	for _, kind := range []string{"private", "readable", "modified", "symlink", "parent-symlink", "directory", "absent"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			dropin := filepath.Join(dir, "resolved.conf.d", "slipgate-no-stub.conf")
			if err := os.MkdirAll(filepath.Dir(dropin), 0755); err != nil {
				t.Fatal(err)
			}
			content := "[Resolve]\nDNSStubListener=no\n"
			if kind == "modified" {
				content += "DNS=192.0.2.1\n"
			}
			if kind != "absent" && kind != "directory" {
				if err := os.WriteFile(dropin, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "readable" {
				if err := os.Chmod(dropin, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				target := filepath.Join(dir, "target")
				if err := os.Rename(dropin, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dropin); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "parent-symlink" {
				parent := filepath.Dir(dropin)
				if err := os.Rename(parent, parent+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-real", parent); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "directory" {
				if err := os.Mkdir(dropin, 0700); err != nil {
					t.Fatal(err)
				}
			}
			real := filepath.Join(dir, "real-systemctl")
			// Inspect permissions inside the service call, proving repair preceded restart.
			body := "#!/bin/sh\nprintf '%s\\n' \"$*\"\nif [ -f \"$DROPIN\" ]; then stat -c %a \"$DROPIN\"; fi\n"
			if err := os.WriteFile(real, []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			shim := filepath.Join(dir, "systemctl")
			if err := os.WriteFile(shim, []byte(slipGateResolvedSystemctlScript(protectedSystemctlScript(real, []string{"cottenrouter"}), dropin)), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", shim, "restart", "systemd-resolved.service")
			cmd.Env = append(os.Environ(), "DROPIN="+dropin)
			output, err := cmd.CombinedOutput()
			valid := kind == "private" || kind == "readable" || kind == "absent"
			if valid {
				if err != nil || !strings.Contains(string(output), "restart systemd-resolved.service") {
					t.Fatalf("restart failed: %v %s", err, output)
				}
				if kind != "absent" && !strings.Contains(string(output), "\n644\n") {
					t.Fatalf("daemon saw unreadable config: %s", output)
				}
			} else {
				if err == nil || strings.Contains(string(output), "restart systemd-resolved.service") {
					t.Fatalf("unexpected file allowed restart: %v %s", err, output)
				}
				if kind != "directory" {
					info, statErr := os.Stat(dropin)
					if statErr != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("unexpected file permissions changed: %v %v", info, statErr)
					}
				}
			}
			if kind != "absent" && kind != "directory" {
				got, readErr := os.ReadFile(dropin)
				if readErr != nil || string(got) != content {
					t.Fatalf("configuration contents changed: %v", readErr)
				}
			}
		})
	}
}
