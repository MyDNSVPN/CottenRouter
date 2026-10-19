package installer

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func cottenCredentialSpec(t *testing.T) Spec {
	t.Helper()
	spec, _ := FindSpec("cottendns")
	root := t.TempDir()
	spec.WorkDir = filepath.Join(root, "backend")
	spec.ConfigPath = filepath.Join(root, "configuration", "server_config.toml")
	for _, directory := range []string{spec.WorkDir, filepath.Dir(spec.ConfigPath)} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return spec
}

func writeCottenCredentialFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCottenMaxPublicCredentialsPreserveSharedKeyRedaction(t *testing.T) {
	for _, configuredPath := range []string{"", "identity/signing.key", "absolute"} {
		t.Run(configuredPath, func(t *testing.T) {
			spec := cottenCredentialSpec(t)
			keySetting := configuredPath
			if configuredPath == "absolute" {
				keySetting = filepath.ToSlash(filepath.Join(t.TempDir(), "signing.key"))
			}
			keyPath := keySetting
			if keyPath == "" {
				keyPath = "cottenmax_key.txt"
			}
			if !filepath.IsAbs(keyPath) {
				keyPath = filepath.Join(filepath.Dir(spec.ConfigPath), keyPath)
			}
			seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
			public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
			publicText := base64.StdEncoding.EncodeToString(public)
			privateText := base64.StdEncoding.EncodeToString(seed)
			writeCottenCredentialFile(t, keyPath, privateText)
			writeCottenCredentialFile(t, keyPath+".pub", publicText+"\n")
			sharedPath := filepath.Join(filepath.Dir(spec.ConfigPath), "secrets", "shared.key")
			writeCottenCredentialFile(t, sharedPath, "legacy-shared-secret\n")
			configuration := "TUNNEL_MODE = \"cottenmax\"\nENCRYPTION_KEY_FILE = \"secrets/shared.key\"\n"
			if keySetting != "" {
				configuration += "COTTENMAX_KEY_FILE = " + strconv.Quote(keySetting) + "\n"
			}
			writeCottenCredentialFile(t, spec.ConfigPath, configuration)
			for _, reveal := range []bool{false, true} {
				credentials, err := tomlProjectCredentials(spec, reveal)
				if err != nil || len(credentials) != 2 {
					t.Fatalf("client credentials unavailable: %v (count=%d)", err, len(credentials))
				}
				shared, maxPublic := credentials[0], credentials[1]
				wantShared := "hidden (use --show-secrets)"
				if reveal {
					wantShared = "legacy-shared-secret"
				}
				if !shared.Sensitive || shared.Value != wantShared || shared.Path != sharedPath {
					t.Fatalf("legacy credential redaction/path changed (reveal=%v)", reveal)
				}
				if maxPublic.Sensitive || maxPublic.Value != publicText || maxPublic.Path != keyPath+".pub" || maxPublic.Label != "CottenMax client public key" {
					t.Fatalf("CottenMax public credential is incorrect (reveal=%v)", reveal)
				}
				for _, item := range credentials {
					if strings.Contains(item.Value, privateText) {
						t.Fatal("CottenMax signing seed was exposed")
					}
				}
			}
		})
	}
}

func TestCottenMaxCredentialsNeverReadOrDeriveFromSigningSeed(t *testing.T) {
	spec := cottenCredentialSpec(t)
	keyPath := filepath.Join(filepath.Dir(spec.ConfigPath), "cottenmax_key.txt")
	// A directory cannot be parsed as a signing seed. A valid public file must
	// remain sufficient, so credentials need no access to the private identity.
	if err := os.Mkdir(keyPath, 0700); err != nil {
		t.Fatal(err)
	}
	public := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x25}, ed25519.PublicKeySize))
	writeCottenCredentialFile(t, keyPath+".pub", public)
	writeCottenCredentialFile(t, spec.ConfigPath, "TUNNEL_MODE = \"cottenmax\"\n")
	credentials, err := tomlProjectCredentials(spec, true)
	if err != nil || len(credentials) != 1 || credentials[0].Value != public || credentials[0].Sensitive {
		t.Fatalf("public-only lookup tried to read the private identity: %v", err)
	}
	if err := os.Remove(keyPath + ".pub"); err != nil {
		t.Fatal(err)
	}
	if credentials, err := tomlProjectCredentials(spec, true); err != nil || len(credentials) != 0 {
		t.Fatalf("shared-credential Max unexpectedly requires a separate public key: %v", err)
	}
}

func TestCottenMaxSharedCredentialNeedsNoSeparateIdentity(t *testing.T) {
	spec := cottenCredentialSpec(t)
	writeCottenCredentialFile(t, spec.ConfigPath, "TUNNEL_MODE = \"cottenmax\"\n")
	writeCottenCredentialFile(t, filepath.Join(spec.WorkDir, "encrypt_key.txt"), "same-cotten-key\n")
	credentials, err := tomlProjectCredentials(spec, false)
	if err != nil || len(credentials) != 1 || !credentials[0].Sensitive || credentials[0].Value != "hidden (use --show-secrets)" {
		t.Fatalf("shared credential was not retained/redacted: %+v %v", credentials, err)
	}
}

func TestCottenMaxCredentialsRejectInvalidPublicKeys(t *testing.T) {
	for _, value := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 64))} {
		spec := cottenCredentialSpec(t)
		publicPath := filepath.Join(filepath.Dir(spec.ConfigPath), "cottenmax_key.txt.pub")
		writeCottenCredentialFile(t, publicPath, value)
		if _, err := cottenMaxPublicCredential(spec, nil); err == nil || !strings.Contains(err.Error(), "base64 32-byte") {
			t.Fatalf("invalid public key was accepted: %v", err)
		}
	}
}

func TestLegacyAndOtherTOMLProjectsDoNotReadCottenMaxKeys(t *testing.T) {
	for _, test := range []struct{ id, mode string }{{"cottendns", "legacy"}, {"cottendns", ""}, {"masterdnsvpn", "cottenmax"}, {"stormdns", "cottenmax"}} {
		spec := cottenCredentialSpec(t)
		spec.ID = test.id
		writeCottenCredentialFile(t, spec.ConfigPath, "TUNNEL_MODE = "+strconv.Quote(test.mode)+"\n")
		writeCottenCredentialFile(t, filepath.Join(spec.WorkDir, "encrypt_key.txt"), "legacy-key\n")
		for _, reveal := range []bool{false, true} {
			credentials, err := tomlProjectCredentials(spec, reveal)
			if err != nil || len(credentials) != 1 || !credentials[0].Sensitive {
				t.Fatalf("legacy credentials changed for %s/%s: %v", test.id, test.mode, err)
			}
			want := "hidden (use --show-secrets)"
			if reveal {
				want = "legacy-key"
			}
			if credentials[0].Value != want {
				t.Fatalf("legacy redaction changed for %s/%s", test.id, test.mode)
			}
		}
	}
}
