package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"porthole/internal/core"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ProfileView is one profile as the UI and tray see it.
type ProfileView struct {
	core.Profile
	Kind   string      `json:"kind"`
	Status core.Status `json:"status"`
}

type SafariView struct {
	Profile string `json:"profile"`
	All     bool   `json:"all"`
}

type AppState struct {
	Profiles        []ProfileView `json:"profiles"`
	Browsers        []string      `json:"browsers"`
	SafariSupported bool          `json:"safariSupported"`
	Safari          *SafariView   `json:"safari"`
	ConfigPath      string        `json:"configPath"`
	Error           string        `json:"error,omitempty"`
}

// SSHInput is the add/edit form for one of your own servers.
type SSHInput struct {
	Original      string `json:"original"` // name being edited, empty when adding
	Name          string `json:"name"`
	Host          string `json:"host"`
	User          string `json:"user"`
	KeyPath       string `json:"keyPath"`
	TZ            string `json:"tz"`
	ExpectCountry string `json:"expectCountry"`
}

// ProfileEdit changes the fields any profile has.
type ProfileEdit struct {
	Name          string `json:"name"`
	TZ            string `json:"tz"`
	ExpectCountry string `json:"expectCountry"`
	Lang          string `json:"lang"`
	Port          int    `json:"port"`
}

// Porthole is the service bound to the frontend; the tray calls the same methods.
type Porthole struct {
	app    *application.App
	mgr    *core.Manager
	mu     sync.Mutex
	cfg    *core.Config
	cfgErr error
	safari *core.SafariSession
	// onChange lets main rebuild the tray menu.
	onChange func(AppState)
}

func NewPorthole() *Porthole {
	b := &Porthole{mgr: core.NewManager()}
	b.mgr.OnChange = b.changed
	b.reload()
	return b
}

func (b *Porthole) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	b.app = application.Get()
	// A Safari session left by a crash keeps the system PAC pointing at a dead port. Restore it, unless
	// something (the CLI) is still serving that PAC.
	if who := core.SafariPending(); who != "" && !portBusy(b.config().PacPort()) {
		core.SafariRestore()
	}
	return nil
}

func (b *Porthole) ServiceShutdown() error {
	b.mu.Lock()
	s := b.safari
	b.safari = nil
	b.mu.Unlock()
	if s != nil {
		s.Stop()
	}
	b.mgr.DownAll()
	return nil
}

func portBusy(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err == nil {
		c.Close()
		return true
	}
	return false
}

func (b *Porthole) reload() {
	cfg, err := core.LoadConfig()
	b.mu.Lock()
	if err == nil {
		b.cfg = cfg
	}
	b.cfgErr = err
	if b.cfg == nil {
		b.cfg = &core.Config{Profile: map[string]*core.Profile{}}
	}
	b.mu.Unlock()
}

func (b *Porthole) config() *core.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

func (b *Porthole) profile(name string) (*core.Config, *core.Profile, error) {
	cfg := b.config()
	p, ok := cfg.Profile[name]
	if !ok {
		return nil, nil, fmt.Errorf("no profile named %q", name)
	}
	return cfg, p, nil
}

func (b *Porthole) changed() {
	st := b.State()
	if b.app != nil {
		b.app.Event.Emit("state", st)
	}
	if b.onChange != nil {
		b.onChange(st)
	}
}

// State returns everything the UI renders.
func (b *Porthole) State() AppState {
	cfg := b.config()
	st := AppState{Browsers: core.InstalledBrowsers(), SafariSupported: core.SafariSupported, ConfigPath: core.ConfigPath()}
	b.mu.Lock()
	if b.cfgErr != nil {
		st.Error = b.cfgErr.Error()
	}
	if b.safari != nil {
		st.Safari = &SafariView{Profile: b.safari.Profile, All: b.safari.All}
	}
	b.mu.Unlock()
	for _, n := range cfg.Names() {
		p := cfg.Profile[n]
		st.Profiles = append(st.Profiles, ProfileView{Profile: *p, Kind: p.Kind(), Status: b.mgr.Status(n)})
	}
	return st
}

// Reload re-reads profiles.toml (after editing it by hand or with the CLI).
func (b *Porthole) Reload() AppState {
	b.reload()
	b.changed()
	return b.State()
}

func (b *Porthole) Connect(name string) (core.Status, error) {
	cfg, p, err := b.profile(name)
	if err != nil {
		return core.Status{}, err
	}
	return b.mgr.Up(cfg, p)
}

func (b *Porthole) Disconnect(name string) {
	b.mu.Lock()
	s := b.safari
	b.mu.Unlock()
	if s != nil && s.Profile == name {
		b.StopSafari()
	}
	b.mgr.Down(name)
}

func (b *Porthole) DisconnectAll() {
	b.StopSafari()
	b.mgr.DownAll()
}

func (b *Porthole) Recheck(name string) (core.Status, error) {
	_, p, err := b.profile(name)
	if err != nil {
		return core.Status{}, err
	}
	return b.mgr.Recheck(p)
}

// Launch connects the profile if needed and opens a browser through it. Safari uses Safari mode
// (streaming domains only).
func (b *Porthole) Launch(name, browser string) error {
	if browser == "safari" {
		return b.LaunchSafari(name, false)
	}
	cfg, p, err := b.profile(name)
	if err != nil {
		return err
	}
	if _, err := b.mgr.Up(cfg, p); err != nil {
		return err
	}
	return core.LaunchBrowser(cfg, p, browser, nil)
}

// LaunchSafari points the system PAC at this profile (pac_set domains, or everything when all is true)
// and restarts Safari. The previous proxy settings come back when Safari quits.
func (b *Porthole) LaunchSafari(name string, all bool) error {
	cfg, p, err := b.profile(name)
	if err != nil {
		return err
	}
	b.mu.Lock()
	busy := b.safari != nil
	b.mu.Unlock()
	if busy {
		if err := b.StopSafari(); err != nil {
			return err
		}
	}
	if _, err := b.mgr.Up(cfg, p); err != nil {
		return err
	}
	var s *core.SafariSession
	s, err = core.StartSafari(cfg, p, all, nil, func(error) {
		b.mu.Lock()
		if b.safari == s {
			b.safari = nil
		}
		b.mu.Unlock()
		b.changed()
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.safari = s
	b.mu.Unlock()
	b.changed()
	return nil
}

func (b *Porthole) StopSafari() error {
	b.mu.Lock()
	s := b.safari
	b.safari = nil
	b.mu.Unlock()
	if s == nil {
		return nil
	}
	err := s.Stop()
	b.changed()
	return err
}

// ImportConfs asks for WireGuard .conf files and adds a profile for each.
func (b *Porthole) ImportConfs() ([]string, error) {
	paths, err := b.app.Dialog.OpenFile().
		SetTitle("Import WireGuard configs").
		CanChooseFiles(true).
		AddFilter("WireGuard config", "*.conf").
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	return b.ImportPaths(paths)
}

// ImportPaths imports dropped or picked .conf files.
func (b *Porthole) ImportPaths(paths []string) ([]string, error) {
	cfg := b.config()
	var names []string
	var errs []error
	for _, path := range paths {
		p, err := core.ImportConf(cfg, path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		names = append(names, p.Name)
	}
	b.changed()
	return names, errors.Join(errs...)
}

// PickKey asks for an SSH private key file and returns its path.
func (b *Porthole) PickKey() (string, error) {
	home, _ := os.UserHomeDir()
	return b.app.Dialog.OpenFile().
		SetTitle("Choose SSH private key").
		CanChooseFiles(true).
		ShowHiddenFiles(true).
		SetDirectory(filepath.Join(home, ".ssh")).
		PromptForSingleSelection()
}

var validName = func(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// SaveSSH adds or updates an SSH server profile.
func (b *Porthole) SaveSSH(in SSHInput) error {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if !validName(in.Name) {
		return errors.New("name: use lowercase letters, digits, - or _")
	}
	if strings.TrimSpace(in.Host) == "" {
		return errors.New("host is required")
	}
	cfg := b.config()
	if in.Original != in.Name && cfg.Profile[in.Name] != nil {
		return fmt.Errorf("a profile named %q already exists", in.Name)
	}
	p := &core.Profile{Type: "ssh"}
	if in.Original != "" {
		old, ok := cfg.Profile[in.Original]
		if !ok {
			return fmt.Errorf("no profile named %q", in.Original)
		}
		cp := *old
		p = &cp
		if in.Original != in.Name {
			if err := core.RenameBrowserData(in.Original, in.Name); err != nil {
				return err
			}
			b.Disconnect(in.Original)
			delete(cfg.Profile, in.Original)
		}
	}
	p.Name, p.Host, p.User = in.Name, strings.TrimSpace(in.Host), strings.TrimSpace(in.User)
	p.TZ, p.ExpectCountry = strings.TrimSpace(in.TZ), strings.ToUpper(strings.TrimSpace(in.ExpectCountry))
	p.SSHArgs = nil
	if k := strings.TrimSpace(in.KeyPath); k != "" {
		p.SSHArgs = []string{"-i", k, "-o", "IdentitiesOnly=yes"}
	}
	if p.Port == 0 {
		p.Port = cfg.FreePort(1081)
	}
	cfg.Profile[p.Name] = p
	err := cfg.Save()
	b.changed()
	return err
}

// EditProfile renames a profile or changes its timezone, expected country, language or port.
func (b *Porthole) EditProfile(original string, e ProfileEdit) error {
	cfg := b.config()
	p, ok := cfg.Profile[original]
	if !ok {
		return fmt.Errorf("no profile named %q", original)
	}
	e.Name = strings.ToLower(strings.TrimSpace(e.Name))
	if !validName(e.Name) {
		return errors.New("name: use lowercase letters, digits, - or _")
	}
	if e.Name != original && cfg.Profile[e.Name] != nil {
		return fmt.Errorf("a profile named %q already exists", e.Name)
	}
	if e.Port < 1024 || e.Port > 65535 {
		return errors.New("port must be between 1024 and 65535")
	}
	for n, other := range cfg.Profile {
		if n != original && other.Port == e.Port {
			return fmt.Errorf("port %d is used by %s", e.Port, n)
		}
	}
	if e.Name != original {
		if err := core.RenameBrowserData(original, e.Name); err != nil {
			return err
		}
	}
	if b.mgr.IsUp(original) && (e.Name != original || e.Port != p.Port) {
		b.Disconnect(original)
	}
	delete(cfg.Profile, original)
	p.Name, p.TZ, p.Lang, p.Port = e.Name, strings.TrimSpace(e.TZ), strings.TrimSpace(e.Lang), e.Port
	p.ExpectCountry = strings.ToUpper(strings.TrimSpace(e.ExpectCountry))
	cfg.Profile[p.Name] = p
	err := cfg.Save()
	b.changed()
	return err
}

// DeleteProfile removes a profile and, for imported configs, its copy of the .conf.
func (b *Porthole) DeleteProfile(name string) error {
	cfg := b.config()
	p, ok := cfg.Profile[name]
	if !ok {
		return nil
	}
	b.Disconnect(name)
	delete(cfg.Profile, name)
	if p.WGConf != "" && strings.HasPrefix(filepath.ToSlash(p.WGConf), "secrets/") {
		shared := false
		for _, other := range cfg.Profile {
			shared = shared || other.WGConf == p.WGConf
		}
		if !shared {
			os.Remove(cfg.Resolve(p.WGConf))
		}
	}
	err := cfg.Save()
	b.changed()
	return err
}

// OpenTestPages opens IP, WebRTC and DNS leak test pages in a browser through the profile.
func (b *Porthole) OpenTestPages(name, browser string) error {
	cfg, p, err := b.profile(name)
	if err != nil {
		return err
	}
	if _, err := b.mgr.Up(cfg, p); err != nil {
		return err
	}
	return core.LaunchBrowser(cfg, p, browser, []string{"https://ipinfo.io/json", "https://browserleaks.com/webrtc", "https://dnsleaktest.com"})
}
