package installer

import "testing"

func TestSoarListenersBelongOnlyToTheirExactRuntime(t *testing.T) {
	official := Spec{ID: "soar", Name: "Soar", Service: "soar-server", Kind: ConfigSoar}
	standalone := Spec{ID: "soar22", Name: "Soar 2.2", Service: "soar22-server", Kind: ConfigSoar}
	for _, test := range []struct {
		process              string
		official, standalone bool
	}{
		{`users:(("soar-server",pid=101,fd=3))`, true, false},
		{`users:(("soar22-server",pid=102,fd=3))`, false, true},
		{`users:(("systemd",pid=1,fd=5),("soar-server",pid=101,fd=3))`, true, false},
		{`users:(("systemd",pid=1,fd=5),("soar22-server",pid=102,fd=3))`, false, true},
		{"soar-server", true, false},
		{"soar22-server", false, true},
		{`users:(("soar-server-old",pid=103,fd=3))`, false, false},
		{`users:(("other-soar-server",pid=104,fd=3))`, false, false},
		{`users:(("soar22-server-old",pid=105,fd=3))`, false, false},
		{`users:(("soar-check",pid=106,fd=3))`, false, false},
		{`users:(("python",pid=107,fd=3)) soar-server`, false, false},
		{"unknown", false, false},
		{"", false, false},
	} {
		t.Run(test.process, func(t *testing.T) {
			listener := Listener{Process: test.process}
			if got := listenerOwnedBySpec(listener, official); got != test.official {
				t.Fatalf("official runtime ownership=%v, want %v", got, test.official)
			}
			if got := listenerOwnedBySpec(listener, standalone); got != test.standalone {
				t.Fatalf("standalone runtime ownership=%v, want %v", got, test.standalone)
			}
		})
	}
}

func TestSoarRuntimeMatchingDoesNotChangeOtherBackendOwnership(t *testing.T) {
	spec, _ := FindSpec("cottendns")
	if !listenerOwnedBySpec(Listener{Process: `users:(("cottendns",pid=42,fd=3))`}, spec) {
		t.Fatal("CottenDNS listener recognition changed")
	}
	if listenerOwnedBySpec(Listener{Process: `users:(("soar-server",pid=42,fd=3))`}, spec) {
		t.Fatal("Soar was assigned to an unrelated project")
	}
}
