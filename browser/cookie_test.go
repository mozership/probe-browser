package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/mozership/probe/actionrpc"
)

const cookiePage = "http://localhost:8090/state.html"

func TestPollCookie(t *testing.T) {
	t.Run("takes the value of the cookie", func(t *testing.T) {
		read := func(context.Context) (string, []*network.Cookie, error) {
			return cookiePage, []*network.Cookie{{Name: "theme", Value: "dark"}, {Name: "access_token", Value: "t-42"}}, nil
		}
		got, err := pollCookie(context.Background(), "access_token", read)
		if err != nil || got != "t-42" {
			t.Errorf("got %q, %v, want %q", got, err, "t-42")
		}
	})

	t.Run("takes the first of two with the name", func(t *testing.T) {
		// The browser lists the one of the longer path first, as it sends it.
		read := func(context.Context) (string, []*network.Cookie, error) {
			return cookiePage, []*network.Cookie{{Name: "sid", Value: "inner", Path: "/app"}, {Name: "sid", Value: "outer", Path: "/"}}, nil
		}
		got, err := pollCookie(context.Background(), "sid", read)
		if err != nil || got != "inner" {
			t.Errorf("got %q, %v, want %q", got, err, "inner")
		}
	})

	t.Run("waits until the page sets it", func(t *testing.T) {
		calls := 0
		read := func(context.Context) (string, []*network.Cookie, error) {
			calls++
			if calls < 3 {
				return cookiePage, nil, nil
			}
			return cookiePage, []*network.Cookie{{Name: "access_token", Value: "t-42"}}, nil
		}
		got, err := pollCookie(context.Background(), "access_token", read)
		if err != nil || got != "t-42" || calls != 3 {
			t.Errorf("got %q, %v after %d reads, want %q after 3", got, err, calls, "t-42")
		}
	})

	t.Run("names the cookies but not their values when time runs out", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		read := func(context.Context) (string, []*network.Cookie, error) {
			return cookiePage, []*network.Cookie{{Name: "sid", Value: "secret"}, {Name: "theme", Value: "dark"}}, nil
		}
		_, err := pollCookie(ctx, "access_token", read)
		want := `get_cookie "access_token": the page http://localhost:8090/state.html has no such cookie; the cookies are ["sid" "theme"]`
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %s and wrap the deadline", err, want)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("error = %v, want no value of a cookie in it", err)
		}
	})

	t.Run("says when the page had no cookie", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		read := func(context.Context) (string, []*network.Cookie, error) { return cookiePage, nil, nil }
		_, err := pollCookie(ctx, "access_token", read)
		want := `the cookies are []`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %s", err, want)
		}
	})

	t.Run("gives the last error when the cookies could not be read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		read := func(context.Context) (string, []*network.Cookie, error) {
			return "", nil, errors.New("no frame")
		}
		_, err := pollCookie(ctx, "access_token", read)
		want := `get_cookie "access_token": the cookies could not be read (no frame)`
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to say %s and wrap the deadline", err, want)
		}
	})
}

func TestBuildActionTasks_GetCookie(t *testing.T) {
	req := NewReq()
	action := &ChromeDPAction{Name: "get_cookie", ID: "token", CookieName: "access_token"}
	req.Actions = []*ChromeDPAction{action}
	tasks, err := req.buildActionTasks()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1", len(tasks))
	}
	if action.reText == nil {
		t.Fatal("the value should be kept for res.results")
	}

	*action.reText = "t-42"
	results, _, err := req.collectResults()
	if err != nil || results["token"] != "t-42" {
		t.Errorf("got %v, %v, want the value under the id", results, err)
	}
}

func TestBuildActionTasks_GetCookieRefused(t *testing.T) {
	testCases := []struct {
		name   string
		action ChromeDPAction
		want   string
	}{
		{"without a name", ChromeDPAction{Name: "get_cookie"}, "get_cookie needs cookie_name"},
		{"on another action", ChromeDPAction{Name: "text", Selector: "h1", CookieName: "sid"}, "text does not take cookie_name"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewReq()
			req.Actions = []*ChromeDPAction{&tc.action}
			if _, err := req.buildActionTasks(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestGetCookieActionMapping(t *testing.T) {
	action := NewChromeDPAction()
	err := actionrpc.DecodeParams(map[string]any{"name": "get_cookie", "id": "token", "cookie_name": "access_token"}, action)
	if err != nil {
		t.Fatalf("MapToStructByTags failed: %v", err)
	}
	if action.CookieName != "access_token" {
		t.Errorf("got cookie_name %q, want %q", action.CookieName, "access_token")
	}
}
