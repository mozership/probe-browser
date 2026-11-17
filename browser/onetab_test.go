package browser

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestOneTab_TheFramesToo turns the script of one_tab on in a page and in a
// frame of its origin already loaded, and off in both.
func TestOneTab_TheFramesToo(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox, chromedp.Flag("headless", true))
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	if err := chromedp.Do(ctx); err != nil {
		t.Skipf("no Chrome to start: %v", err)
	}
	ctx, tcancel := context.WithTimeout(ctx, 20*time.Second)
	defer tcancel()
	if err := chromedp.Do(ctx, chromedp.Navigate(`data:text/html,<iframe srcdoc="<p>frame</p>"></iframe>`)); err != nil {
		t.Fatal(err)
	}
	read := "[window.__probeOneTab.on, frames[0].__probeOneTab.on]"
	// oneTabOn reaches the frame loaded before it, as of a kept browser.
	if err := chromedp.Do(ctx, chromedp.WaitReady("iframe"), chromedp.Evaluate[chromedp.Void](oneTabOn)); err != nil {
		t.Fatal(err)
	}
	on, err := chromedp.Run(ctx, chromedp.Evaluate[[2]bool](read))
	if err != nil || on != [2]bool{true, true} {
		t.Fatalf("installed: %v, %v", on, err)
	}
	if err := chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](oneTabOff)); err != nil {
		t.Fatal(err)
	}
	if on, err := chromedp.Run(ctx, chromedp.Evaluate[[2]bool](read)); err != nil || on != [2]bool{false, false} {
		t.Errorf("after oneTabOff: %v, %v; want off in the page and in its frame", on, err)
	}
}
