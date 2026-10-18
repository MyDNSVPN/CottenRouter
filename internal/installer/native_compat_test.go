package installer

import (
	"bytes"
	"testing"
)

func TestMasterDnsVPNPersistentServiceDoesNotGenerateKeyAndExit(t *testing.T) {
	spec, _ := FindSpec("masterdnsvpn")
	upstream := []byte("EXECUTABLE_ARGS=\"-genkey -nowait\"\n./\"$EXECUTABLE\" $EXECUTABLE_ARGS\ncat > \"$SVC\" <<EOF\n[Service]\nExecStart=$INSTALL_DIR/$EXECUTABLE $EXECUTABLE_ARGS\nRestart=always\nEOF\n")
	prepared := prepareNativeInstaller(spec, upstream)
	if !bytes.Contains(prepared, []byte("./\"$EXECUTABLE\" $EXECUTABLE_ARGS")) {
		t.Fatal("one-time key initialization was removed")
	}
	if !bytes.Contains(prepared, []byte("ExecStart=$INSTALL_DIR/$EXECUTABLE\n")) {
		t.Fatalf("persistent service still inherits initialization flags:\n%s", prepared)
	}
	if !bytes.Contains(upstream, []byte("ExecStart=$INSTALL_DIR/$EXECUTABLE $EXECUTABLE_ARGS")) {
		t.Fatal("original pinned installer was modified")
	}
	otherSpec, _ := FindSpec("cottendns")
	if !bytes.Equal(prepareNativeInstaller(otherSpec, upstream), upstream) {
		t.Fatal("MasterDnsVPN workaround changed another project's installer")
	}
	updated := bytes.ReplaceAll(upstream, []byte(`EXECUTABLE_ARGS="-genkey -nowait"`), []byte(`EXECUTABLE_ARGS="-some-future-flag"`))
	if !bytes.Equal(prepareNativeInstaller(spec, updated), updated) {
		t.Fatal("workaround removed unrecognized future service arguments")
	}
}
