package browser

import (
	"bytes"
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
