//go:build linux

package installer

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/TaJirax/CottenRouter/internal/catalog"
	"github.com/TaJirax/CottenRouter/internal/dnswire"
)

// This opt-in test runs the native installer's actual source-build pipeline.
// Only module/toolchain downloads use the network; the running server and DNS
// probe use loopback, and no systemd unit or host configuration is changed.
func TestSoarPinnedSourceBuildLive(t *testing.T) {
	if os.Getenv("COTTENROUTER_TEST_SOURCE_BUILD") != "1" {
		t.Skip("set COTTENROUTER_TEST_SOURCE_BUILD=1 to build and run the pinned upstream CLI")
	}
	const commit = "a8af533d82284f5e6ab13b706552596c108bacf2"
	manager := Manager{Runner: OSRunner{}, Client: &http.Client{Timeout: 30 * time.Second}}
	buildCtx, stopBuild := context.WithTimeout(context.Background(), 4*time.Minute)
	defer stopBuild()
	binaryData, err := manager.buildSoar(buildCtx, catalog.Project{
		ID: "soar", RepoFullName: "getlantern/soar",
		Repository: "https://github.com/getlantern/soar", CommitSHA: commit,
	}, t.TempDir(), func(message string) { t.Log(message) })
	if err != nil {
		t.Fatalf("native Soar source build failed: %v", err)
	}
	probeSoarCLI(t, buildCtx, binaryData, "soar", "official source at "+commit)
}

func TestWhiteDNSSoarBundleLive(t *testing.T) {
	if os.Getenv("COTTENROUTER_TEST_WHITEDNS_BUNDLE") != "1" {
		t.Skip("set COTTENROUTER_TEST_WHITEDNS_BUNDLE=1 to download and run the verified WhiteDNS runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	project, err := catalog.DefaultResolver().LatestProject(ctx, "soar22")
	if err != nil {
		t.Fatal(err)
	}
	manager := DefaultManager()
	distribution, err := manager.prepareWhiteDNSSoar(ctx, project, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if distribution.engine != "v0.2.2" || distribution.setup != "0.1.3" || distribution.archiveSHA256 == "" || len(distribution.files["whitedns-bundle.tar.gz"]) == 0 {
		t.Fatal("WhiteDNS runtime lost its version or original bundle provenance")
	}
	probeSoarCLI(t, ctx, distribution.binary, "soar22", "WhiteDNS setup 0.1.3 / older engine v0.2.2")
}

func probeSoarCLI(t *testing.T, buildCtx context.Context, binaryData []byte, projectID, source string) {
	t.Helper()
	spec, _ := FindSpec(projectID)
	spec.WorkDir = t.TempDir()
	spec.ConfigPath = filepath.Join(spec.WorkDir, "soar.env")
	binaryPath := filepath.Join(spec.WorkDir, soarExecutableName(spec))
	if err := os.WriteFile(binaryPath, binaryData, 0755); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(spec.WorkDir, "server.key")
	if err := ensureSoarKey(keyPath); err != nil {
		t.Fatal(err)
	}
	credentials, err := soarCredentials(spec, false)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("installer could not derive client credentials: %v", err)
	}
	publicKey, err := exec.CommandContext(buildCtx, binaryPath, "pubkey", keyPath).CombinedOutput()
	if err != nil {
		t.Fatalf("upstream rejected the installer's private seed: %v: %s", err, publicKey)
	}
	if strings.TrimSpace(string(publicKey)) != credentials[0].Value {
		t.Fatal("installer public key differs from the actual upstream CLI's public key")
	}

	reservation, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := reservation.LocalAddr().String()
	_ = reservation.Close()
	_, portText, _ := net.SplitHostPort(listen)
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	zone := "native.soar.integration.test"
	if projectID == "soar22" {
		// The standalone wizard limits zones for its profile UI. The packaged
		// server shares Lantern's 197-character handshake limit; exercise that
		// boundary because CottenRouter uses its own configuration surface.
		zone = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + ".tests"
	}
	configured, err := configure(spec, Request{ProjectID: projectID, Domain: zone, PrivatePort: port}, PortPlan{DNSPort: port}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.ConfigPath, configured, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(spec.WorkDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	smokeCtx, stopSmoke := context.WithTimeout(context.Background(), 20*time.Second)
	command := exec.CommandContext(smokeCtx, binaryPath,
		"-zone", envValue(configured, "SOAR_ZONE"),
		"-listen", envValue(configured, "SOAR_LISTEN"),
		"-key-file", keyPath,
		"-allow-ports", envValue(configured, "SOAR_ALLOW_PORTS"))
	command.Dir = spec.WorkDir
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		stopSmoke()
		t.Fatal(err)
	}
	processDone := make(chan struct{})
	var processErr error
	go func() {
		processErr = command.Wait()
		close(processDone)
	}()
	t.Cleanup(func() {
		defer stopSmoke()
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-processDone
		}
	})
	client, err := net.Dial("udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	query := make([]byte, 12)
	binary.BigEndian.PutUint16(query[:2], 0x6042)
	binary.BigEndian.PutUint16(query[2:4], 0x0100)
	binary.BigEndian.PutUint16(query[4:6], 1)
	for _, label := range strings.Split(zone, ".") {
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 6, 0, 1) // root, SOA, IN
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-processDone:
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("upstream CLI exited before answering DNS: %v\n%s", processErr, logs)
		default:
		}
		_ = client.SetDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := client.Write(query); err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		response := make([]byte, 4096)
		n, err := client.Read(response)
		if err != nil {
			lastErr = err
			time.Sleep(25 * time.Millisecond)
			continue
		}
		question, err := dnswire.ParseReplyQuestion(response[:n])
		if err != nil {
			t.Fatalf("upstream CLI returned malformed DNS: %v", err)
		}
		if binary.BigEndian.Uint16(response[:2]) != 0x6042 ||
			binary.BigEndian.Uint16(response[2:4])&0x840f != 0x8400 ||
			binary.BigEndian.Uint16(response[6:8]) == 0 ||
			question.Name != zone || question.Type != 6 || question.Class != 1 {
			t.Fatalf("upstream CLI did not return the requested authoritative SOA: %x", response[:n])
		}
		t.Logf("%s passed key compatibility and private UDP SOA smoke (%d-byte executable)", source, len(binaryData))
		return
	}
	logs, _ := os.ReadFile(logPath)
	t.Fatal(fmt.Errorf("upstream CLI did not answer its private UDP listener: %w\n%s", lastErr, logs))
}
