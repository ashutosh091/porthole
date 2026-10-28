// Package core holds porthole's tunnels, local proxies and browser launching, shared by the desktop UI and tray.
package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// Profile mirrors a [profile.<name>] table in profiles.toml, the same file the Python CLI uses.
type Profile struct {
	Name          string            `toml:"-" json:"name"`
	Type          string            `toml:"type" json:"type"` // "ssh" or "gluetun"
	Host          string            `toml:"host,omitempty" json:"host,omitempty"`
	User          string            `toml:"user,omitempty" json:"user,omitempty"`
	SSHArgs       []string          `toml:"ssh_args,omitempty" json:"sshArgs,omitempty"`
	Port          int               `toml:"port" json:"port"`
	TZ            string            `toml:"tz,omitempty" json:"tz,omitempty"`
	ExpectCountry string            `toml:"expect_country,omitempty" json:"expectCountry,omitempty"`
	Lang          string            `toml:"lang,omitempty" json:"lang,omitempty"`
	Browser       string            `toml:"browser,omitempty" json:"browser,omitempty"`
	Provider      string            `toml:"provider,omitempty" json:"provider,omitempty"`
	VPN           string            `toml:"vpn,omitempty" json:"vpn,omitempty"`
	Countries     string            `toml:"countries,omitempty" json:"countries,omitempty"`
	Cities        string            `toml:"cities,omitempty" json:"cities,omitempty"`
	Hostnames     string            `toml:"hostnames,omitempty" json:"hostnames,omitempty"`
	Regions       string            `toml:"regions,omitempty" json:"regions,omitempty"`
	EnvFile       string            `toml:"env_file,omitempty" json:"envFile,omitempty"`
	WGConf        string            `toml:"wg_conf,omitempty" json:"wgConf,omitempty"`
	Env           map[string]string `toml:"env,omitempty" json:"env,omitempty"`
	PacSet        string            `toml:"pac_set,omitempty" json:"pacSet,omitempty"`
	// ChromeProfile shares browser data (logins) with other profiles that name the same one.
	ChromeProfile string `toml:"chrome_profile,omitempty" json:"chromeProfile,omitempty"`
}

// Kind is how the desktop app runs a profile: in-process WireGuard, a system ssh tunnel, or not at all
// (gluetun profiles that pick servers from gluetun's built-in list stay CLI-only).
func (p *Profile) Kind() string {
	switch {
	case p.Type == "ssh":
		return "ssh"
	case p.WGConf != "":
		return "wireguard"
	default:
		return "cli-only"
	}
}

type SafariConfig struct {
	PacSet   string   `toml:"pac_set,omitempty"`
	PacPort  int      `toml:"pac_port,omitempty"`
	Services []string `toml:"services,omitempty"`
}

type Config struct {
	Defaults map[string]any      `toml:"defaults,omitempty"`
	Profile  map[string]*Profile `toml:"profile"`
	Safari   SafariConfig        `toml:"safari,omitempty"`
	PacSets  map[string][]string `toml:"pac_sets,omitempty"`

	path string
}

// ConfigPath follows the CLI: PORTHOLE_CONFIG, then ~/.config/porthole/profiles.toml.
func ConfigPath() string {
	if p := os.Getenv("PORTHOLE_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "porthole", "profiles.toml")
}

// StateDir is ~/.porthole (or PORTHOLE_HOME), shared with the CLI for browser profiles and the Safari restore file.
func StateDir() string {
	if p := os.Getenv("PORTHOLE_HOME"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".porthole")
}

func LoadConfig() (*Config, error) {
	cfg := &Config{path: ConfigPath(), Profile: map[string]*Profile{}}
	if _, err := os.Stat(cfg.path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(cfg.path, cfg); err != nil {
		return nil, fmt.Errorf("reading %s: %w", cfg.path, err)
	}
	if cfg.Profile == nil {
		cfg.Profile = map[string]*Profile{}
	}
	for name, p := range cfg.Profile {
		p.Name = name
	}
	return cfg, nil
}

// Dir is the folder relative paths (wg_conf, env_file) are resolved against.
func (c *Config) Dir() string { return filepath.Dir(c.path) }

func (c *Config) Resolve(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(c.Dir(), rel)
}

func (c *Config) Save() error {
	if err := os.MkdirAll(c.Dir(), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# porthole profiles, shared by the porthole CLI and the desktop app.\n# See profiles.example.toml in the repo for every option.\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Profile))
	for n := range c.Profile {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// FreePort returns the first port from start that no profile uses.
func (c *Config) FreePort(start int) int {
	used := map[int]bool{}
	for _, p := range c.Profile {
		used[p.Port] = true
	}
	for port := start; ; port++ {
		if !used[port] {
			return port
		}
	}
}

func (c *Config) PacDomains(p *Profile) []string {
	set := p.PacSet
	if set == "" {
		set = c.Safari.PacSet
	}
	if set == "" {
		set = "streaming"
	}
	return c.PacSets[set]
}

// SharedProfile is the chrome_profile a region uses: its own setting, else [defaults]. Empty means the
// region keeps its own browser data.
func (c *Config) SharedProfile(p *Profile) (string, error) {
	name := p.ChromeProfile
	if name == "" {
		name, _ = c.Defaults["chrome_profile"].(string)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", fmt.Errorf("chrome_profile %q may only use letters, digits, - and _", name)
		}
	}
	return name, nil
}

func (c *Config) PacPort() int {
	if c.Safari.PacPort != 0 {
		return c.Safari.PacPort
	}
	return 18080
}
