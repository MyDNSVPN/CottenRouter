package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSoarOwnershipRespectsSystemdUnitPrecedence(t *testing.T) {
	for _, test := range []struct {
		name       string
		firstIndex int
		managed    bool
	}{
		{"local managed override", 0, true},
		{"local foreign service", 0, false},
		{"runtime foreign service", 1, false},
		{"usr vendor foreign service", 2, false},
		{"lib vendor foreign service", 3, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, _ := FindSpec("soar")
			base := t.TempDir()
			var directories []string
			for _, name := range []string{"etc", "run", "usr-lib", "lib"} {
				directory := filepath.Join(base, name)
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				directories = append(directories, directory)
			}
			for index := test.firstIndex; index < len(directories); index++ {
				unit := []byte("[Service]\nExecStart=/usr/local/lib/soar/current/soar-server -key-file /etc/soar/server.key\n")
				if index == test.firstIndex && test.managed {
					unit = []byte(soarUnit(spec))
				}
				if err := os.WriteFile(filepath.Join(directories[index], spec.Service+".service"), unit, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := requireSoarUnitOwnershipAt(spec, directories)
			if test.managed && err != nil {
				t.Fatalf("managed higher-priority unit was rejected: %v", err)
			}
			if !test.managed && (err == nil || !strings.Contains(err.Error(), "another Soar installation")) {
				t.Fatalf("foreign unit was not protected: %v", err)
			}
		})
	}
}

func TestSoarOwnershipDoesNotRequireFreshUnitOrAffectOtherProjects(t *testing.T) {
	directory := t.TempDir()
	spec, _ := FindSpec("soar")
	if err := requireSoarUnitOwnershipAt(spec, []string{directory}); err != nil {
		t.Fatalf("fresh install requires an existing unit: %v", err)
	}
	spec, _ = FindSpec("cottendns")
	if err := os.WriteFile(filepath.Join(directory, spec.Service+".service"), []byte("foreign unit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireSoarUnitOwnershipAt(spec, []string{directory}); err != nil {
		t.Fatalf("Soar guard affected another project: %v", err)
	}
}

func TestSoarOwnershipFailsClosedOnInvalidUnitFiles(t *testing.T) {
	spec, _ := FindSpec("soar")
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{"directory", func(path string) error { return os.Mkdir(path, 0700) }},
		{"oversized", func(path string) error { return os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := test.make(filepath.Join(directory, spec.Service+".service")); err != nil {
				t.Fatal(err)
			}
			if err := requireSoarUnitOwnershipAt(spec, []string{directory}); err == nil {
				t.Fatal("invalid unit was accepted")
			}
		})
	}
}
