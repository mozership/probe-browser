package outline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const formPage = `<!doctype html>
<html><head><title>Sign up</title></head><body>
<main>
  <h1>Create your account</h1>
  <form>
    <label for="email">Email</label><input id="email" type="email" required>
    <label for="pw">Password</label><input id="pw" type="password" value="s3cret">
    <label for="plan">Plan</label><select id="plan"><option value="free">Free</option><option value="pro">Pro</option></select>
    <label><input id="terms" type="checkbox"> I agree</label>
    <button type="submit">Sign up</button>
  </form>
  <div style="display:none"><button>Hidden</button></div>
  <nav aria-label="Footer"><a href="/next">Next page</a></nav>
  <img src="/missing.png" alt="logo">
</main>
<p>Outside main</p>
</body></html>`

// shadowPage is built of custom elements, whose text is in open shadow
// roots, where innerText does not reach.
const shadowPage = `<!doctype html>
<html><head><title>Notes</title></head><body>
<main>
  <h1>Your notes</h1>
  <note-card></note-card>
  <note-frame><b>Slotted words</b></note-frame>
</main>
<script>
customElements.define('note-card', class extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({mode: 'open'}).innerHTML =
      '<p>Inside the card</p><span id="nick">Nickname</span><input aria-labelledby="nick">';
  }
});
customElements.define('note-frame', class extends HTMLElement {
  constructor() {
    super();
    this.attachShadow({mode: 'open'}).innerHTML = '<div>Framed: <slot></slot></div>';
  }
});
</script>
</body></html>`

// startChrome starts Chrome on a page of a test server, and skips the test
// where there is none to start.
func startChrome(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, formPage) })
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><h1>Second page</h1>`)
	})
	mux.HandleFunc("/shadow", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, shadowPage) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox, chromedp.Flag("headless", true), chromedp.WindowSize(1280, 800))
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	t.Cleanup(func() { cancel(); allocCancel() })
	if err := chromedp.Do(ctx); err != nil {
		t.Skipf("no Chrome to start: %v", err)
	}
	ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(tcancel)
	ctx = context.WithValue(ctx, serverKey{}, srv.URL)
	return ctx
}

type serverKey struct{}

func open(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	if err := chromedp.Do(ctx, chromedp.Navigate(ctx.Value(serverKey{}).(string)+path)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, ctx context.Context, filter string, depth int, ref string) string {
	t.Helper()
	s, err := chromedp.Run(ctx, Read(filter, depth, ref))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// refOf returns the ref of the first line of lines that matches pattern.
func refOf(t *testing.T, lines, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(pattern) + `.*\[(ref_\d+)\]`)
	for _, line := range strings.Split(lines, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no line matches %q in:\n%s", pattern, lines)
	return ""
}

func TestRead(t *testing.T) {
	ctx := startChrome(t)
	open(t, ctx, "/")

	all := read(t, ctx, "", 0, "")
	for _, want := range []string{
		`heading "Create your account" level=1`,
		`textbox "Email" type=email required`,
		`textbox "Password" type=password value="••••"`,
		`combobox "Plan" value="Free" options="Free | Pro"`,
		`checkbox "I agree" unchecked`,
		`button "Sign up"`,
		`link "Next page" href="/next"`,
		`img "logo"`,
		`text "Outside main"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the page read lacks %q:\n%s", want, all)
		}
	}
	for _, unwanted := range []string{"Hidden", "s3cret"} {
		if strings.Contains(all, unwanted) {
			t.Errorf("the page read has %q:\n%s", unwanted, all)
		}
	}

	interactive := read(t, ctx, Interactive, 0, "")
	if strings.Contains(interactive, "heading") || strings.Contains(interactive, "text ") {
		t.Errorf("an interactive read has what one cannot act on:\n%s", interactive)
	}
	// A ref names its element from one read to the next.
	if a, b := refOf(t, all, `button "Sign up"`), refOf(t, interactive, `button "Sign up"`); a != b {
		t.Errorf("the button is %s, then %s", a, b)
	}

	nav := refOf(t, all, `navigation "Footer"`)
	if got := read(t, ctx, "", 0, nav); !strings.HasPrefix(got, "navigation") || !strings.Contains(got, "Next page") || strings.Contains(got, "Sign up") {
		t.Errorf("a read of %s is not the navigation alone:\n%s", nav, got)
	}
	if got := read(t, ctx, "", 1, ""); strings.Contains(got, "Next page") {
		t.Errorf("a read of depth 1 went into the navigation:\n%s", got)
	}
}

func TestRead_ARefGoesStaleOnNavigation(t *testing.T) {
	ctx := startChrome(t)
	open(t, ctx, "/")
	ref := refOf(t, read(t, ctx, "", 0, ""), `button "Sign up"`)
	open(t, ctx, "/next")
	_, err := chromedp.Run(ctx, Read("", 0, ref))
	if err == nil || !strings.Contains(err.Error(), "is not on the page any more") {
		t.Errorf("a read of a ref of the page before: %v, want that it is stale", err)
	}
}

func TestRead_RefusesAnotherFilter(t *testing.T) {
	ctx := startChrome(t)
	open(t, ctx, "/")
	_, err := chromedp.Run(ctx, Read("visible", 0, ""))
	if err == nil || !strings.Contains(err.Error(), `"visible"`) {
		t.Errorf("err = %v, want the filter refused", err)
	}
}

func TestText(t *testing.T) {
	ctx := startChrome(t)
	open(t, ctx, "/")
	text, err := chromedp.Run(ctx, Text())
	if err != nil {
		t.Fatal(err)
	}
	// The main content comes first, then the rest of the page.
	if i, j := strings.Index(text, "Create your account"), strings.Index(text, "Outside main"); i < 0 || j < i {
		t.Errorf("the text is not main first:\n%s", text)
	}

	open(t, ctx, "/shadow")
	text, err = chromedp.Run(ctx, Text())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Your notes", "Inside the card", "Framed: Slotted words"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}
	refOf(t, read(t, ctx, Interactive, 0, ""), `textbox "Nickname"`)
}

func TestScript_InstallsOnce(t *testing.T) {
	ctx := startChrome(t)
	open(t, ctx, "/")
	ref := refOf(t, read(t, ctx, "", 0, ""), `button "Sign up"`)
	// Evaluating the script again keeps the refs given out.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](Script)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, ctx, "", 0, ref); !strings.Contains(got, `button "Sign up"`) {
		t.Errorf("a read of %s after the script ran again: %s", ref, got)
	}
}
