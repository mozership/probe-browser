package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// cookiePollInterval is how often get_cookie reads the cookies again while
// the one it waits for is not there.
const cookiePollInterval = 100 * time.Millisecond

// getCookie returns an action that waits until the page open has a cookie
// called name, and keeps its value in dst. The cookies are those the
// browser sends to the page's URL, HttpOnly ones included, so that a cookie
// of the same name for another site or path is not taken. A page often sets
// its cookie a moment after the request that brings it, as after a sign-in,
// so a cookie that is not there yet is waited for.
func getCookie(name string, dst *string) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		value, err := pollCookie(ctx, name, func(ctx context.Context) (string, []*network.Cookie, error) {
			url, err := chromedp.Location()(ctx, t)
			if err != nil {
				return "", nil, err
			}
			res, err := cdp.Call(ctx, t, network.GetCookies, network.GetCookiesParams{URLs: []string{url}})
			if err != nil {
				return url, nil, err
			}
			return url, res.Cookies, nil
		})
		if err != nil {
			return chromedp.Void{}, err
		}
		*dst = value
		return chromedp.Void{}, nil
	}
}

// pollCookie calls read until the cookies it returns hold one called name,
// or ctx ends. Of two with the name, as for two paths, the first is taken:
// the browser lists them in the order it sends them, the most specific
// first. When ctx ends, the error gives the names of the cookies the page
// had, but not their values, which can be secrets.
func pollCookie(ctx context.Context, name string, read func(context.Context) (string, []*network.Cookie, error)) (string, error) {
	var url string
	var names []string
	var lastErr error
	for {
		u, cookies, err := read(ctx)
		switch {
		case err == nil:
			for _, c := range cookies {
				if c.Name == name {
					return c.Value, nil
				}
			}
			url, names, lastErr = u, make([]string, len(cookies)), nil
			for i, c := range cookies {
				names[i] = c.Name
			}
		case ctx.Err() == nil:
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("get_cookie %q: the cookies could not be read (%v): %w", name, lastErr, ctx.Err())
			}
			if names == nil {
				names = []string{}
			}
			return "", fmt.Errorf("get_cookie %q: the page %s has no such cookie; the cookies are %q: %w", name, url, names, ctx.Err())
		case <-time.After(cookiePollInterval):
		}
	}
}
