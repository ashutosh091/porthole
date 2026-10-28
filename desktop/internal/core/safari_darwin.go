package core

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Safari only follows the macOS system proxy, so Safari mode sets a PAC on the active network service that
// routes the pac_set domains (or everything) through the profile, then restores the old setting when Safari
// quits. The restore file uses the CLI's format, so `porthole safari restore` can undo a crashed session too.

const SafariSupported = true

type autoProxy struct {
	URL     *string `json:"url"`
	Enabled bool    `json:"enabled"`
}

type restoreState struct {
	Profile  string               `json:"profile"`
	Services map[string]autoProxy `json:"services"`
}

func restoreFile() string { return filepath.Join(StateDir(), "safari-restore.json") }

type SafariSession struct {
	Profile string
	All     bool
	srv     *http.Server
	stopped chan struct{}
	once    sync.Once
	err     error
}

func PacText(port int, domains []string) string {
	target := fmt.Sprintf("PROXY 127.0.0.1:%d", port) // no DIRECT fallback: a dead tunnel fails, it doesn't leak
	match := "true"
	if domains != nil {
		d, _ := json.Marshal(domains)
		match = "(function(){var d=" + string(d) + ";for(var i=0;i<d.length;i++){if(host===d[i]||dnsDomainIs(host,'.'+d[i]))return true;}return false;})()"
	}
	return "function FindProxyForURL(url, host) {\n" +
		"  host = host.toLowerCase();\n" +
		"  if (isPlainHostName(host) || host === '127.0.0.1' || host === 'localhost') return 'DIRECT';\n" +
		"  if (" + match + ") return '" + target + "';\n" +
		"  return 'DIRECT';\n}\n"
}

func networkServices(cfg *Config) []string {
	if len(cfg.Safari.Services) > 0 {
		return cfg.Safari.Services
	}
	out, _ := exec.Command("route", "-n", "get", "default").Output()
	m := regexp.MustCompile(`interface:\s*(\S+)`).FindSubmatch(out)
	if m != nil && !strings.HasPrefix(string(m[1]), "utun") {
		ports, _ := exec.Command("networksetup", "-listallhardwareports").Output()
		for _, block := range strings.Split(string(ports), "\n\n") {
			name := regexp.MustCompile(`Hardware Port:\s*(.+)`).FindStringSubmatch(block)
			dev := regexp.MustCompile(`Device:\s*(\S+)`).FindStringSubmatch(block)
			if name != nil && dev != nil && dev[1] == string(m[1]) {
				return []string{strings.TrimSpace(name[1])}
			}
		}
	}
	return []string{"Wi-Fi"}
}

func readAutoProxy(service string) (autoProxy, error) {
	out, err := exec.Command("networksetup", "-getautoproxyurl", service).Output()
	if err != nil {
		return autoProxy{}, fmt.Errorf("reading proxy settings of %s: %w", service, err)
	}
	u := regexp.MustCompile(`URL:\s*(.*)`).FindStringSubmatch(string(out))
	e := regexp.MustCompile(`Enabled:\s*(\w+)`).FindStringSubmatch(string(out))
	ap := autoProxy{Enabled: e != nil && e[1] == "Yes"}
	if u != nil && strings.TrimSpace(u[1]) != "(null)" {
		s := strings.TrimSpace(u[1])
		ap.URL = &s
	}
	return ap, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// networksetupBatch runs the commands directly, falling back to one macOS admin prompt if they need it.
func networksetupBatch(cmds [][]string) error {
	failed := func(out []byte, err error) bool {
		s := strings.ToLower(string(out))
		return err != nil || strings.Contains(s, "error") || strings.Contains(s, "admin")
	}
	if out, err := exec.Command(cmds[0][0], cmds[0][1:]...).CombinedOutput(); !failed(out, err) {
		for _, c := range cmds[1:] {
			if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); failed(out, err) {
				return fmt.Errorf("%s: %s", strings.Join(c, " "), strings.TrimSpace(string(out)))
			}
		}
		return nil
	}
	parts := make([]string, len(cmds))
	for i, c := range cmds {
		q := make([]string, len(c))
		for j, a := range c {
			q[j] = shellQuote(a)
		}
		parts[i] = strings.Join(q, " ")
	}
	script := strings.Join(parts, " && ")
	script = strings.ReplaceAll(strings.ReplaceAll(script, `\`, `\\`), `"`, `\"`)
	out, err := exec.Command("osascript", "-e", `do shell script "`+script+`" with administrator privileges`).CombinedOutput()
	if err != nil {
		return fmt.Errorf("changing the system proxy failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// SafariRestore puts back the proxy settings saved when Safari mode started. No-op without a restore file.
func SafariRestore() error {
	data, err := os.ReadFile(restoreFile())
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var st restoreState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	var cmds [][]string
	for svc, ap := range st.Services {
		if ap.URL != nil {
			cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", svc, *ap.URL})
		}
		state := "off"
		if ap.Enabled {
			state = "on"
		}
		cmds = append(cmds, []string{"networksetup", "-setautoproxystate", svc, state})
	}
	if len(cmds) > 0 {
		if err := networksetupBatch(cmds); err != nil {
			return err
		}
	}
	return os.Remove(restoreFile())
}

// SafariPending reports a Safari session left behind (by a crash or by the CLI).
func SafariPending() string {
	data, err := os.ReadFile(restoreFile())
	if err != nil {
		return ""
	}
	var st restoreState
	json.Unmarshal(data, &st)
	if st.Profile == "" {
		return "unknown"
	}
	return st.Profile
}

func safariRunning() bool { return exec.Command("pgrep", "-x", "Safari").Run() == nil }

// StartSafari sets the PAC, restarts Safari and watches it; onEnd runs once the settings are restored.
func StartSafari(cfg *Config, p *Profile, all bool, urls []string, onEnd func(error)) (*SafariSession, error) {
	if who := SafariPending(); who != "" {
		return nil, fmt.Errorf("a Safari session (%s) is still active or was not cleaned up; stop or restore it first", who)
	}
	var domains []string
	if !all {
		domains = cfg.PacDomains(p)
		if len(domains) == 0 {
			return nil, fmt.Errorf("no pac_set domains configured for %s", p.Name)
		}
		for i, d := range domains {
			domains[i] = strings.TrimPrefix(strings.ToLower(d), ".")
		}
	}
	pac := []byte(PacText(p.Port, domains))
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.PacPort()))
	if err != nil {
		return nil, fmt.Errorf("cannot serve the PAC on port %d: %w", cfg.PacPort(), err)
	}
	s := &SafariSession{Profile: p.Name, All: all, stopped: make(chan struct{})}
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(pac)
	})}
	go s.srv.Serve(ln)

	services := networkServices(cfg)
	st := restoreState{Profile: p.Name, Services: map[string]autoProxy{}}
	for _, svc := range services {
		ap, err := readAutoProxy(svc)
		if err != nil {
			s.srv.Close()
			return nil, err
		}
		st.Services[svc] = ap
	}
	os.MkdirAll(StateDir(), 0o700)
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(restoreFile(), data, 0o600); err != nil {
		s.srv.Close()
		return nil, err
	}
	pacURL := fmt.Sprintf("http://127.0.0.1:%d/proxy.pac?v=%d", cfg.PacPort(), time.Now().Unix())
	var cmds [][]string
	for _, svc := range services {
		cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", svc, pacURL},
			[]string{"networksetup", "-setautoproxystate", svc, "on"})
	}
	if err := networksetupBatch(cmds); err != nil {
		os.Remove(restoreFile())
		s.srv.Close()
		return nil, err
	}

	// Safari must fully exit before relaunch: reopening a Safari that is still shutting down just
	// activates the dying process, which then exits and would end the session at once.
	if safariRunning() {
		exec.Command("osascript", "-e", `tell application "Safari" to quit`).Run()
		if !waitFor(func() bool { return !safariRunning() }, 30*time.Second) {
			s.Stop()
			return nil, fmt.Errorf("Safari did not quit within 30s (is a dialog open?); quit it yourself and try again")
		}
	}
	if len(urls) == 0 {
		urls = []string{defaultURL}
	}
	exec.Command("open", append([]string{"-a", "Safari"}, urls...)...).Run()
	if !waitFor(safariRunning, 30*time.Second) {
		s.Stop()
		return nil, fmt.Errorf("Safari did not start within 30s")
	}

	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		// Two misses in a row, so a brief pgrep miss doesn't end the session.
		for misses := 0; misses < 2; {
			select {
			case <-s.stopped:
				onEnd(s.err)
				return
			case <-t.C:
				if safariRunning() {
					misses = 0
				} else {
					misses++
				}
			}
		}
		s.Stop()
		onEnd(s.err)
	}()
	return s, nil
}

func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return cond()
}

// Stop restores the system proxy and stops serving the PAC. Safe to call more than once.
func (s *SafariSession) Stop() error {
	s.once.Do(func() {
		s.err = SafariRestore()
		s.srv.Close()
		close(s.stopped)
	})
	return s.err
}
