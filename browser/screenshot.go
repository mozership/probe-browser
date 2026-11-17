package browser

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// screenshotExts names the file a screenshot is saved to for what its bytes
// are: full_screenshot below quality 100 is JPEG, the others PNG.
var screenshotExts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// saveScreenshot writes a captured screenshot to a new file in the temporary
// directory and returns its path. The directory is shared with other users
// and a screenshot can show a signed-in page, so only the owner may read the
// file; CreateTemp also refuses a file or symlink already at the name.
func saveScreenshot(data []byte) (string, error) {
	ext := screenshotExts[http.DetectContentType(data)]
	f, err := os.CreateTemp("", "probe-browser-screenshot-*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create the screenshot file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write the screenshot: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write the screenshot: %w", err)
	}
	return f.Name(), nil
}

// maxScale bounds how much capture_screenshot enlarges a region.
const maxScale = 4

// region is the part of the viewport capture_screenshot captures, in CSS
// pixels from its top left corner, enlarged scale times.
type region struct {
	x, y, width, height, scale float64
}

// viewportScreenshot returns an action that captures the viewport, or the
// region of it, as JPEG at a quality of 1 to 99, and as PNG otherwise, as
// full_screenshot does.
func viewportScreenshot(quality int, r *region, dst *[]byte) chromedp.Action[chromedp.Void] {
	params := page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatPng}
	if q := fullScreenshotQuality(quality); q < 100 {
		q64 := int64(q)
		params.Format, params.Quality = page.CaptureScreenshotFormatJpeg, &q64
	}
	scroll := chromedp.Evaluate[[2]float64]("[scrollX, scrollY]")
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		p := params
		if r != nil {
			// The clip is of the document, so the scroll is added to the
			// point of the viewport.
			s, err := scroll(ctx, t)
			if err != nil {
				return chromedp.Void{}, err
			}
			p.Clip = &page.Viewport{X: r.x + s[0], Y: r.y + s[1], Width: r.width, Height: r.height, Scale: r.scale}
		}
		res, err := cdp.Call(ctx, t, page.CaptureScreenshot, p)
		if err != nil {
			return chromedp.Void{}, err
		}
		*dst = res.Data
		return chromedp.Void{}, nil
	}
}

// regionOf returns the region the action captures, or nil for the whole
// viewport, or why it is not one.
func (a *ChromeDPAction) regionOf() (*region, error) {
	given := a.givesAny("x", "y", "width", "height", "scale")
	if a.Name != "capture_screenshot" {
		if a.givesAny("width", "height", "scale") {
			return nil, fmt.Errorf("%s does not take width, height or scale: only capture_screenshot does", a.Name)
		}
		return nil, nil
	}
	if !given {
		return nil, nil
	}
	if !a.givesAny("x") || !a.givesAny("y") || !a.givesAny("width") || !a.givesAny("height") {
		return nil, fmt.Errorf("capture_screenshot takes x, y, width and height together, the region of the viewport to capture")
	}
	if a.X < 0 || a.Y < 0 || a.Width <= 0 || a.Height <= 0 {
		return nil, fmt.Errorf("capture_screenshot: the region %g, %g, %g by %g is not one of the viewport: x and y are 0 or more, width and height more than 0", a.X, a.Y, a.Width, a.Height)
	}
	scale := a.Scale
	if !a.givesAny("scale") {
		scale = 1
	}
	if scale < 1 || scale > maxScale {
		return nil, fmt.Errorf("capture_screenshot: the scale is %g, but it is 1 to %d", scale, maxScale)
	}
	return &region{a.X, a.Y, a.Width, a.Height, scale}, nil
}
