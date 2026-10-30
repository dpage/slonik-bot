package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Find returns the Chrome or Chromium executable to use. A non-empty path is
// returned as it is if it names an existing file, and otherwise "" is returned
// so that a mistyped chrome_path is an error rather than a silent fallback.
// With an empty path it returns the first Chrome or Chromium found, or "" if
// there is none.
func Find(path string) string {
	if path != "" {
		if isFile(path) {
			return path
		}
		return ""
	}

	if runtime.GOOS == "darwin" {
		candidates := []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
		}
		candidates = append(candidates, "/Applications/Chromium.app/Contents/MacOS/Chromium")
		for _, c := range candidates {
			if isFile(c) {
				return c
			}
		}
	}

	// chromium covers the Docker image's /usr/bin/chromium.
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
