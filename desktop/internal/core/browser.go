package core

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const defaultURL = "https://ipinfo.io/json"

// browserPaths lists where each browser lives per OS; the first existing path (or PATH entry) wins.
var browserPaths = map[string]map[string][]string{
	"chrome": {
		"darwin":  {"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		"windows": {`C:\Program Files\Google\Chrome\Application\chrome.exe`, `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`},
		"linux":   {"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"},
	},
	"brave": {
		"darwin":  {"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"},
		"windows": {`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`},
		"linux":   {"brave-browser", "brave"},
	},
	"firefox": {
		"darwin":  {"/Applications/Firefox.app/Contents/MacOS/firefox"},
		"windows": {`C:\Program Files\Mozilla Firefox\firefox.exe`},
		"linux":   {"firefox"},
	},
	"safari": {
		"darwin": {"/Applications/Safari.app"},
	},
}

var BrowserOrder = []string{"chrome", "brave", "firefox", "safari"}

func browserPath(name string) string {
	for _, p := range browserPaths[name][runtime.GOOS] {
		if filepath.IsAbs(p) {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		} else if found, err := exec.LookPath(p); err == nil {
			return found
		}
	}
	return ""
}

// InstalledBrowsers returns the supported browsers present on this machine, in display order.
func InstalledBrowsers() []string {
	var out []string
	for _, b := range BrowserOrder {
		if browserPath(b) != "" {
			out = append(out, b)
		}
	}
	return out
}

// profileDir is the browser data folder: ~/.porthole/shared/<chrome_profile>/<browser> when the region shares
// one (same logins everywhere), else ~/.porthole/profiles/<name>/<browser>. The CLI uses the same layout.
func profileDir(cfg *Config, p *Profile, browser string) (string, error) {
	shared, err := cfg.SharedProfile(p)
	if err != nil {
		return "", err
	}
	d := filepath.Join(StateDir(), "profiles", p.Name, browser)
	if shared != "" {
		d = filepath.Join(StateDir(), "shared", shared, browser)
	}
	return d, os.MkdirAll(d, 0o700)
}

var (
	proxyPortRe   = regexp.MustCompile(`--proxy-server=\S+:(\d+)`)
	firefoxPortRe = regexp.MustCompile(`"network\.proxy\.(?:socks|http)_port", (\d+)`)
)

// openPort reports the proxy port of a browser already running on data folder dir, if any. A browser
// keeps the proxy it started with, so a second launch on the same folder would join that process.
func openPort(browser, dir string) (int, bool) {
	if runtime.GOOS == "windows" {
		return 0, false // TODO: inspect command lines via the Windows process API
	}
	out, err := exec.Command("ps", "-axww", "-o", "command=").Output()
	if err != nil {
		return 0, false
	}
	marker := "--user-data-dir=" + dir
	if browser == "firefox" {
		marker = "-profile " + dir
	}
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.Index(line, marker)
		if i < 0 || strings.Contains(line, "Helper") {
			continue
		}
		if rest := line[i+len(marker):]; rest != "" && rest[0] != ' ' {
			continue // a longer path that merely starts with dir
		}
		var m []string
		if browser == "firefox" {
			prefs, _ := os.ReadFile(filepath.Join(dir, "user.js"))
			m = firefoxPortRe.FindStringSubmatch(string(prefs))
		} else {
			m = proxyPortRe.FindStringSubmatch(line)
		}
		port := 0
		if m != nil {
			port, _ = strconv.Atoi(m[1])
		}
		return port, true
	}
	return 0, false
}

// guardOpen refuses to launch when the data folder is already open through another region; the new window
// would otherwise silently go out through that region's proxy.
func guardOpen(cfg *Config, p *Profile, browser, dir string) error {
	port, open := openPort(browser, dir)
	if !open || port == p.Port {
		return nil
	}
	owner := fmt.Sprintf("port %d", port)
	for _, n := range cfg.Names() {
		if cfg.Profile[n].Port == port {
			owner = n
		}
	}
	label, _ := cfg.SharedProfile(p)
	if label == "" {
		label = p.Name
	}
	return fmt.Errorf("%s profile '%s' is already open through '%s'. Quit that %s window first: a browser keeps "+
		"the proxy it started with, so a new '%s' window would go out through '%s'", browser, label, owner, browser, p.Name, owner)
}

func browserEnv(p *Profile) []string {
	env := os.Environ()
	if p.TZ != "" {
		env = append(env, "TZ="+p.TZ) // Chromium and Firefox honour TZ on macOS and Linux
	}
	return env
}

func spawn(bin string, args []string, env []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// LaunchBrowser opens chrome, brave or firefox on the profile's data folder, pointed at the profile's proxy.
func LaunchBrowser(cfg *Config, p *Profile, browser string, urls []string) error {
	bin := browserPath(browser)
	if bin == "" {
		return fmt.Errorf("%s is not installed", browser)
	}
	if len(urls) == 0 {
		urls = []string{defaultURL}
	}
	dir, err := profileDir(cfg, p, browser)
	if err != nil {
		return err
	}
	if err := guardOpen(cfg, p, browser, dir); err != nil {
		return err
	}
	switch browser {
	case "chrome", "brave":
		args := []string{
			"--user-data-dir=" + dir,
			fmt.Sprintf("--proxy-server=socks5://127.0.0.1:%d", p.Port),
			"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
			"--no-first-run", "--no-default-browser-check",
		}
		if p.Lang != "" {
			args = append(args, "--lang="+p.Lang, "--accept-lang="+p.Lang)
		}
		return spawn(bin, append(args, urls...), browserEnv(p))
	case "firefox":
		if err := writeFirefoxPrefs(dir, p); err != nil {
			return err
		}
		return spawn(bin, append([]string{"-no-remote", "-profile", dir}, urls...), browserEnv(p))
	}
	return fmt.Errorf("unknown browser %q", browser)
}

func writeFirefoxPrefs(dir string, p *Profile) error {
	prefs := [][2]any{
		{"network.proxy.type", 1},
		{"network.proxy.socks", "127.0.0.1"},
		{"network.proxy.socks_port", p.Port},
		{"network.proxy.socks_version", 5},
		{"network.proxy.socks_remote_dns", true},
		{"network.proxy.no_proxies_on", "localhost, 127.0.0.1"},
		{"media.peerconnection.ice.proxy_only", true},
		{"network.trr.mode", 5},
		{"browser.shell.checkDefaultBrowser", false},
		{"browser.aboutwelcome.enabled", false},
	}
	if p.Lang != "" {
		prefs = append(prefs, [2]any{"intl.accept_languages", p.Lang})
	}
	var b strings.Builder
	for _, kv := range prefs {
		k, _ := json.Marshal(kv[0])
		v, _ := json.Marshal(kv[1])
		fmt.Fprintf(&b, "user_pref(%s, %s);\n", k, v)
	}
	return os.WriteFile(filepath.Join(dir, "user.js"), []byte(b.String()), 0o600)
}

// RenameBrowserData moves a region's own browser data (~/.porthole/profiles/<old>) to its new name, so logins
// survive a rename. Shared chrome_profile folders are named after the shared profile and need no move.
func RenameBrowserData(oldName, newName string) error {
	src := filepath.Join(StateDir(), "profiles", oldName)
	dst := filepath.Join(StateDir(), "profiles", newName)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	for _, b := range []string{"chrome", "brave", "firefox"} {
		if _, open := openPort(b, filepath.Join(src, b)); open {
			return fmt.Errorf("quit the %s window opened through %s first; its data folder is in use", b, oldName)
		}
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("browser data for %q already exists at %s; move or delete that folder first", newName, dst)
	}
	return os.Rename(src, dst)
}
