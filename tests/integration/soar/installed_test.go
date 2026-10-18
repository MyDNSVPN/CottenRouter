package soarintegration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getlantern/soar"
)

// This opt-in probe uses the installed service, its persisted identity and the
// real shared port 53. It makes one public HTTP HEAD request through the tunnel.
// Run after installing the soar.systemd.test zone in the dedicated distro.
func TestInstalledSoarPublicHTTP(t *testing.T) {
	probeInstalledSoarPublicHTTP(t, "soar")
}

func TestInstalledWhiteDNSSoarPublicHTTP(t *testing.T) {
	probeInstalledSoarPublicHTTP(t, "soar22")
}

func probeInstalledSoarPublicHTTP(t *testing.T, projectID string) {
	t.Helper()
	if os.Getenv("COTTENROUTER_TEST_SYSTEMD_NETWORK") != "1" {
		t.Skip("requires explicitly enabled installed-service network probe")
	}
	if os.Geteuid() != 0 || os.Getenv("WSL_DISTRO_NAME") != "CottenRouter-Test" {
		t.Fatal("requires root in the disposable CottenRouter-Test distribution")
	}
	t.Parallel()
	raw, err := os.ReadFile("/opt/cottenrouter/backends/" + projectID + "/server.key")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatal("invalid installed seed")
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	client, err := soar.NewClient(soar.Config{
		Zone: projectID + ".systemd.test", ServerKey: public,
		Resolvers: []string{"127.0.0.1:53"}, TCP: "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	transport := &http.Transport{DialContext: client.DialContext}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: transport, Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 500 {
		t.Fatalf("unexpected public HTTP status: %s", response.Status)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s installed encrypted tunnel returned %s through shared port 53", projectID, response.Status)
}
