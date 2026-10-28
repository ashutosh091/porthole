package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type ExitInfo struct {
	IP      string `json:"ip"`
	City    string `json:"city"`
	Country string `json:"country"`
	Org     string `json:"org"`
	// AltCountry is ip-api.com's answer when it differs from ipinfo.io (VPN "virtual locations").
	AltCountry string `json:"altCountry,omitempty"`
}

type Status struct {
	Name    string    `json:"name"`
	State   string    `json:"state"` // down | connecting | up | error
	Error   string    `json:"error,omitempty"`
	Exit    *ExitInfo `json:"exit,omitempty"`
	Warning string    `json:"warning,omitempty"`
}

type tunnel interface {
	Dialer
	Close()
}

type running struct {
	tun   tunnel
	proxy *Proxy
}

// Manager owns the running tunnels. OnChange fires after every status change, from any goroutine.
type Manager struct {
	mu       sync.Mutex
	running  map[string]*running
	status   map[string]*Status
	OnChange func()
}

func NewManager() *Manager {
	return &Manager{running: map[string]*running{}, status: map[string]*Status{}}
}

func (m *Manager) set(s Status) {
	m.mu.Lock()
	m.status[s.Name] = &s
	m.mu.Unlock()
	if m.OnChange != nil {
		m.OnChange()
	}
}

func (m *Manager) Status(name string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.status[name]; ok {
		return *s
	}
	return Status{Name: name, State: "down"}
}

func (m *Manager) IsUp(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[name]
	return ok
}

// Up starts a profile's tunnel and local proxy, then verifies the exit country. Safe to call when already up.
func (m *Manager) Up(cfg *Config, p *Profile) (Status, error) {
	if m.IsUp(p.Name) {
		return m.Status(p.Name), nil
	}
	m.set(Status{Name: p.Name, State: "connecting"})
	st, err := m.up(cfg, p)
	if err != nil {
		m.set(Status{Name: p.Name, State: "error", Error: err.Error()})
		return m.Status(p.Name), err
	}
	m.set(st)
	return st, nil
}

func (m *Manager) up(cfg *Config, p *Profile) (Status, error) {
	var tun tunnel
	switch p.Kind() {
	case "wireguard":
		wc, err := ParseWGConf(cfg.Resolve(p.WGConf))
		if err != nil {
			return Status{}, err
		}
		t, err := startWireGuard(wc)
		if err != nil {
			return Status{}, err
		}
		tun = t
	case "ssh":
		t, err := startSSH(p)
		if err != nil {
			return Status{}, err
		}
		tun = t
		go func() {
			<-t.Done()
			if m.stop(p.Name) {
				m.set(Status{Name: p.Name, State: "error", Error: "ssh tunnel dropped: " + t.errText()})
			}
		}()
	default:
		return Status{}, fmt.Errorf("%s picks servers from gluetun's built-in list, which only the CLI runs; import the location's .conf instead", p.Name)
	}
	px, err := ServeProxy(p.Port, tun)
	if err != nil {
		tun.Close()
		return Status{}, err
	}
	m.mu.Lock()
	m.running[p.Name] = &running{tun: tun, proxy: px}
	m.mu.Unlock()

	st, err := Check(p, tun)
	if err != nil {
		m.stop(p.Name)
		return Status{}, err
	}
	return st, nil
}

// stop tears down a tunnel; it reports whether one was running.
func (m *Manager) stop(name string) bool {
	m.mu.Lock()
	r, ok := m.running[name]
	delete(m.running, name)
	m.mu.Unlock()
	if ok {
		r.proxy.Close()
		r.tun.Close()
	}
	return ok
}

func (m *Manager) Down(name string) {
	m.stop(name)
	m.set(Status{Name: name, State: "down"})
}

func (m *Manager) DownAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.running))
	for n := range m.running {
		names = append(names, n)
	}
	m.mu.Unlock()
	for _, n := range names {
		m.Down(n)
	}
}

// Recheck refreshes the exit info of a running tunnel.
func (m *Manager) Recheck(p *Profile) (Status, error) {
	m.mu.Lock()
	r, ok := m.running[p.Name]
	m.mu.Unlock()
	if !ok {
		return m.Status(p.Name), fmt.Errorf("%s is not connected", p.Name)
	}
	st, err := Check(p, r.tun)
	if err != nil {
		m.set(Status{Name: p.Name, State: "up", Warning: err.Error()})
		return m.Status(p.Name), err
	}
	m.set(st)
	return st, nil
}

func geoClient(d Dialer) *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: d.DialContext}}
}

func getJSON(c *http.Client, url string, v any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// Check looks up the exit through the tunnel. When ipinfo.io disagrees with expect_country it asks
// ip-api.com too: VPN "virtual locations" are registered in one country and hosted in another.
func Check(p *Profile, d Dialer) (Status, error) {
	c := geoClient(d)
	var info ExitInfo
	var err error
	for attempt := 0; attempt < 3; attempt++ { // a fresh WireGuard tunnel needs a moment for its handshake
		if err = getJSON(c, "https://ipinfo.io/json", &info); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return Status{}, fmt.Errorf("%s: no response through the tunnel: %v", p.Name, err)
	}
	st := Status{Name: p.Name, State: "up", Exit: &info}
	want := p.ExpectCountry
	if want == "" || info.Country == want {
		return st, nil
	}
	var alt struct {
		CountryCode string `json:"countryCode"`
	}
	if getJSON(c, "http://ip-api.com/json/?fields=countryCode", &alt) == nil && alt.CountryCode == want {
		info.AltCountry = alt.CountryCode
		st.Warning = fmt.Sprintf("Virtual location: ip-api.com says %s, ipinfo.io says %s. Some sites will see %s.", want, info.Country, info.Country)
		return st, nil
	}
	return Status{}, fmt.Errorf("%s: exit is %s %s, %s, expected %s", p.Name, info.IP, info.City, info.Country, want)
}
