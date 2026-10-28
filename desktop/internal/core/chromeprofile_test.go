//go:build !windows

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestChromeProfileRoundTripAndDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PORTHOLE_CONFIG", filepath.Join(dir, "profiles.toml"))
	t.Setenv("PORTHOLE_HOME", filepath.Join(dir, "state"))
	os.WriteFile(ConfigPath(), []byte(`
[defaults]
chrome_profile = "main"

[profile.th]
type = "gluetun"
wg_conf = "secrets/th.conf"
port = 8893

[profile.us]
type = "ssh"
host = "x"
port = 1081
chrome_profile = "work"

[profile.in]
type = "ssh"
host = "y"
port = 1082
chrome_profile = "bad name"
`), 0o600)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(ConfigPath())
	for _, want := range []string{`chrome_profile = "main"`, `chrome_profile = "work"`} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("save dropped %s:\n%s", want, saved)
		}
	}
	cfg, _ = LoadConfig()
	cases := map[string]string{"th": "shared/main/chrome", "us": "shared/work/chrome"}
	for name, want := range cases {
		d, err := profileDir(cfg, cfg.Profile[name], "chrome")
		if err != nil || !strings.HasSuffix(d, want) {
			t.Errorf("%s: dir %q err %v, want suffix %q", name, d, err, want)
		}
	}
	if _, err := profileDir(cfg, cfg.Profile["in"], "chrome"); err == nil {
		t.Error("invalid chrome_profile accepted")
	}
	delete(cfg.Defaults, "chrome_profile")
	if d, _ := profileDir(cfg, cfg.Profile["th"], "chrome"); !strings.HasSuffix(d, "profiles/th/chrome") {
		t.Errorf("without chrome_profile th should use its own dir, got %s", d)
	}
}

func TestGuardOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PORTHOLE_HOME", dir)
	cfg := &Config{Defaults: map[string]any{"chrome_profile": "main"}, Profile: map[string]*Profile{
		"th": {Name: "th", Port: 8893}, "vn": {Name: "vn", Port: 8894},
	}}
	d, _ := profileDir(cfg, cfg.Profile["th"], "chrome")
	// A stand-in "browser": its command line carries the data folder and th's proxy.
	cmd := exec.Command("/bin/sh", "-c", "sleep 20; true", "--user-data-dir="+d, "--proxy-server=socks5://127.0.0.1:8893")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // sh and its sleep
	time.Sleep(200 * time.Millisecond)

	if err := guardOpen(cfg, cfg.Profile["th"], "chrome", d); err != nil {
		t.Errorf("same region should be allowed: %v", err)
	}
	err := guardOpen(cfg, cfg.Profile["vn"], "chrome", d)
	if err == nil || !strings.Contains(err.Error(), "already open through 'th'") {
		t.Errorf("other region should be refused, got %v", err)
	}
	if err := guardOpen(cfg, cfg.Profile["vn"], "chrome", d+"2"); err != nil {
		t.Errorf("a different folder must not match: %v", err)
	}
}

func TestRenameBrowserData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PORTHOLE_HOME", home)
	src := filepath.Join(home, "profiles", "th", "chrome")
	os.MkdirAll(src, 0o700)
	os.WriteFile(filepath.Join(src, "Cookies"), []byte("login"), 0o600)

	if err := RenameBrowserData("th", "bkk"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "profiles", "bkk", "chrome", "Cookies")); err != nil || string(b) != "login" {
		t.Fatalf("data not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "profiles", "th")); !os.IsNotExist(err) {
		t.Error("old folder still there")
	}
	if err := RenameBrowserData("nothing", "x"); err != nil {
		t.Errorf("missing source should be a no-op: %v", err)
	}

	os.MkdirAll(filepath.Join(home, "profiles", "vn", "chrome"), 0o700)
	if err := RenameBrowserData("bkk", "vn"); err == nil {
		t.Error("existing target folder should be refused")
	}

	open := filepath.Join(home, "profiles", "bkk", "chrome")
	cmd := exec.Command("/bin/sh", "-c", "sleep 20; true", "--user-data-dir="+open, "--proxy-server=socks5://127.0.0.1:8893")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	if err := RenameBrowserData("bkk", "bangkok"); err == nil || !strings.Contains(err.Error(), "quit the chrome window") {
		t.Errorf("rename while the browser is open should be refused, got %v", err)
	}
}
