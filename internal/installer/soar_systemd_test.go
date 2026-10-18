//go:build linux

package installer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failRouterRestartOnce struct {
	OSRunner
	failed bool
}

func (r *failRouterRestartOnce) Run(ctx context.Context, name string, args []string, dir string, interactive bool) error {
	if !r.failed && name == "systemctl" && strings.Join(args, " ") == "restart cottenrouter" {
		r.failed = true
		return errors.New("injected router activation failure")
	}
	return r.OSRunner.Run(ctx, name, args, dir, interactive)
}

// This test intentionally installs/removes services at their real paths. The
// explicit gate and dedicated distribution check prevent accidental host runs.
func TestSoarSystemdLifecycle(t *testing.T) {
	if os.Getenv("COTTENROUTER_TEST_SYSTEMD") != "1" {
		t.Skip("set COTTENROUTER_TEST_SYSTEMD=1 in the disposable CottenRouter-Test WSL2 distribution")
	}
	if os.Geteuid() != 0 || os.Getenv("WSL_DISTRO_NAME") != "CottenRouter-Test" {
		t.Fatal("live lifecycle test requires root in the dedicated CottenRouter-Test distribution")
	}
	if name, err := os.ReadFile("/proc/1/comm"); err != nil || strings.TrimSpace(string(name)) != "systemd" {
		t.Fatal("live lifecycle test requires systemd as PID 1")
	}
	spec, _ := FindSpec("soar")
	if _, err := os.Stat(spec.WorkDir); !os.IsNotExist(err) {
		t.Fatal("live lifecycle test requires no pre-existing managed Soar directory")
	}
	const routerPath = "/etc/cottenrouter/config.json"
	previousRouter, err := os.ReadFile(routerPath)
	if err != nil {
		t.Fatal("bootstrap the router before running the live lifecycle test: ", err)
	}
	manager := DefaultManager()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		_ = manager.Runner.Run(context.Background(), "systemctl", []string{"disable", "--now", spec.Service}, "/", false)
		_ = atomicWrite(routerPath, previousRouter, 0640)
		_ = manager.Runner.Run(context.Background(), "systemctl", []string{"restart", "cottenrouter"}, "/", false)
	})
	request := Request{ProjectID: "soar", Domain: "soar.systemd.test", PrivatePort: 5305, RouterConfig: routerPath}
	plan, err := manager.Install(ctx, request, func(message string) { t.Log(message) })
	if err != nil {
		t.Fatal("live Soar installation: ", err)
	}
	request.PrivatePort = plan.DNSPort
	probe := func(zone string) { t.Helper(); probeManagedSoarSOA(t, zone) }
	probe(request.Domain)
	keyPath := filepath.Join(spec.WorkDir, "server.key")
	seed, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := Credentials("soar", false)
	if err != nil || len(credentials) != 1 || credentials[0].Sensitive {
		t.Fatalf("live public-key display: %+v, %v", credentials, err)
	}
	t.Log("actual systemd installation, private listener, frontend DNS, and public credentials passed")

	// Fail after the backend is already restarted onto its new port. Rollback
	// must restore the running listener as well as the configuration files.
	originalConfig, err := os.ReadFile(spec.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	originalRouter, err := os.ReadFile(routerPath)
	if err != nil {
		t.Fatal(err)
	}
	injected := &failRouterRestartOnce{}
	failingManager := Manager{Runner: injected, Client: manager.Client}
	changed := request
	changed.Domain, changed.PrivatePort = "rollback.soar.systemd.test", 5306
	if _, err := failingManager.Configure(ctx, changed, nil); err == nil || !injected.failed {
		t.Fatalf("activation failure was not exercised: %v", err)
	}
	for path, want := range map[string][]byte{spec.ConfigPath: originalConfig, routerPath: originalRouter} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("failed configure did not restore %s: %v", path, err)
		}
	}
	probe(request.Domain)
	t.Log("failed configure restored files and the live backend listener")

	changed.Domain = "updated.soar.systemd.test"
	updatedPlan, err := manager.Configure(ctx, changed, nil)
	if err != nil {
		t.Fatal("live configure: ", err)
	}
	changed.PrivatePort = updatedPlan.DNSPort
	probe(changed.Domain)
	after, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(seed, after) {
		t.Fatal("configuration replaced the server identity: ", err)
	}
	for _, action := range []string{"stop", "start", "restart"} {
		if err := manager.Service(ctx, "soar", action); err != nil {
			t.Fatalf("service %s: %v", action, err)
		}
		if action == "stop" {
			if err := manager.Runner.Run(ctx, "systemctl", []string{"is-active", "--quiet", spec.Service}, "/", false); err == nil {
				t.Fatal("stopped service is still active")
			}
		} else {
			probe(changed.Domain)
		}
	}
	if err := manager.Remove(ctx, "soar", routerPath, false); err != nil {
		t.Fatal("detach: ", err)
	}
	if after, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(seed, after) {
		t.Fatal("detach did not preserve the server identity: ", err)
	}
	if _, err := manager.Configure(ctx, changed, nil); err != nil {
		t.Fatal("reattach: ", err)
	}
	probe(changed.Domain)
	if err := manager.Remove(ctx, "soar", routerPath, true); err != nil {
		t.Fatal("purge: ", err)
	}
	for _, path := range []string{spec.WorkDir, "/etc/systemd/system/soar-server.service"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("purge left %s behind: %v", path, err)
		}
	}
	if err := manager.requireActive(ctx, "cottenrouter", "CottenRouter"); err != nil {
		t.Fatal(err)
	}
	t.Log("configuration, identity persistence, stop/start/restart, detach, reattach, and purge passed")
}

func probeManagedSoarSOA(t *testing.T, zone string) {
	t.Helper()
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[:2], 0x7623)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split(zone, ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 6, 0, 1)
	deadline := time.Now().Add(5 * time.Second)
	var lastError error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("udp", "127.0.0.1:53", time.Second)
		if err != nil {
			lastError = err
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(400 * time.Millisecond))
		_, err = conn.Write(query)
		response := make([]byte, 4096)
		var n int
		if err == nil {
			n, err = conn.Read(response)
		}
		_ = conn.Close()
		if err == nil && n >= 12 && binary.BigEndian.Uint16(response[:2]) == 0x7623 &&
			binary.BigEndian.Uint16(response[2:4])&0x840f == 0x8400 && binary.BigEndian.Uint16(response[6:8]) > 0 {
			return
		}
		lastError = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("live router did not return authoritative Soar SOA for %s: %v", zone, lastError)
}
