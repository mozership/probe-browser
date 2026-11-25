package browser

import (
	"image"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/sdk"
)

func TestBuildActionTasks_ViewportRefused(t *testing.T) {
	for _, vp := range [][2]int{{800, 0}, {0, 600}, {-1, 600}} {
		req := NewReq()
		req.ViewportW, req.ViewportH = vp[0], vp[1]
		req.Actions = []*ChromeDPAction{{Name: "url"}}
		if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), "viewport_w and viewport_h go together") {
			t.Errorf("%v: %v, want it refused", vp, err)
		}
	}
}

// TestViewport_KeptInAKeptBrowser sets the viewport of a kept browser in
// the step that starts it and in the one that attaches to it, as the
// browser keeps it only for the connection that set it, and reads its size
// from a screenshot of each.
func TestViewport_KeptInAKeptBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	t.Setenv(CacheDirEnv, t.TempDir())
	a := &Action{log: hclog.NewNullLogger()}
	run := "run-viewport-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { _ = cleanupSessions(run) })
	var state map[string]any
	for i, actions := range [][]any{
		{map[string]any{"name": "navigate", "url": "data:text/html,<p>A page</p>"}, map[string]any{"name": "capture_screenshot", "id": "shot"}},
		{map[string]any{"name": "capture_screenshot", "id": "shot"}},
	} {
		ret, next, err := a.RunStep(sdk.Call{
			With: map[string]any{"session": true, "headless": true, "timeout": "30s", "evidence_dir": t.TempDir(),
				"viewport_w": 800, "viewport_h": 600, "actions": actions},
			State:     state,
			Step:      sdk.Step{RunID: run},
			Cleanups:  &sdk.Cleanups{},
			Processes: &sdk.Processes{},
		})
		if err != nil {
			if strings.Contains(err.Error(), "executable file not found") {
				t.Skipf("no Chrome to start: %v", err)
			}
			t.Fatalf("step %d: %v", i, err)
		}
		if next != nil {
			state = next
		}
		path := ret["res"].(map[string]any)["filepaths"].(map[string]string)["shot"]
		t.Cleanup(func() { _ = os.Remove(path) })
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, err := image.DecodeConfig(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Width != 800 || cfg.Height != 600 {
			t.Errorf("step %d: the viewport is %dx%d, want 800x600", i, cfg.Width, cfg.Height)
		}
	}
}
