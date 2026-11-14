package chrome

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// FindExecutable locates a Chrome / Chromium / Edge executable: the
// CREWBASE_BROWSER or CHROME_PATH environment variable first, then the
// usual install locations of the current OS. It returns "" when none exists.
func FindExecutable() string {
	for _, env := range []string{"CREWBASE_BROWSER", "CHROME_PATH"} {
		if p := os.Getenv(env); p != "" && isExecutable(p) {
			return p
		}
	}
	for _, c := range candidates(runtime.GOOS) {
		if filepath.IsAbs(c) {
			if isExecutable(c) {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func candidates(goos string) []string {
	switch goos {
	case "darwin":
		apps := []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		var out []string
		home, _ := os.UserHomeDir()
		for _, a := range apps {
			out = append(out, filepath.Join("/Applications", a))
			if home != "" {
				out = append(out, filepath.Join(home, "Applications", a))
			}
		}
		return append(out, "google-chrome", "chromium")
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Chromium\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
			)
		}
		return append(out, "chrome.exe", "msedge.exe")
	default:
		return []string{
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
			"chrome", "headless-shell", "microsoft-edge", "brave-browser",
			"/snap/bin/chromium", "/usr/bin/google-chrome",
		}
	}
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}
