package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// requireSoarUnitOwnership protects standalone Soar installations that use the
// same service name. Check the first unit systemd would load, including vendor
// units when no local override exists, before any lifecycle operation changes
// its files or service state.
func requireSoarUnitOwnership(spec Spec) error {
	return requireSoarUnitOwnershipAt(spec, []string{
		"/etc/systemd/system",
		"/run/systemd/system",
		"/usr/lib/systemd/system",
		"/lib/systemd/system",
	})
}

func requireSoarUnitOwnershipAt(spec Spec, directories []string) error {
	if spec.Kind != ConfigSoar {
		return nil
	}
	for _, directory := range directories {
		path := filepath.Join(directory, spec.Service+".service")
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect existing Soar service %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A mask or dangling link still overrides lower-priority units.
			// Never skip it and accidentally adopt a different installation.
			info, err = os.Stat(path)
			if err != nil {
				return fmt.Errorf("resolve existing Soar service %s: %w", path, err)
			}
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("existing Soar service %s is not a regular unit file", path)
		}
		unit, err := readSmallFile(path)
		if err != nil {
			return fmt.Errorf("read existing Soar service %s: %w", path, err)
		}
		if err := validateSoarUnitOwnership(spec, unit); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	return nil
}
