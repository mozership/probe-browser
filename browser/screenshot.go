package browser

import (
	"fmt"
	"net/http"
	"os"
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
