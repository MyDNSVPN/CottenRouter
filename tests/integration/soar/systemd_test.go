//go:build linux

package soarintegration

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/soar"
)

// Exercise the real public-only egress guard in a fresh process under the
// service sandbox. No public connection is made by PublicOnly itself.
func TestSoarSystemdDestinationGuard(t *testing.T) {
	if mode := os.Getenv("COTTENROUTER_GUARD_CHILD"); mode != "" {
		err := soar.PublicOnly("tcp", "8.8.8.8:443", nil)
		if mode == "permit" && err != nil {
			t.Fatal("sandbox blocked public destination guard: ", err)
		}
		if mode == "deny" && err == nil {
			t.Fatal("negative control did not block interface enumeration")
		}
		if err := soar.PublicOnly("tcp", "127.0.0.1:443", nil); err == nil {
			t.Fatal("private destination guard was bypassed")
		}
		return
	}
	if os.Getenv("COTTENROUTER_TEST_SYSTEMD") != "1" {
		t.Skip("requires explicitly enabled live systemd tests")
	}
	if os.Geteuid() != 0 || os.Getenv("WSL_DISTRO_NAME") != "CottenRouter-Test" {
		t.Fatal("requires the dedicated root WSL2 test distribution")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("/run", "cottenrouter-guard-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	probe := filepath.Join(directory, "guard.test")
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, binary, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"permit", "deny"} {
		families := "AF_INET AF_INET6 AF_UNIX"
		if mode == "permit" {
			families += " AF_NETLINK"
		}
		args := []string{"--quiet", "--wait", "--pipe", "--collect", "--unit=" + fmt.Sprintf("cottenrouter-guard-%d-%s", os.Getpid(), mode),
			"--property=NoNewPrivileges=yes", "--property=CapabilityBoundingSet=", "--property=ProtectSystem=strict", "--property=ProtectHome=yes", "--property=PrivateTmp=yes", "--property=RestrictAddressFamilies=" + families,
			"--setenv=COTTENROUTER_GUARD_CHILD=" + mode, probe, "-test.v", "-test.run=^TestSoarSystemdDestinationGuard$"}
		output, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("sandbox %s failed: %v\n%s", mode, err, output)
		}
		if !strings.Contains(string(output), "PASS") {
			t.Fatalf("sandbox helper did not run: %s", output)
		}
	}
	if _, err := net.InterfaceAddrs(); err != nil {
		t.Fatal("host interface enumeration: ", err)
	}
	t.Log("production socket permissions permit public egress; negative control and private-destination rejection passed")
}
