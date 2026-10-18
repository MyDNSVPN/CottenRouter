package installer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/version"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/TaJirax/CottenRouter/internal/catalog"
	"github.com/TaJirax/CottenRouter/internal/dnswire"
)

// Both Soar distributions keep their complete managed state in separate
// backend directories; each one's only external artifact is its own unit.
func (m Manager) installSoar(ctx context.Context, spec Spec, request Request, plan PortPlan, project catalog.Project, progress Progress) (result error) {
	unitPath := filepath.Join("/etc/systemd/system", spec.Service+".service")
	for _, name := range []string{spec.WorkDir, spec.ConfigPath, request.RouterConfig, unitPath} {
		if err := rejectSymlinkComponents(name); err != nil {
			return err
		}
	}
	if err := requireSoarUnitOwnership(spec); err != nil {
		return err
	}
	previousConfig, err := os.ReadFile(spec.ConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	configured, err := configure(spec, request, plan, previousConfig)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(spec.WorkDir, 0750); err != nil {
		return err
	}
	// Build on the backend filesystem, away from small /tmp tmpfs mounts, and
	// before restarting anything. A failed download leaves live services alone.
	buildDir, err := os.MkdirTemp(spec.WorkDir, ".build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)
	runtime, err := m.prepareSoarRuntime(ctx, spec, project, buildDir, progress)
	if err != nil {
		return err
	}
	binary := runtime.binary
	if err := ensureSoarKey(filepath.Join(spec.WorkDir, "server.key")); err != nil {
		return err
	}

	type snapshot struct {
		path   string
		data   []byte
		mode   os.FileMode
		exists bool
	}
	var snapshots []snapshot
	paths := []string{spec.ConfigPath, request.RouterConfig, unitPath, filepath.Join(spec.WorkDir, soarExecutableName(spec)), filepath.Join(spec.WorkDir, "upstream.json"), filepath.Join("/etc/systemd/system", spec.Service+".service.d", "cottenrouter.conf"), "/etc/systemd/system/cottenrouter-backends.slice"}
	for name := range runtime.files {
		paths = append(paths, filepath.Join(spec.WorkDir, name))
	}
	for _, name := range paths {
		if err := rejectSymlinkComponents(name); err != nil {
			return err
		}
		item := snapshot{path: name, mode: 0600}
		info, statErr := os.Stat(name)
		if statErr == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("managed Soar path %s is not a regular file", name)
			}
			item.exists, item.mode = true, info.Mode().Perm()
			item.data, err = os.ReadFile(name)
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		snapshots = append(snapshots, item)
	}
	state := snapshotManagedServiceState(ctx, m.Runner, spec.Service)
	mutated := false
	defer func() {
		if result == nil || !mutated {
			return
		}
		// Stop the newly started process while its unit is still installed.
		// Otherwise a first-install failure can leave an orphan running after
		// the new unit and executable are removed from disk.
		_ = m.Runner.Run(context.Background(), "systemctl", []string{"disable", "--now", spec.Service}, "/", false)
		for _, item := range snapshots {
			if item.exists {
				result = errors.Join(result, atomicWrite(item.path, item.data, item.mode))
			} else if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
				result = errors.Join(result, err)
			}
		}
		_ = m.Runner.Run(context.Background(), "systemctl", []string{"daemon-reload"}, "/", false)
		restoreManagedServicesExact([]managedServiceState{state}, m.Runner)
		_ = m.Runner.Run(context.Background(), "systemctl", []string{"restart", "cottenrouter"}, "/", false)
	}()
	mutated = true
	if err := atomicWrite(filepath.Join(spec.WorkDir, soarExecutableName(spec)), binary, 0755); err != nil {
		return err
	}
	for name, data := range runtime.files {
		if err := atomicWrite(filepath.Join(spec.WorkDir, name), data, 0600); err != nil {
			return err
		}
	}
	if err := atomicWrite(spec.ConfigPath, configured, 0600); err != nil {
		return err
	}
	if err := atomicWrite(unitPath, []byte(soarUnit(spec)), 0644); err != nil {
		return err
	}
	if err := updateRouterConfig(request.RouterConfig, spec, request, plan); err != nil {
		return err
	}
	if err := m.installContainment(ctx, spec); err != nil {
		return err
	}
	progress("Starting " + spec.Name + " on its private UDP listener and verifying CottenRouter")
	for _, args := range [][]string{{"enable", spec.Service}, {"restart", spec.Service}} {
		if err := m.Runner.Run(ctx, "systemctl", args, "/", false); err != nil {
			return err
		}
	}
	if err := m.requireActive(ctx, spec.Service, spec.Name); err != nil {
		return err
	}
	if err := m.waitForPrivateListeners(ctx, spec, request, plan); err != nil {
		return err
	}
	if err := m.waitForRouterPortsReleased(ctx, request.RouterConfig); err != nil {
		return err
	}
	if err := m.Runner.Run(ctx, "systemctl", []string{"restart", "cottenrouter"}, "/", false); err != nil {
		return err
	}
	if err := m.requireActive(ctx, "cottenrouter", "CottenRouter"); err != nil {
		return err
	}
	if err := m.waitForRouterListeners(ctx, request.RouterConfig); err != nil {
		return err
	}
	for _, target := range publicPorts(request, plan) {
		if err := openFirewall(ctx, m.Runner, target); err != nil {
			return err
		}
	}
	digest := sha256.Sum256(binary)
	record, err := json.MarshalIndent(struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit_sha"`
		SHA256     string `json:"binary_sha256"`
		Engine     string `json:"engine_version,omitempty"`
		Setup      string `json:"setup_version,omitempty"`
		Archive    string `json:"archive_sha256,omitempty"`
	}{project.Repository, project.CommitSHA, hex.EncodeToString(digest[:]), runtime.engine, runtime.setup, runtime.archiveSHA256}, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(spec.WorkDir, "upstream.json"), append(record, '\n'), 0640); err != nil {
		return err
	}
	progress(spec.Name + " installed; use cottenrouter keys --project " + spec.ID + " for the client public key")
	return nil
}

func soarExecutableName(spec Spec) string {
	if spec.ID == "soar22" {
		return "soar22-server"
	}
	return "soar-server"
}

func soarDescription(spec Spec) string {
	if spec.ID == "soar22" {
		return "Soar 2.2 (WhiteDNS, older engine v0.2.2) managed by CottenRouter"
	}
	return "Soar DNS tunnel managed by CottenRouter"
}

func soarUnit(spec Spec) string {
	return fmt.Sprintf(`[Unit]
Description=%s
Wants=network-online.target
After=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=root
WorkingDirectory=%s
EnvironmentFile=%s
ExecStart=%s/%s -zone ${SOAR_ZONE} -listen ${SOAR_LISTEN} -key-file %s/server.key -allow-ports ${SOAR_ALLOW_PORTS}
Restart=on-failure
RestartSec=2
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
# Soar's public-only destination filter enumerates local interface addresses.
# Linux performs that lookup over netlink; denying it blocks every TCP dial.
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
`, soarDescription(spec), spec.WorkDir, spec.ConfigPath, spec.WorkDir, soarExecutableName(spec), spec.WorkDir)
}

func validateSoarUnitOwnership(spec Spec, unit []byte) error {
	// WhiteDNS's standalone installer uses the same service name with its own
	// configuration and identity. Replacing that unit with a newly generated
	// seed would silently disconnect its existing clients.
	settings := map[string]string{}
	for _, line := range strings.Split(string(unit), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			settings[key] = value
		}
	}
	if settings["Description"] != soarDescription(spec) ||
		settings["EnvironmentFile"] != spec.ConfigPath ||
		!strings.HasPrefix(settings["ExecStart"], spec.WorkDir+"/"+soarExecutableName(spec)+" ") {
		return fmt.Errorf("existing %s.service belongs to another Soar installation; migrate its configuration and key before installing with CottenRouter", spec.Service)
	}
	return nil
}

func validateSoarConfig(data []byte) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok || (key != "SOAR_ZONE" && key != "SOAR_LISTEN" && key != "SOAR_ALLOW_PORTS") || seen[key] {
			return fmt.Errorf("Soar config must contain each SOAR_ZONE, SOAR_LISTEN, and SOAR_ALLOW_PORTS setting once")
		}
		seen[key] = true
	}
	zone := envValue(data, "SOAR_ZONE")
	if normalized, err := dnswire.NormalizeDomain(zone); err != nil || normalized != zone {
		return fmt.Errorf("SOAR_ZONE must be a normalized delegated DNS zone")
	}
	if err := validateSoarZone(zone); err != nil {
		return err
	}
	host, portText, err := net.SplitHostPort(envValue(data, "SOAR_LISTEN"))
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || host != "127.0.0.1" || port < 1024 || port > 65535 {
		return fmt.Errorf("SOAR_LISTEN must be 127.0.0.1:<private port between 1024 and 65535>")
	}
	ports := envValue(data, "SOAR_ALLOW_PORTS")
	if ports == "any" {
		return nil
	}
	for _, value := range strings.Split(ports, ",") {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != value {
			return fmt.Errorf("SOAR_ALLOW_PORTS must be comma-separated destination ports or any")
		}
	}
	return nil
}

func validateSoarZone(zone string) error {
	// A hello is 34 bytes: 55 base32 characters, then a dot and the zone.
	if len(zone) > 197 {
		return fmt.Errorf("Soar zone is too long to carry its handshake (maximum 197 characters)")
	}
	for _, ch := range []byte(zone) {
		if ch < '!' || ch > '~' || ch == '\\' || ch == '"' || ch == '\'' || ch == '$' || ch == '`' || ch == '%' {
			return fmt.Errorf("Soar zone must use printable ASCII DNS labels without shell or systemd metacharacters")
		}
	}
	return nil
}

func readSoarSeed(path string) ([]byte, error) {
	if err := rejectSymlinkComponents(path); err != nil {
		return nil, err
	}
	raw, err := readSmallFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("Soar key must contain a base64 32-byte Ed25519 seed")
	}
	return seed, nil
}

func ensureSoarKey(path string) error {
	if _, err := readSoarSeed(path); err == nil {
		return os.Chmod(path, 0600)
	} else if !os.IsNotExist(err) {
		return err // Never silently replace a malformed or unreadable identity.
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	return atomicWrite(path, []byte(base64.StdEncoding.EncodeToString(private.Seed())+"\n"), 0600)
}

func soarCredentials(spec Spec, reveal bool) ([]Credential, error) {
	path := filepath.Join(spec.WorkDir, "server.key")
	seed, err := readSoarSeed(path)
	if err != nil {
		return nil, err
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	credentials := []Credential{{Label: "Soar client public key", Path: path, Value: base64.StdEncoding.EncodeToString(public)}}
	if reveal {
		credentials = append(credentials, Credential{Label: "Soar server private seed", Path: path, Value: base64.StdEncoding.EncodeToString(seed), Sensitive: true})
	}
	return credentials, nil
}

func (m Manager) buildSoar(ctx context.Context, project catalog.Project, buildDir string, progress Progress) ([]byte, error) {
	if !regexp.MustCompile(`^[a-f0-9]{40,64}$`).MatchString(project.CommitSHA) || project.RepoFullName != "getlantern/soar" {
		return nil, fmt.Errorf("Soar source build requires a verified upstream commit")
	}
	goBinary, err := m.soarGoToolchain(ctx, buildDir, progress)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"bin", "tmp", "cache", "mod", "gopath"} {
		if err := os.MkdirAll(filepath.Join(buildDir, dir), 0700); err != nil {
			return nil, err
		}
	}
	// This cache belongs only to the temporary build. Keep its module
	// directories writable so unprivileged validation can remove it too.
	args := []string{"GOBIN=" + filepath.Join(buildDir, "bin"), "GOTMPDIR=" + filepath.Join(buildDir, "tmp"), "GOCACHE=" + filepath.Join(buildDir, "cache"), "GOMODCACHE=" + filepath.Join(buildDir, "mod"), "GOPATH=" + filepath.Join(buildDir, "gopath"), "GOTOOLCHAIN=auto", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=", "GOPROXY=https://proxy.golang.org|direct", "GOSUMDB=sum.golang.org", goBinary, "install", "-modcacherw", "-trimpath", "github.com/getlantern/soar/cmd/soar-server@" + project.CommitSHA}
	if err := m.Runner.Run(ctx, "env", args, buildDir, false); err != nil {
		return nil, fmt.Errorf("build Soar: %w", err)
	}
	binaryPath := filepath.Join(buildDir, "bin", "soar-server")
	if _, err := m.Runner.Output(ctx, binaryPath, "-h"); err != nil {
		return nil, fmt.Errorf("built Soar binary cannot run: %w", err)
	}
	return os.ReadFile(binaryPath)
}

// The bootstrap's temporary Go toolchain may already have been removed. Keep
// Soar install independent of distro Go versions, without changing host PATH.
func (m Manager) soarGoToolchain(ctx context.Context, buildDir string, progress Progress) (string, error) {
	if binary, err := exec.LookPath("go"); err == nil {
		if output, err := m.Runner.Output(ctx, binary, "env", "GOVERSION"); err == nil && version.Compare(strings.TrimSpace(string(output)), "go1.25.0") >= 0 {
			return binary, nil
		}
	}
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv6l"
	}
	checksums := map[string]string{
		"amd64":   "2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613",
		"arm64":   "05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae",
		"armv6l":  "a5a8f8198fcf00e1e485b8ecef9ee020778bf32a408a4e8873371bfce458cd09",
		"ppc64le": "0f18a89e7576cf2c5fa0b487a1635d9bcbf843df5f110e9982c64df52a983ad0",
		"riscv64": "c018ff74a2c48d55c8ca9b07c8e24163558ffec8bea08b326d6336905d956b67",
		"s390x":   "34e5a2e19f2292fbaf8783e3a241e6e49689276aef6510a8060ea5ef54eee408",
	}
	expected, ok := checksums[arch]
	if !ok {
		return "", fmt.Errorf("install Go >=1.25 on this architecture (%s) before installing Soar", arch)
	}
	progress("Downloading a checksum-verified temporary Go toolchain")
	filename := "go1.25.0.linux-" + arch + ".tar.gz"
	var failures error
	for index, base := range []string{"https://go.dev/dl/", "https://mirrors.aliyun.com/golang/"} {
		archive := filepath.Join(buildDir, fmt.Sprintf("go-%d.tar.gz", index))
		if err := m.downloadSoarToolchain(ctx, base+filename, archive, expected); err != nil {
			failures = errors.Join(failures, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if err := m.Runner.Run(ctx, "tar", []string{"-xzf", archive, "-C", buildDir}, buildDir, false); err != nil {
			return "", err
		}
		return filepath.Join(buildDir, "go", "bin", "go"), nil
	}
	return "", fmt.Errorf("download verified Go toolchain: %w", failures)
}

// A mirror is only a transport fallback: every archive must match the same
// official SHA-256 before any file is extracted or a compiler is executed.
func (m Manager) downloadSoarToolchain(ctx context.Context, url, archive, expected string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	if m.Client != nil {
		client.Transport, client.CheckRedirect = m.Client.Transport, m.Client.CheckRedirect
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, (256<<20)+1))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if n > 256<<20 || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("Go toolchain archive from %s failed SHA-256 verification", url)
	}
	return nil
}
