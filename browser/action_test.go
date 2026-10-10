package browser

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/linyows/probe/actionref"
)

// TestManifestsDeclare checks that action.yml, and the one the e2e workflows
// use, declare the params the action takes, so that probe check takes it as
// it is, and declare no guard, as the action keeps to none. probe manifest
// keeps what action.yml declares in the one of a release.
func TestManifestsDeclare(t *testing.T) {
	for _, name := range []string{"action.yml", "e2e/action/action.yml"} {
		data, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
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
