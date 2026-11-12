package storagestate

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "state.json")
	// As Playwright writes it.
	if err := os.WriteFile(good, []byte(`{
  "cookies": [{"name": "sid", "value": "s-7", "domain": "localhost", "path": "/", "expires": -1, "httpOnly": true, "secure": false, "sameSite": "Lax"}],
  "origins": [{"origin": "http://localhost:8090", "localStorage": [{"name": "token", "value": "t-42"}]}]
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Read(good)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cookies) != 1 || s.Cookies[0] != (Cookie{Name: "sid", Value: "s-7", Domain: "localhost", Path: "/", Expires: -1, HTTPOnly: true, SameSite: "Lax"}) {
		t.Errorf("cookies %+v", s.Cookies)
	}
	if len(s.Origins) != 1 || s.Origins[0].Origin != "http://localhost:8090" || s.Origins[0].LocalStorage[0] != (Item{Name: "token", Value: "t-42"}) {
		t.Errorf("origins %+v", s.Origins)
	}

	if _, err := Read(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("cookies"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(bad); err == nil || !strings.Contains(err.Error(), "is not a storage state") {
		t.Errorf("not JSON: %v", err)
	}
}

func TestSetLocalStorage_NothingToKeep(t *testing.T) {
	// An origin without items opens no page, so no target is needed.
	origins := []Origin{{Origin: "http://localhost:8090", LocalStorage: []Item{}}}
	if err := setLocalStorage(t.Context(), nil, origins); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// startChrome starts Chrome for a test, and skips the test where there is
// none to start.
func startChrome(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox, chromedp.Flag("headless", true))
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	t.Cleanup(func() { cancel(); allocCancel() })
	if err := chromedp.Do(ctx); err != nil {
		t.Skipf("no Chrome to start: %v", err)
	}
	return ctx
}

// TestLoadAndSave gives a browser a state, and checks that the server gets
// the cookies, that the page reads its localStorage, that the server is
// asked for nothing before the first page, a favicon included, and that
// Save takes back what Load gave.
func TestLoadAndSave(t *testing.T) {
	ctx := startChrome(t)
	var (
		mu    sync.Mutex
		paths []string
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		who := "nobody"
		if c, err := r.Cookie("sid"); err == nil {
			who = c.Value
		}
		w.Write([]byte(`<!doctype html><link rel="icon" href="data:,"><p id="server">` + who + `</p>`))
	}))
	defer s.Close()

	state := &State{
		Cookies: []Cookie{{Name: "sid", Value: "s-7", Domain: "127.0.0.1", Path: "/", Expires: -1, HTTPOnly: true, SameSite: "Lax"}},
		Origins: []Origin{{Origin: s.URL, LocalStorage: []Item{{Name: "token", Value: "t-42"}}}},
	}
	if err := chromedp.Do(ctx, Load(state)); err != nil {
		t.Fatal(err)
	}
	// The browser asked for the favicon a moment after the page loaded.
	time.Sleep(time.Second)
	mu.Lock()
	if len(paths) != 0 {
		t.Errorf("the server was asked for %v before the first page", paths)
	}
	mu.Unlock()
	if url, err := chromedp.Run(ctx, chromedp.Location()); err != nil || url != "about:blank" {
		t.Errorf("left on %q, %v; want about:blank", url, err)
	}

	if _, err := chromedp.Run(ctx, chromedp.Navigate(s.URL+"/account")); err != nil {
		t.Fatal(err)
	}
	who, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.getElementById("server").textContent`))
	if err != nil || who != "s-7" {
		t.Errorf("the server saw %q, %v; want s-7", who, err)
	}
	token, err := chromedp.Run(ctx, chromedp.Evaluate[string](`localStorage.getItem("token")`))
	if err != nil || token != "t-42" {
		t.Errorf("the page read %q, %v; want t-42", token, err)
	}

	var saved State
	if err := chromedp.Do(ctx, Save(&saved)); err != nil {
		t.Fatal(err)
	}
	if len(saved.Cookies) != 1 || saved.Cookies[0] != state.Cookies[0] {
		t.Errorf("saved cookies %+v, want %+v", saved.Cookies, state.Cookies)
	}
	if len(saved.Origins) != 1 || saved.Origins[0].Origin != s.URL || len(saved.Origins[0].LocalStorage) != 1 || saved.Origins[0].LocalStorage[0] != state.Origins[0].LocalStorage[0] {
		t.Errorf("saved origins %+v", saved.Origins)
	}
}
