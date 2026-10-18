package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/TaJirax/CottenRouter/internal/catalog"
)

const (
	whiteDNSSoarSetupVersion  = "0.1.3"
	whiteDNSSoarEngineVersion = "v0.2.2"
	whiteDNSSoarAsset         = "soar-server-setup-" + whiteDNSSoarSetupVersion + "-linux-amd64.tar.gz"
	whiteDNSSoarReleaseURL    = "https://github.com/WhiteDNS/Soar-Server/releases/download/v" + whiteDNSSoarSetupVersion + "/"
)

type soarRuntime struct {
	binary                       []byte
	files                        map[string][]byte
	engine, setup, archiveSHA256 string
}

func (m Manager) prepareSoarRuntime(ctx context.Context, spec Spec, project catalog.Project, staging string, progress Progress) (soarRuntime, error) {
	if spec.ID == "soar22" {
		progress("Installing Soar 2.2 from WhiteDNS's verified bundle (older Lantern engine v0.2.2)")
		return m.prepareWhiteDNSSoar(ctx, project, staging)
	}
	progress("Building Soar from its verified upstream commit (the first build may take several minutes)")
	binary, err := m.buildSoar(ctx, project, staging, progress)
	return soarRuntime{binary: binary}, err
}

func (m Manager) prepareWhiteDNSSoar(ctx context.Context, project catalog.Project, staging string) (soarRuntime, error) {
	var result soarRuntime
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return result, fmt.Errorf("Soar 2.2 uses the WhiteDNS Linux amd64 bundle; this platform is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if project.ID != "soar22" || project.RepoFullName != "WhiteDNS/Soar-Server" ||
		!regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(project.CommitSHA) {
		return result, fmt.Errorf("Soar 2.2 requires a verified WhiteDNS installer commit")
	}
	setup, err := m.download(ctx, project.InstallerURL)
	if err != nil {
		return result, err
	}
	// This option deliberately names the older engine. Never silently change it
	// when the upstream default branch starts pointing to a newer release.
	if err := requireWhiteDNSSetting(setup, "SETUP_RELEASE", whiteDNSSoarSetupVersion); err != nil {
		return result, err
	}
	if err := requireWhiteDNSSetting(setup, "REPOSITORY", "WhiteDNS/Soar-Server"); err != nil {
		return result, err
	}
	archive, err := m.download(ctx, whiteDNSSoarReleaseURL+whiteDNSSoarAsset)
	if err != nil {
		return result, err
	}
	checksum, err := m.download(ctx, whiteDNSSoarReleaseURL+whiteDNSSoarAsset+".sha256")
	if err != nil {
		return result, err
	}
	files, digest, err := verifyWhiteDNSSoarBundle(archive, checksum)
	if err != nil {
		return result, err
	}
	// Run only the verified server's help probe. The standalone wizard assumes
	// ownership of soar-server.service and public UDP/53; CottenRouter instead
	// installs this distribution through its separate transactional lifecycle.
	binary := files["bin/linux-amd64/soar-server"]
	candidate := filepath.Join(staging, "soar22-server")
	if err := os.WriteFile(candidate, binary, 0755); err != nil {
		return result, err
	}
	if _, err := m.Runner.Output(ctx, candidate, "-h"); err != nil {
		return result, fmt.Errorf("WhiteDNS Soar 2.2 binary cannot run: %w", err)
	}
	return soarRuntime{
		binary: binary, engine: whiteDNSSoarEngineVersion, setup: whiteDNSSoarSetupVersion, archiveSHA256: digest,
		files: map[string][]byte{
			// Preserve the original installer, diagnostic tool, documentation and
			// licenses as one immutable bundle, together with its checksum.
			"whitedns-bundle.tar.gz": archive,
			"whitedns-bundle.sha256": checksum,
			"whitedns-setup.sh":      setup,
			"SOURCE-VERSIONS.txt":    files["SOURCE-VERSIONS.txt"],
		},
	}, nil
}

func requireWhiteDNSSetting(script []byte, name, expected string) error {
	count := 0
	for _, line := range strings.Split(string(script), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, name+"=") {
			count++
			if line != name+"="+expected {
				return fmt.Errorf("WhiteDNS %s changed; Soar 2.2 requires %s (older engine %s)", name, expected, whiteDNSSoarEngineVersion)
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("WhiteDNS installer must declare %s exactly once", name)
	}
	return nil
}

// Validate entirely in memory before writing or executing any bundle content.
// In particular, no tar member can create a symlink or escape the managed path.
func verifyWhiteDNSSoarBundle(archive, checksum []byte) (map[string][]byte, string, error) {
	digestBytes := sha256.Sum256(archive)
	digest := hex.EncodeToString(digestBytes[:])
	fields := strings.Fields(string(checksum))
	if len(fields) != 2 || len(fields[0]) != 64 || !strings.EqualFold(fields[0], digest) || strings.TrimPrefix(fields[1], "*") != whiteDNSSoarAsset {
		return nil, "", fmt.Errorf("WhiteDNS bundle failed archive SHA-256 verification")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, "", fmt.Errorf("read WhiteDNS bundle: %w", err)
	}
	defer gz.Close()
	const maxUnpacked = 64 << 20
	limited := &io.LimitedReader{R: gz, N: maxUnpacked + 1}
	reader := tar.NewReader(limited)
	files := map[string][]byte{}
	seen := map[string]bool{}
	prefix := strings.TrimSuffix(whiteDNSSoarAsset, ".tar.gz") + "/"
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("read WhiteDNS archive: %w", err)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if count >= 64 || !strings.HasPrefix(name+"/", prefix) || path.Clean(name) != name || strings.ContainsAny(name, `\:`) || seen[name] {
			return nil, "", fmt.Errorf("unsafe or duplicate WhiteDNS archive member %q", header.Name)
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 16<<20 {
			return nil, "", fmt.Errorf("unsupported WhiteDNS archive member %q", header.Name)
		}
		data, err := io.ReadAll(reader)
		if err != nil || len(data) != int(header.Size) {
			return nil, "", fmt.Errorf("incomplete WhiteDNS archive member %q", header.Name)
		}
		files[strings.TrimPrefix(name, prefix)] = data
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N == 0 {
		return nil, "", fmt.Errorf("WhiteDNS bundle is corrupt or exceeds the unpacked size limit")
	}
	manifest := files["SHA256SUMS"]
	allowed := map[string]bool{"install.sh": true, "bin/linux-amd64/soar-server": true, "bin/linux-amd64/soar-check": true, "SOURCE-VERSIONS.txt": true}
	verified := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, "", fmt.Errorf("invalid WhiteDNS bundle manifest")
		}
		name := strings.TrimPrefix(fields[1], "*")
		data, exists := files[name]
		actual := sha256.Sum256(data)
		if !allowed[name] || !exists || verified[name] || len(data) == 0 || !strings.EqualFold(fields[0], hex.EncodeToString(actual[:])) {
			return nil, "", fmt.Errorf("WhiteDNS bundle checksum failed for %q", name)
		}
		verified[name] = true
	}
	if len(verified) != len(allowed) {
		return nil, "", fmt.Errorf("WhiteDNS bundle manifest must verify all four runtime files")
	}
	for name, value := range map[string]string{"SETUP_VERSION": whiteDNSSoarSetupVersion, "SOAR_VERSION": whiteDNSSoarEngineVersion} {
		if err := requireWhiteDNSSetting(files["install.sh"], name, value); err != nil {
			return nil, "", err
		}
	}
	versions := strings.ReplaceAll(string(files["SOURCE-VERSIONS.txt"]), "\r\n", "\n")
	for prefix, expected := range map[string]string{"Setup:": "Setup: " + whiteDNSSoarSetupVersion, "Soar:": "Soar: " + whiteDNSSoarEngineVersion} {
		count := 0
		for _, line := range strings.Split(versions, "\n") {
			if strings.HasPrefix(line, prefix) {
				count++
				if line != expected {
					return nil, "", fmt.Errorf("unexpected WhiteDNS source version %q", line)
				}
			}
		}
		if count != 1 {
			return nil, "", fmt.Errorf("WhiteDNS bundle must identify setup %s and older engine %s", whiteDNSSoarSetupVersion, whiteDNSSoarEngineVersion)
		}
	}
	return files, digest, nil
}
