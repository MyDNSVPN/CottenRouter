package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/TaJirax/CottenRouter/internal/config"
)

func whiteDNSFixtureFiles() map[string][]byte {
	return map[string][]byte{
		"install.sh":                  []byte("SETUP_VERSION=0.1.3\nSOAR_VERSION=v0.2.2\n# must never execute this installer\nexit 97\n"),
		"bin/linux-amd64/soar-server": []byte("verified-server"),
		"bin/linux-amd64/soar-check":  []byte("verified-checker"),
		"SOURCE-VERSIONS.txt":         []byte("Setup: 0.1.3\nSoar: v0.2.2\n"),
	}
}

func whiteDNSFixtureManifest(files map[string][]byte) []byte {
	var result strings.Builder
	for _, name := range []string{"install.sh", "bin/linux-amd64/soar-server", "bin/linux-amd64/soar-check", "SOURCE-VERSIONS.txt"} {
		fmt.Fprintf(&result, "%x  %s\n", sha256.Sum256(files[name]), name)
	}
	return []byte(result.String())
}

func whiteDNSFixtureArchive(t *testing.T, files map[string][]byte, manifest []byte, extra *tar.Header) ([]byte, []byte) {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	prefix := strings.TrimSuffix(whiteDNSSoarAsset, ".tar.gz") + "/"
	add := func(name string, data []byte) {
		t.Helper()
		if err := writer.WriteHeader(&tar.Header{Name: prefix + name, Mode: 0755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		add(name, data)
	}
	add("SHA256SUMS", manifest)
	if extra != nil {
		if err := writer.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()
	checksum := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), whiteDNSSoarAsset))
	return archive, checksum
}

func TestWhiteDNSBundleRequiresArchiveAndEveryRuntimeChecksum(t *testing.T) {
	files := whiteDNSFixtureFiles()
	manifest := whiteDNSFixtureManifest(files)
	archive, checksum := whiteDNSFixtureArchive(t, files, manifest, nil)
	verified, digest, err := verifyWhiteDNSSoarBundle(archive, checksum)
	if err != nil || digest == "" || !bytes.Equal(verified["bin/linux-amd64/soar-server"], files["bin/linux-amd64/soar-server"]) {
		t.Fatalf("valid bundle was not accepted byte-for-byte: %v", err)
	}
	for _, bad := range [][]byte{nil, []byte("broken"), bytes.Replace(checksum, []byte(whiteDNSSoarAsset), []byte("other.tar.gz"), 1), append(append([]byte{}, checksum...), checksum...)} {
		if _, _, err := verifyWhiteDNSSoarBundle(archive, bad); err == nil {
			t.Fatal("accepted malformed external checksum")
		}
	}
	for name, data := range files {
		t.Run(name, func(t *testing.T) {
			files[name] = append(append([]byte{}, data...), 'x')
			archive, checksum := whiteDNSFixtureArchive(t, files, manifest, nil)
			files[name] = data
			if _, _, err := verifyWhiteDNSSoarBundle(archive, checksum); err == nil {
				t.Fatal("accepted corrupted runtime file")
			}
		})
	}
	for _, bad := range [][]byte{nil, []byte("not a manifest"), append(append([]byte{}, manifest...), manifest...), []byte(strings.ReplaceAll(string(manifest), "install.sh", "unexpected.sh"))} {
		archive, checksum := whiteDNSFixtureArchive(t, files, bad, nil)
		if _, _, err := verifyWhiteDNSSoarBundle(archive, checksum); err == nil {
			t.Fatal("accepted incomplete, duplicate or foreign manifest")
		}
	}
}

func TestWhiteDNSBundleRejectsUnsafeArchiveEntries(t *testing.T) {
	prefix := strings.TrimSuffix(whiteDNSSoarAsset, ".tar.gz") + "/"
	for _, header := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "/etc/escape", Typeflag: tar.TypeReg},
		{Name: prefix + "../escape", Typeflag: tar.TypeReg},
		{Name: prefix + "./alias", Typeflag: tar.TypeReg},
		{Name: prefix + `bin\escape`, Typeflag: tar.TypeReg},
		{Name: prefix + "C:escape", Typeflag: tar.TypeReg},
		{Name: prefix + "install.sh", Typeflag: tar.TypeReg},
		{Name: prefix + "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: prefix + "hardlink", Typeflag: tar.TypeLink, Linkname: "install.sh"},
		{Name: prefix + "fifo", Typeflag: tar.TypeFifo},
	} {
		t.Run(header.Name, func(t *testing.T) {
			files := whiteDNSFixtureFiles()
			archive, checksum := whiteDNSFixtureArchive(t, files, whiteDNSFixtureManifest(files), &header)
			if _, _, err := verifyWhiteDNSSoarBundle(archive, checksum); err == nil {
				t.Fatal("accepted unsafe archive entry")
			}
		})
	}
	files := whiteDNSFixtureFiles()
	archive, _ := whiteDNSFixtureArchive(t, files, whiteDNSFixtureManifest(files), nil)
	archive = archive[:len(archive)-8]
	checksum := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), whiteDNSSoarAsset))
	if _, _, err := verifyWhiteDNSSoarBundle(archive, checksum); err == nil {
		t.Fatal("accepted truncated gzip stream")
	}
}

func TestWhiteDNSOlderVersionCannotSilentlyChange(t *testing.T) {
	for _, script := range []string{"", "SETUP_RELEASE=$(whoami)\n", "SETUP_RELEASE=0.1.3\nSETUP_RELEASE=0.1.3\n", "SETUP_RELEASE=0.2.0\n"} {
		if requireWhiteDNSSetting([]byte(script), "SETUP_RELEASE", whiteDNSSoarSetupVersion) == nil {
			t.Fatalf("accepted unsafe release declaration %q", script)
		}
	}
	for _, versions := range []string{"Setup: 0.1.3\nSoar: v0.2.3\n", "Setup: 0.1.3\nSoar: v0.2.2\nSoar: v0.2.2\n", "Setup: 0.1.3\nSoar: v0.2.2\nSoar: v0.2.3\n"} {
		files := whiteDNSFixtureFiles()
		files["SOURCE-VERSIONS.txt"] = []byte(versions)
		archive, checksum := whiteDNSFixtureArchive(t, files, whiteDNSFixtureManifest(files), nil)
		if _, _, err := verifyWhiteDNSSoarBundle(archive, checksum); err == nil {
			t.Fatal("accepted wrong or ambiguous engine version")
		}
	}
}

func TestSoarDistributionsHaveIndependentIdentityUnitsAndRoutes(t *testing.T) {
	original, _ := FindSpec("soar")
	legacy, _ := FindSpec("soar22")
	if original.Service == legacy.Service || original.WorkDir == legacy.WorkDir || original.ConfigPath == legacy.ConfigPath || original.DefaultPort == legacy.DefaultPort || soarExecutableName(original) == soarExecutableName(legacy) {
		t.Fatal("Soar distributions share managed resources")
	}
	if validateSoarUnitOwnership(original, []byte(soarUnit(legacy))) == nil || validateSoarUnitOwnership(legacy, []byte(soarUnit(original))) == nil {
		t.Fatal("one distribution can adopt the other's unit")
	}
	path := t.TempDir() + "/router.json"
	for _, spec := range []Spec{original, legacy} {
		request := Request{ProjectID: spec.ID, Domain: spec.ID + ".example", PrivatePort: spec.DefaultPort, RouterConfig: path}
		if _, err := request.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := updateRouterConfig(path, spec, request, PortPlan{DNSPort: request.PrivatePort}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil || len(cfg.Routes) != 2 {
		t.Fatalf("both routes were not preserved: %v", err)
	}
	if err := removeProjectConfig(path, legacy); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil || len(cfg.Routes) != 1 || cfg.Routes[0].Name != "soar" {
		t.Fatalf("removing legacy affected original: %+v, %v", cfg.Routes, err)
	}
}
