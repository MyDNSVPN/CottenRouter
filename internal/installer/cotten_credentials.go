package installer

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func tomlProjectCredentials(spec Spec, reveal bool) ([]Credential, error) {
	data, _ := readSmallFile(spec.ConfigPath)
	keyPath := filepath.Join(spec.WorkDir, "encrypt_key.txt")
	if configured := tomlValue(data, "ENCRYPTION_KEY_FILE"); configured != "" {
		keyPath = configured
		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(filepath.Dir(spec.ConfigPath), keyPath)
		}
	}
	var credentials []Credential
	if _, err := os.Stat(keyPath); err == nil {
		item := Credential{Label: "Shared encryption key", Path: keyPath, Sensitive: true, Value: "hidden (use --show-secrets)"}
		if reveal {
			if raw, err := readSmallFile(keyPath); err == nil {
				item.Value = strings.TrimSpace(string(raw))
			}
		}
		credentials = append(credentials, item)
	}
	if spec.ID == "cottendns" && strings.EqualFold(tomlValue(data, "TUNNEL_MODE"), "cottenmax") {
		public, err := cottenMaxPublicCredential(spec, data)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, public)
	}
	return credentials, nil
}

func cottenMaxPublicCredential(spec Spec, data []byte) (Credential, error) {
	keyPath := tomlValue(data, "COTTENMAX_KEY_FILE")
	if keyPath == "" {
		keyPath = "cottenmax_key.txt"
	}
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(spec.ConfigPath), keyPath)
	}
	publicPath := keyPath + ".pub"
	if err := rejectSymlinkComponents(publicPath); err != nil {
		return Credential{}, fmt.Errorf("CottenMax public key: %w", err)
	}
	info, err := os.Stat(publicPath)
	if err != nil {
		return Credential{}, fmt.Errorf("read CottenMax public key %s (start CottenDNS with TUNNEL_MODE=cottenmax to generate it): %w", publicPath, err)
	}
	if !info.Mode().IsRegular() {
		return Credential{}, fmt.Errorf("CottenMax public key %s must be a regular file", publicPath)
	}
	raw, err := readSmallFile(publicPath)
	if err != nil {
		return Credential{}, fmt.Errorf("read CottenMax public key %s: %w", publicPath, err)
	}
	value := strings.TrimSpace(string(raw))
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Credential{}, fmt.Errorf("CottenMax public key %s must contain a base64 32-byte Ed25519 public key", publicPath)
	}
	// The signing seed is deliberately never opened, even with --show-secrets.
	return Credential{Label: "CottenMax client public key", Path: publicPath, Value: base64.StdEncoding.EncodeToString(key)}, nil
}
