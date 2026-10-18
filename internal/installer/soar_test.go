package installer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TaJirax/CottenRouter/internal/catalog"
	"github.com/TaJirax/CottenRouter/internal/config"
)

func TestSoarRequestRejectsUnsupportedFeatures(t *testing.T) {
	for _, change := range []func(*Request){
		func(r *Request) { r.EnableTCP = true },
		func(r *Request) { r.EnableDoT = true },
		func(r *Request) { r.EnableDoH = true },
		func(r *Request) { r.ExtraDomains = "other.example" },
		func(r *Request) { r.ChatDomains = "chat.example" },
	} {
		request := Request{ProjectID: "soar", Domain: "soar.example"}
		change(&request)
		if _, err := request.Validate(); err == nil {
			t.Fatalf("Soar accepted an unsupported feature: %+v", request)
		}
	}
}

func TestSoarServicePermitsInterfaceEnumerationForPublicOnlyGuard(t *testing.T) {
	spec, _ := FindSpec("soar")
	unit := soarUnit(spec)
	var families string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "RestrictAddressFamilies=") {
			families = strings.TrimPrefix(line, "RestrictAddressFamilies=")
		}
	}
	allowed := map[string]bool{}
	for _, family := range strings.Fields(families) {
		allowed[family] = true
	}
	// Upstream's PublicOnly guard calls net.InterfaceAddrs before permitting
	// an outbound stream. Linux needs netlink for that operation, even when
	// UDP handshakes and the authoritative SOA health check already succeed.
	if !allowed["AF_NETLINK"] || !allowed["AF_INET"] || !allowed["AF_INET6"] {
		t.Fatalf("service sandbox prevents the public-destination guard from working: %s", families)
	}
	if strings.Contains(unit, "-allow-private") {
		t.Fatal("production service bypasses the upstream public-destination guard")
	}
}

func TestSoarInstallDoesNotAdoptWhiteDNSServiceAndReplaceItsIdentity(t *testing.T) {
	spec, _ := FindSpec("soar")
	if err := validateSoarUnitOwnership(spec, []byte(soarUnit(spec))); err != nil {
		t.Fatalf("managed service cannot be upgraded: %v", err)
	}
	foreign := []byte("[Service]\nEnvironmentFile=/etc/soar/server.conf\nExecStart=/usr/local/lib/soar/current/soar-server -key-file /etc/soar/server.key\n")
	if err := validateSoarUnitOwnership(spec, foreign); err == nil || !strings.Contains(err.Error(), "configuration and key") {
		t.Fatalf("installer could silently replace the WhiteDNS identity: %v", err)
	}
}

func TestSoarRoutesAndAdvancedEditsRemainUDPOnly(t *testing.T) {
	spec, _ := FindSpec("soar")
	spec.WorkDir = t.TempDir()
	spec.ConfigPath = filepath.Join(spec.WorkDir, "soar.env")
	routerPath := filepath.Join(spec.WorkDir, "router.json")
	request := Request{ProjectID: "soar", Domain: "soar.example", PrivatePort: 5305, RouterConfig: routerPath}
	if _, err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Routes: []config.Route{{Name: "other", Domains: []string{"other.example"}, Backend: "127.0.0.1:5301"}}}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(routerPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	plan := PortPlan{DNSPort: 5305}
	if err := updateRouterConfig(routerPath, spec, request, plan); err != nil {
		t.Fatal(err)
	}
	data, err := configure(spec, request, plan, []byte("SOAR_ALLOW_PORTS=443,8443\n"))
	if err != nil || envValue(data, "SOAR_ALLOW_PORTS") != "443,8443" {
		t.Fatalf("configuration lost the destination policy: %s, %v", data, err)
	}
	domain, extra, port := configSummary(spec, data)
	if domain != "soar.example" || extra != "" || port != 5305 {
		t.Fatalf("incorrect discovered Soar settings: %q, %q, %d", domain, extra, port)
	}
	data = setEnv(data, "SOAR_ZONE", "new.example")
	data = setEnv(data, "SOAR_LISTEN", "127.0.0.1:5355")
	if err := os.WriteFile(spec.ConfigPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := syncProjectRoute(routerPath, spec); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(routerPath)
	if err != nil || len(updated.Routes) != 2 {
		t.Fatalf("Soar edit removed a neighboring route: %+v, %v", updated.Routes, err)
	}
	route := updated.Routes[1]
	if route.Name != "soar" || route.Backend != "127.0.0.1:5355" || route.TCPBackend != "disabled" || len(route.Domains) != 1 || route.Domains[0] != "new.example" {
		t.Fatalf("Soar route does not match the native server: %+v", route)
	}
	request, plan, err = privateListenerExpectation(spec, spec.ConfigPath)
	if err != nil || request.EnableTCP || plan.DNSPort != 5355 {
		t.Fatalf("listener verification expected nonexistent TCP: %+v, %+v, %v", request, plan, err)
	}
	if err := removeProjectConfig(routerPath, spec); err != nil {
		t.Fatal(err)
	}
	updated, err = config.Load(routerPath)
	if err != nil || len(updated.Routes) != 1 || updated.Routes[0].Name != "other" {
		t.Fatalf("Soar removal affected neighboring route: %+v, %v", updated.Routes, err)
	}
}

func TestSoarPreflightRejectsTimeoutShorterThanHeldPoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.json")
	if err := os.WriteFile(path, []byte(`{"query_timeout_ms":500}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec, _ := FindSpec("soar")
	request := Request{ProjectID: "soar", Domain: "soar.example", PrivatePort: 5305}
	if err := preflightRouterConfig(path, spec, request, PortPlan{DNSPort: 5305}); err == nil || !strings.Contains(err.Error(), "query_timeout_ms >= 2000") {
		t.Fatalf("short timeout could discard held Soar answers: %v", err)
	}
}

func TestSoarIdentityPersistsAndCredentialsMatch(t *testing.T) {
	spec, _ := FindSpec("soar")
	spec.WorkDir = t.TempDir()
	keyPath := filepath.Join(spec.WorkDir, "server.key")
	if err := ensureSoarKey(keyPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSoarKey(keyPath); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("reinstall replaced the client-pinned identity: %v", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(before)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("generated seed is not compatible with Soar: %v", err)
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	credentials, err := soarCredentials(spec, false)
	if err != nil || len(credentials) != 1 || credentials[0].Sensitive || credentials[0].Value != base64.StdEncoding.EncodeToString(public) {
		t.Fatalf("public credentials do not identify this server: %+v, %v", credentials, err)
	}
	credentials, err = soarCredentials(spec, true)
	if err != nil || len(credentials) != 2 || !credentials[1].Sensitive || credentials[1].Value != base64.StdEncoding.EncodeToString(seed) {
		t.Fatalf("explicit secret display lost the server seed: %+v, %v", credentials, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private seed permissions are not 0600: %v, %v", info, err)
		}
	}
}

func TestSoarRejectsMalformedExistingIdentityWithoutReplacingIt(t *testing.T) {
	for _, raw := range []string{"broken", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 64))} {
		keyPath := filepath.Join(t.TempDir(), "server.key")
		if err := os.WriteFile(keyPath, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := ensureSoarKey(keyPath); err == nil {
			t.Fatal("invalid existing identity was accepted")
		}
		got, err := os.ReadFile(keyPath)
		if err != nil || string(got) != raw {
			t.Fatalf("failed key validation changed the existing identity: %q, %v", got, err)
		}
	}
}

func TestSoarConfigRequiresPrivateListenerAndValidDestinationPolicy(t *testing.T) {
	for _, test := range []struct {
		name, zone, listener, allow string
		valid                       bool
	}{
		{"defaults", "dns.example", "127.0.0.1:5305", "80,443", true},
		{"unrestricted destinations", "dns.example", "127.0.0.1:65535", "any", true},
		{"wildcard listener", "dns.example", "0.0.0.0:5305", "443", false},
		{"public listener", "dns.example", "203.0.113.1:5305", "443", false},
		{"privileged listener", "dns.example", "127.0.0.1:53", "443", false},
		{"unnormalized zone", "DNS.Example", "127.0.0.1:5305", "443", false},
		{"missing zone", "", "127.0.0.1:5305", "443", false},
		{"missing policy", "dns.example", "127.0.0.1:5305", "", false},
		{"zero destination", "dns.example", "127.0.0.1:5305", "0", false},
		{"invalid destination", "dns.example", "127.0.0.1:5305", "65536", false},
		{"range destination", "dns.example", "127.0.0.1:5305", "80-443", false},
		{"non-ASCII zone", "tünnel.example", "127.0.0.1:5305", "443", false},
		{"oversize handshake zone", strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + ".example", "127.0.0.1:5305", "443", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := []byte("SOAR_ZONE=" + test.zone + "\nSOAR_LISTEN=" + test.listener + "\nSOAR_ALLOW_PORTS=" + test.allow + "\n")
			if err := validateSoarConfig(data); (err == nil) != test.valid {
				t.Fatalf("validation err=%v, want valid=%v", err, test.valid)
			}
		})
	}
}

func TestSoarConfigRejectsAmbiguousEnvironmentFile(t *testing.T) {
	valid := "SOAR_ZONE=dns.example\nSOAR_LISTEN=127.0.0.1:5305\nSOAR_ALLOW_PORTS=80,443\n"
	for _, suffix := range []string{
		"SOAR_LISTEN=0.0.0.0:5305\n",
		"SOAR_ZONE=other.example\n",
		"SOAR_ALLOW_PORTS=any\n",
		"LD_PRELOAD=/tmp/library.so\n",
		"export SOAR_ZONE=other.example\n",
		"invalid environment line\n",
	} {
		if err := validateSoarConfig([]byte(valid + suffix)); err == nil {
			t.Fatalf("accepted an EnvironmentFile line that can diverge from validation: %q", suffix)
		}
	}
	if err := validateSoarConfig([]byte("# managed settings\n\n" + valid)); err != nil {
		t.Fatalf("ordinary EnvironmentFile comments rejected: %v", err)
	}
}

type soarBuildRunner struct {
	commands     []string
	buildArgs    []string
	buildErr     error
	probeErr     error
	omitBinary   bool
	binaryProbed bool
	goVersion    string
}

func (runner *soarBuildRunner) Run(_ context.Context, name string, args []string, _ string, _ bool) error {
	runner.commands = append(runner.commands, name)
	if name != "env" {
		return errors.New("unexpected host mutation: " + name)
	}
	runner.buildArgs = append([]string(nil), args...)
	if runner.buildErr != nil {
		return runner.buildErr
	}
	if runner.omitBinary {
		return nil
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "GOBIN=") {
			return os.WriteFile(filepath.Join(strings.TrimPrefix(arg, "GOBIN="), "soar-server"), []byte("built-soar-binary"), 0755)
		}
	}
	return errors.New("build has no private output directory")
}

func (runner *soarBuildRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if len(args) == 2 && args[0] == "env" && args[1] == "GOVERSION" {
		if runner.goVersion != "" {
			return []byte(runner.goVersion), nil
		}
		return []byte("go1.25.0\n"), nil
	}
	if len(args) == 1 && args[0] == "-h" && filepath.Base(name) == "soar-server" {
		runner.binaryProbed = true
		return nil, runner.probeErr
	}
	return nil, errors.New("unexpected command output request: " + name)
}

type soarToolchainTransport func(*http.Request) (*http.Response, error)

func (transport soarToolchainTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestSoarToolchainRejectsFailedOrUnverifiedDownloads(t *testing.T) {
	for _, test := range []struct {
		name, wantError string
		status          int
	}{
		{"download failure", "HTTP 503", http.StatusServiceUnavailable},
		{"checksum failure", "SHA-256 verification", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &soarBuildRunner{goVersion: "go1.24.0"}
			var requested []string
			client := &http.Client{Transport: soarToolchainTransport(func(request *http.Request) (*http.Response, error) {
				requested = append(requested, request.URL.Host)
				if (request.URL.Host != "go.dev" && request.URL.Host != "mirrors.aliyun.com") || !strings.Contains(request.URL.Path, "/go1.25.0.linux-") {
					t.Fatalf("unexpected Go download source: %s", request.URL)
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader("untrusted archive")), Header: http.Header{}, Request: request}, nil
			})}
			_, err := (Manager{Runner: runner, Client: client}).soarGoToolchain(context.Background(), t.TempDir(), func(string) {})
			if len(requested) != 2 || requested[0] != "go.dev" || requested[1] != "mirrors.aliyun.com" || err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("toolchain failure was not rejected: %v, requested=%v", err, requested)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("unverified toolchain reached extraction or execution: %v", runner.commands)
			}
		})
	}
}

func soarTestProject() catalog.Project {
	return catalog.Project{ID: "soar", RepoFullName: "getlantern/soar", Repository: "https://github.com/getlantern/soar", CommitSHA: strings.Repeat("a", 40)}
}

func TestSoarBuildUsesPinnedSourceAndPrivateCaches(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go executable is not available in PATH")
	}
	runner := &soarBuildRunner{}
	buildDir := t.TempDir()
	binary, err := (Manager{Runner: runner}).buildSoar(context.Background(), soarTestProject(), buildDir, func(string) {})
	if err != nil || string(binary) != "built-soar-binary" || !runner.binaryProbed {
		t.Fatalf("build did not produce and validate the server: %q, %v", binary, err)
	}
	args := strings.Join(runner.buildArgs, "\n")
	if !strings.Contains(args, "github.com/getlantern/soar/cmd/soar-server@"+soarTestProject().CommitSHA) {
		t.Fatalf("build did not pin its source to the verified commit: %s", args)
	}
	for _, name := range []string{"GOBIN", "GOTMPDIR", "GOCACHE", "GOMODCACHE", "GOPATH"} {
		found := false
		for _, arg := range runner.buildArgs {
			if strings.HasPrefix(arg, name+"=") {
				relative, err := filepath.Rel(buildDir, strings.TrimPrefix(arg, name+"="))
				if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
					t.Fatalf("%s escaped the build directory: %s", name, arg)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("build inherited an uncontrolled %s", name)
		}
	}
}

func TestSoarBuildFailuresStopBeforeInstall(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go executable is not available in PATH")
	}
	for _, test := range []struct {
		name, wantError string
		runner          soarBuildRunner
		wantProbe       bool
	}{
		{"compiler failure", "build Soar", soarBuildRunner{buildErr: errors.New("compiler failed")}, false},
		{"unusable binary", "built Soar binary cannot run", soarBuildRunner{probeErr: errors.New("wrong executable format")}, true},
		{"missing binary", "soar-server", soarBuildRunner{omitBinary: true}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Manager{Runner: &test.runner}).buildSoar(context.Background(), soarTestProject(), t.TempDir(), func(string) {})
			if err == nil || !strings.Contains(err.Error(), test.wantError) || test.runner.binaryProbed != test.wantProbe {
				t.Fatalf("failure was not reported at the correct stage: %v, probed=%v", err, test.runner.binaryProbed)
			}
		})
	}
	runner := &soarBuildRunner{}
	project := soarTestProject()
	project.CommitSHA = "main"
	if _, err := (Manager{Runner: runner}).buildSoar(context.Background(), project, t.TempDir(), func(string) {}); err == nil || len(runner.commands) != 0 {
		t.Fatalf("an unverified source reached the build runner: %v", err)
	}
}

func TestSoarFailedBuildPreservesRunningInstallation(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go executable is not available in PATH")
	}
	spec, _ := FindSpec("soar")
	spec.WorkDir = t.TempDir()
	// This fixture has its own work directory, so it also needs its own unit
	// name. A real managed Soar unit on the test host belongs to a different
	// installation and must not be adopted by this simulated failed build.
	spec.Service = "cottenrouter-test-" + strings.ToLower(filepath.Base(filepath.Dir(spec.WorkDir)))
	spec.ConfigPath = filepath.Join(spec.WorkDir, "soar.env")
	routerPath := filepath.Join(spec.WorkDir, "router.json")
	oldConfig := []byte("SOAR_ZONE=old.example\nSOAR_LISTEN=127.0.0.1:5305\nSOAR_ALLOW_PORTS=443\n")
	if err := os.WriteFile(spec.ConfigPath, oldConfig, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routerPath, []byte("existing-router"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &soarBuildRunner{buildErr: errors.New("network unavailable")}
	request := Request{ProjectID: "soar", Domain: "new.example", PrivatePort: 5305, RouterConfig: routerPath}
	err := (Manager{Runner: runner}).installSoar(context.Background(), spec, request, PortPlan{DNSPort: 5305}, soarTestProject(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "build Soar") {
		t.Fatalf("expected the build failure: %v", err)
	}
	for name, want := range map[string]string{spec.ConfigPath: string(oldConfig), routerPath: "existing-router"} {
		got, err := os.ReadFile(name)
		if err != nil || string(got) != want {
			t.Fatalf("build failure changed %s: %q, %v", name, got, err)
		}
	}
	for _, command := range runner.commands {
		if command != "env" {
			t.Fatalf("build failure mutated a running service: %s", command)
		}
	}
	if _, err := os.Stat(filepath.Join(spec.WorkDir, "server.key")); !os.IsNotExist(err) {
		t.Fatalf("failed build unexpectedly created an identity: %v", err)
	}
}
