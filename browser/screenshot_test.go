package browser

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveScreenshot(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 16)), ".png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00" + strings.Repeat("\x00", 16)), ".jpg"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 " + strings.Repeat("\x00", 16)), ".webp"},
		{"unknown has no extension", []byte("\x00\x01\x02\x03"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			path, err := saveScreenshot(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if got := filepath.Ext(path); got != tt.ext {
				t.Errorf("extension = %q, want %q (path %s)", got, tt.ext, path)
			}
			if dir := filepath.Dir(path); dir != filepath.Clean(os.Getenv("TMPDIR")) {
				t.Errorf("saved in %s, want the temporary directory", dir)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Errorf("content = %q, want %q", got, tt.data)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want 0600: a screenshot can show private data", info.Mode().Perm())
			}
		})
	}
}

// TestSaveScreenshot_NamesEachFileApart keeps two screenshots of the same
// page from landing in one file.
func TestSaveScreenshot_NamesEachFileApart(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	data := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 16))
	a, err := saveScreenshot(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := saveScreenshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("both screenshots were saved to %s", a)
	}
}

func TestSaveScreenshot_FailsWithoutTheDirectory(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := saveScreenshot([]byte("x")); err == nil {
		t.Fatal("want an error when the temporary directory is missing")
	}
}

func TestRegionOf_Refused(t *testing.T) {
	for _, tc := range []struct {
		action map[string]any
		want   string
	}{
		{map[string]any{"name": "capture_screenshot", "x": 0, "y": 0, "width": 10}, "takes x, y, width and height together"},
		{map[string]any{"name": "capture_screenshot", "x": 0, "y": 0, "width": 10, "height": 0}, "is not one of the viewport"},
		{map[string]any{"name": "capture_screenshot", "x": -1, "y": 0, "width": 10, "height": 10}, "is not one of the viewport"},
		{map[string]any{"name": "capture_screenshot", "x": 0, "y": 0, "width": 10, "height": 10, "scale": 5}, "the scale is 5, but it is 1 to 4"},
		{map[string]any{"name": "full_screenshot", "width": 10}, "full_screenshot does not take width, height or scale"},
		{map[string]any{"name": "capture_screenshot", "direction": "down"}, "capture_screenshot does not take direction or amount"},
	} {
		req := NewReq()
		if err := req.parseData(map[string]any{"actions": []any{tc.action}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error = %v, want it to say %q", tc.action, err, tc.want)
		}
	}
}

// TestCaptureScreenshot_ARegionAndJPEG captures a region of the viewport,
// enlarged, and the viewport as JPEG, and reads the images back.
func TestCaptureScreenshot_ARegionAndJPEG(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	ret, err := Request(map[string]any{"headless": true, "timeout": "30s", "window_w": 800, "window_h": 600, "evidence_dir": t.TempDir(), "actions": []any{
		map[string]any{"name": "navigate", "url": "data:text/html,<body style='margin:0;height:3000px;background:red'>"},
		map[string]any{"name": "capture_screenshot", "id": "region", "x": 10, "y": 20, "width": 100, "height": 50, "scale": 2},
		map[string]any{"name": "capture_screenshot", "id": "jpeg", "quality": 80},
	}})
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("no Chrome to start: %v", err)
		}
		t.Fatal(err)
	}
	files := ret["res"].(map[string]any)["filepaths"].(map[string]string)
	for id, want := range map[string]struct {
		format string
		w, h   int
	}{"region": {"png", 200, 100}, "jpeg": {"jpeg", 0, 0}} {
		path := files[id]
		t.Cleanup(func() { _ = os.Remove(path) })
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if format != want.format || (want.w > 0 && (cfg.Width != want.w || cfg.Height != want.h)) {
			t.Errorf("%s: a %s of %dx%d, want a %s of %dx%d", id, format, cfg.Width, cfg.Height, want.format, want.w, want.h)
		}
	}
}

// TestCaptureScreenshot_ARegionOfAScrolledPage captures a region of the
// viewport of a page scrolled down: a region of what the viewport shows,
// not of the top of the page.
func TestCaptureScreenshot_ARegionOfAScrolledPage(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	// Red to 1000 pixels down, blue to 2000, green below: scrolled 1500
	// down, the viewport shows blue at its top, which a region of the top
	// of the page, or of 1500 below the viewport, would not.
	page := "data:text/html,<body style='margin:0'><div style='height:1000px;background:rgb(255,0,0)'></div><div style='height:1000px;background:rgb(0,0,255)'></div><div style='height:3000px;background:rgb(0,255,0)'></div><div id='mark' style='position:absolute;top:1500px;height:1px;width:1px'></div>"
	ret, err := Request(map[string]any{"headless": true, "timeout": "30s", "window_w": 800, "window_h": 600, "evidence_dir": t.TempDir(), "actions": []any{
		map[string]any{"name": "navigate", "url": page},
		// The wheel scrolls in steps a while on some machines, so the page is
		// scrolled at once, the mark at the top of the viewport.
		map[string]any{"name": "scroll", "selector": "#mark"},
		map[string]any{"name": "capture_screenshot", "id": "region", "x": 0, "y": 0, "width": 20, "height": 20},
	}})
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("no Chrome to start: %v", err)
		}
		t.Fatal(err)
	}
	path := ret["res"].(map[string]any)["filepaths"].(map[string]string)["region"]
	t.Cleanup(func() { _ = os.Remove(path) })
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := img.At(10, 10).RGBA(); b <= r || b <= g {
		t.Errorf("the region is not blue, of what the viewport shows, but %d, %d, %d", r>>8, g>>8, b>>8)
	}
}

func TestCaptureScreenshot_ARegionOutOfTheViewport(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	for _, r := range []map[string]any{
		{"x": 790, "y": 0, "width": 100, "height": 10},
		{"x": 900, "y": 0, "width": 10, "height": 10},
		{"x": 0, "y": 590, "width": 10, "height": 100},
	} {
		action := map[string]any{"name": "capture_screenshot"}
		maps.Copy(action, r)
		_, err := Request(map[string]any{"headless": true, "timeout": "30s", "window_w": 800, "window_h": 600, "evidence_dir": t.TempDir(), "actions": []any{
			map[string]any{"name": "navigate", "url": "data:text/html,<p>A page</p>"},
			action,
		}})
		if err != nil && strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("no Chrome to start: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "goes out of the viewport") {
			t.Errorf("%v: %v, want the region refused", r, err)
		}
	}
}
