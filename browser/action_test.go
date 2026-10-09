package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/linyows/probe/actionref"
)

// TestManifestsDeclare checks that the action.yml a release writes, and the
// one the e2e workflows use, declare the params the action takes, so that
// probe check takes it as it is, and declare no guard, as the action keeps
// to none.
func TestManifestsDeclare(t *testing.T) {
	checksums := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(checksums, []byte(strings.Repeat("a", 64)+"  probe-browser_linux_amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	released, err := exec.Command("sh", "../scripts/action-yml.sh", "v0.0.0", checksums).Output()
	if err != nil {
		t.Fatalf("scripts/action-yml.sh: %v", err)
	}
	local, err := os.ReadFile("../e2e/action/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"scripts/action-yml.sh": released, "e2e/action/action.yml": local} {
		m, err := actionref.ParseManifest(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(m.Params, Params()) {
			t.Errorf("%s declares params %v, want %v", name, m.Params, Params())
		}
		if m.Guard != nil {
			t.Errorf("%s declares guard %v, want none", name, m.Guard)
		}
	}
}
