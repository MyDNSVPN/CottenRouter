//go:build linux

package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TaJirax/CottenRouter/internal/config"
)

// This test deliberately changes real service paths, and therefore runs only
// in the explicitly opted-in, disposable test distribution. It must not run
// in parallel with another live installation test.
func TestSoarSystemdCoexistence(t *testing.T) {
	if os.Getenv("COTTENROUTER_TEST_SYSTEMD") != "1" {
		t.Skip("set COTTENROUTER_TEST_SYSTEMD=1 in the disposable CottenRouter-Test WSL2 distribution")
	}
	if os.Geteuid() != 0 || os.Getenv("WSL_DISTRO_NAME") != "CottenRouter-Test" {
		t.Fatal("live coexistence test requires root in the dedicated CottenRouter-Test distribution")
	}
	if name, err := os.ReadFile("/proc/1/comm"); err != nil || strings.TrimSpace(string(name)) != "systemd" {
		t.Fatal("live coexistence test requires systemd as PID 1")
	}
	official, ok := FindSpec("soar")
	if !ok {
		t.Fatal("official Soar spec is missing")
	}
	legacy, ok := FindSpec("soar22")
	if !ok {
		t.Fatal("Soar 2.2 spec is missing")
	}
	for _, spec := range []Spec{official, legacy} {
		for _, path := range []string{spec.WorkDir, filepath.Join("/etc/systemd/system", spec.Service+".service")} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("live coexistence test requires no pre-existing %s: %v", path, err)
			}
		}
	}
	const routerPath = "/etc/cottenrouter/config.json"
	previousRouter, err := os.ReadFile(routerPath)
	if err != nil {
		t.Fatal("bootstrap the router before the live coexistence test: ", err)
	}
	bootstrap, err := config.Load(routerPath)
	if err != nil || len(bootstrap.Routes) != 1 || bootstrap.Routes[0].Name != "bootstrap-placeholder" || len(bootstrap.TLSListeners) != 0 {
		t.Fatalf("live coexistence test requires a fresh bootstrap router configuration: %v", err)
	}
	manager := DefaultManager()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if err := manager.requireActive(ctx, "cottenrouter", "CottenRouter"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stopCleanup()
		for _, spec := range []Spec{legacy, official} {
			_, directoryErr := os.Lstat(spec.WorkDir)
			_, unitErr := os.Lstat(filepath.Join("/etc/systemd/system", spec.Service+".service"))
			if !os.IsNotExist(directoryErr) || !os.IsNotExist(unitErr) {
				if err := manager.Remove(cleanupCtx, spec.ID, routerPath, true); err != nil {
					t.Errorf("cleanup %s: %v", spec.ID, err)
					_ = manager.Runner.Run(cleanupCtx, "systemctl", []string{"disable", "--now", spec.Service}, "/", false)
				}
			}
		}
		if err := atomicWrite(routerPath, previousRouter, 0640); err != nil {
			t.Errorf("restore bootstrap router configuration: %v", err)
		}
		if err := manager.Runner.Run(cleanupCtx, "systemctl", []string{"restart", "cottenrouter"}, "/", false); err != nil {
			t.Errorf("restart restored bootstrap router: %v", err)
		}
	})
	progress := func(message string) { t.Log(message) }
	officialRequest := Request{ProjectID: official.ID, Domain: "soar.systemd.test", PrivatePort: 5305, RouterConfig: routerPath}
	plan, err := manager.Install(ctx, officialRequest, progress)
	if err != nil || plan.DNSPort != 5305 {
		t.Fatalf("install official Soar on 5305: plan=%+v err=%v", plan, err)
	}
	probeManagedSoarSOA(t, officialRequest.Domain)
	officialSeed := readSoarCoexistFile(t, filepath.Join(official.WorkDir, "server.key"))
	officialFiles := snapshotSoarCoexistFiles(t, official)
	officialRoute := requireSoarCoexistRoute(t, routerPath, official.ID, true)
	officialPID, err := manager.Runner.Output(ctx, "systemctl", "show", "--property=MainPID", "--value", official.Service)
	if err != nil || strings.TrimSpace(string(officialPID)) == "0" || strings.TrimSpace(string(officialPID)) == "" {
		t.Fatalf("official Soar has no live process: %v", err)
	}
	assertOfficial := func() {
		t.Helper()
		for path, expected := range officialFiles {
			if digest := sha256.Sum256(readSoarCoexistFile(t, path)); digest != expected {
				t.Fatalf("Soar 2.2 operation changed official Soar artifact %s", path)
			}
		}
		if route := requireSoarCoexistRoute(t, routerPath, official.ID, true); !reflect.DeepEqual(route, officialRoute) {
			t.Fatal("Soar 2.2 operation changed official Soar routing")
		}
		pid, err := manager.Runner.Output(ctx, "systemctl", "show", "--property=MainPID", "--value", official.Service)
		if err != nil || strings.TrimSpace(string(pid)) != strings.TrimSpace(string(officialPID)) {
			t.Fatalf("Soar 2.2 operation stopped or restarted official Soar: %v", err)
		}
		probeManagedSoarSOA(t, officialRequest.Domain)
	}
	legacyRequest := Request{ProjectID: legacy.ID, Domain: "soar22.systemd.test", PrivatePort: 5306, RouterConfig: routerPath}
	plan, err = manager.Install(ctx, legacyRequest, progress)
	if err != nil || plan.DNSPort != 5306 {
		t.Fatalf("install Soar 2.2 on 5306: plan=%+v err=%v", plan, err)
	}
	legacySeed := readSoarCoexistFile(t, filepath.Join(legacy.WorkDir, "server.key"))
	if bytes.Equal(officialSeed, legacySeed) {
		t.Fatal("independent Soar backends share a private identity")
	}
	assertLegacyIdentity := func() {
		t.Helper()
		if !bytes.Equal(legacySeed, readSoarCoexistFile(t, filepath.Join(legacy.WorkDir, "server.key"))) {
			t.Fatal("Soar 2.2 operation replaced its persistent identity")
		}
		credentials, err := Credentials(legacy.ID, false)
		officialCredentials, officialErr := Credentials(official.ID, false)
		if err != nil || officialErr != nil || len(credentials) != 1 || len(officialCredentials) != 1 ||
			credentials[0].Sensitive || credentials[0].Value == officialCredentials[0].Value {
			t.Fatalf("independent public credentials are unavailable or shared: %v, %v", err, officialErr)
		}
	}
	assertLegacyIdentity()
	probeManagedSoarSOA(t, legacyRequest.Domain)
	assertOfficial()
	t.Log("both real systemd runtimes serve independent zones and identities")

	plan, err = manager.Install(ctx, legacyRequest, progress)
	if err != nil || plan.DNSPort != 5306 {
		t.Fatalf("reinstall Soar 2.2: plan=%+v err=%v", plan, err)
	}
	assertLegacyIdentity()
	probeManagedSoarSOA(t, legacyRequest.Domain)
	assertOfficial()
	legacyRequest.Domain, legacyRequest.PrivatePort = "updated.soar22.systemd.test", 5307
	plan, err = manager.Configure(ctx, legacyRequest, progress)
	if err != nil || plan.DNSPort != 5307 {
		t.Fatalf("configure Soar 2.2 on 5307: plan=%+v err=%v", plan, err)
	}
	assertLegacyIdentity()
	probeManagedSoarSOA(t, legacyRequest.Domain)
	assertOfficial()

	beforeFailure := map[string][]byte{
		legacy.ConfigPath: readSoarCoexistFile(t, legacy.ConfigPath),
		routerPath:        readSoarCoexistFile(t, routerPath),
	}
	injected := &failRouterRestartOnce{}
	failingManager := Manager{Runner: injected, Client: manager.Client}
	failedRequest := legacyRequest
	failedRequest.Domain, failedRequest.PrivatePort = "rollback.soar22.systemd.test", 5308
	if _, err := failingManager.Configure(ctx, failedRequest, nil); err == nil || !injected.failed {
		t.Fatalf("legacy router activation failure was not exercised: %v", err)
	}
	for path, expected := range beforeFailure {
		if !bytes.Equal(expected, readSoarCoexistFile(t, path)) {
			t.Fatalf("legacy activation failure did not restore %s", path)
		}
	}
	if err := manager.waitForPrivateListeners(ctx, legacy, legacyRequest, PortPlan{DNSPort: 5307}); err != nil {
		t.Fatal("rollback did not restore the live Soar 2.2 listener: ", err)
	}
	assertLegacyIdentity()
	probeManagedSoarSOA(t, legacyRequest.Domain)
	assertOfficial()
	t.Log("legacy reinstall/configure and failed-activation rollback preserve both runtimes")

	for _, action := range []string{"stop", "start", "restart"} {
		if err := manager.Service(ctx, legacy.ID, action); err != nil {
			t.Fatalf("legacy service %s: %v", action, err)
		}
		if action == "stop" {
			if err := manager.Runner.Run(ctx, "systemctl", []string{"is-active", "--quiet", legacy.Service}, "/", false); err == nil {
				t.Fatal("stopped Soar 2.2 service is still active")
			}
		} else {
			probeManagedSoarSOA(t, legacyRequest.Domain)
		}
		assertLegacyIdentity()
		assertOfficial()
	}
	if err := manager.Remove(ctx, legacy.ID, routerPath, false); err != nil {
		t.Fatal("detach legacy: ", err)
	}
	requireSoarCoexistRoute(t, routerPath, legacy.ID, false)
	assertLegacyIdentity()
	assertOfficial()
	if _, err := manager.Configure(ctx, legacyRequest, progress); err != nil {
		t.Fatal("reattach legacy: ", err)
	}
	assertLegacyIdentity()
	probeManagedSoarSOA(t, legacyRequest.Domain)
	assertOfficial()
	if err := manager.Remove(ctx, legacy.ID, routerPath, true); err != nil {
		t.Fatal("purge legacy: ", err)
	}
	requireSoarCoexistRoute(t, routerPath, legacy.ID, false)
	assertSoarCoexistPurged(t, legacy)
	assertOfficial()
	if err := manager.Remove(ctx, official.ID, routerPath, true); err != nil {
		t.Fatal("purge official: ", err)
	}
	assertSoarCoexistPurged(t, official)
	if err := manager.requireActive(ctx, "cottenrouter", "CottenRouter"); err != nil {
		t.Fatal(err)
	}
	t.Log("independent stop/start/restart, detach/reattach, and purges passed")
}

func readSoarCoexistFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read managed file %s: %v", path, err)
	}
	return data
}

func snapshotSoarCoexistFiles(t *testing.T, spec Spec) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	for _, path := range []string{
		spec.ConfigPath,
		filepath.Join(spec.WorkDir, "server.key"),
		filepath.Join(spec.WorkDir, soarExecutableName(spec)),
		filepath.Join(spec.WorkDir, "upstream.json"),
		filepath.Join("/etc/systemd/system", spec.Service+".service"),
		filepath.Join("/etc/systemd/system", spec.Service+".service.d", "cottenrouter.conf"),
	} {
		files[path] = sha256.Sum256(readSoarCoexistFile(t, path))
	}
	return files
}

func requireSoarCoexistRoute(t *testing.T, path, name string, present bool) config.Route {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range cfg.Routes {
		if route.Name == name {
			if !present {
				t.Fatalf("detached or purged route %s is still present", name)
			}
			return route
		}
	}
	if present {
		t.Fatalf("required route %s is missing", name)
	}
	return config.Route{}
}

func assertSoarCoexistPurged(t *testing.T, spec Spec) {
	t.Helper()
	for _, path := range []string{spec.WorkDir, filepath.Join("/etc/systemd/system", spec.Service+".service"), filepath.Join("/etc/systemd/system", spec.Service+".service.d", "cottenrouter.conf")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("purge left %s behind: %v", path, err)
		}
	}
}
