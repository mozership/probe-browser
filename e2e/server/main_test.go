package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Fixture</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(dir))
	defer srv.Close()

	get := func(t *testing.T, path string, cookies ...*http.Cookie) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}

	t.Run("serves the fixture pages", func(t *testing.T) {
		if body := get(t, "/"); !strings.Contains(body, "<h1>Fixture</h1>") {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("signs in with an HttpOnly cookie", func(t *testing.T) {
		res, err := http.Post(srv.URL+"/sign-in", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		cookies := res.Cookies()
		if len(cookies) != 1 || cookies[0].Name != "sid" || cookies[0].Value != "s-7" || !cookies[0].HttpOnly {
			t.Fatalf("cookies = %v, want sid=s-7 HttpOnly", cookies)
		}
		if who := get(t, "/whoami", cookies[0]); who != "s-7" {
			t.Errorf("whoami = %q, want s-7", who)
		}
	})

	t.Run("nobody without the cookie", func(t *testing.T) {
		if who := get(t, "/whoami"); who != "nobody" {
			t.Errorf("whoami = %q, want nobody", who)
		}
	})
}
