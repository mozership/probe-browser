package browser

import (
	"os"
	"testing"
)

// TestMain runs the test binary as the recorder of a kept browser when a
// test's step starts it, as probe-browser runs itself.
func TestMain(m *testing.M) {
	if IsRecorder() {
		os.Exit(RunRecorder())
	}
	os.Exit(m.Run())
}
