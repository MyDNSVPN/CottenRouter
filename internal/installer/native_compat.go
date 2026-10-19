package installer

import "bytes"

// prepareNativeInstaller repairs known upstream setup defects in the execution
// copy. The original download stays intact in the pinned uninstall record.
func prepareNativeInstaller(spec Spec, installer []byte) []byte {
	if spec.ID == "cottendns" {
		// Upstream's mtime sort can pick an old binary left in the directory,
		// even after downloading a newer archive. The caller passes the freshly
		// resolved release tag, so select that exact executable instead.
		return bytes.ReplaceAll(installer,
			[]byte(`EXECUTABLE="$(ls -t ${PREFIX}_v* 2>/dev/null | head -n1 || true)"`),
			[]byte(`EXECUTABLE="${PREFIX}_${TARGET_VERSION}"`))
	}
	if spec.ID != "masterdnsvpn" {
		return installer
	}
	if !bytes.Contains(installer, []byte(`EXECUTABLE_ARGS="-genkey -nowait"`)) {
		return installer
	}
	// MasterDnsVPN uses EXECUTABLE_ARGS="-genkey -nowait" for one-time key
	// initialization, then expands the same variable into its service unit.
	// The server exits immediately with -genkey, so that unit never serves DNS
	// and the native installer's final is-active check fails. Keep key setup,
	// but start the persistent server without the initialization-only flags.
	return bytes.ReplaceAll(installer,
		[]byte("ExecStart=$INSTALL_DIR/$EXECUTABLE $EXECUTABLE_ARGS"),
		[]byte("ExecStart=$INSTALL_DIR/$EXECUTABLE"))
}
