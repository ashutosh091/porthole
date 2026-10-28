package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type directDialer struct{ seen []string }

func (d *directDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.seen = append(d.seen, addr)
	var nd net.Dialer
	return nd.DialContext(ctx, network, addr)
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.toml")
	os.WriteFile(path, []byte(`
[defaults]
browser = "chrome"
[profile.kr]
type = "gluetun"
provider = "custom"
wg_conf = "secrets/kr.conf"
port = 8895
tz = "Asia/Seoul"
[profile.us]
type = "ssh"
host = "1.2.3.4"
ssh_args = ["-i", "/k"]
port = 1081
[profile.uk]
type = "gluetun"
provider = "mullvad"
countries = "United Kingdom"
env_file = "secrets/s.env"
env = { WIREGUARD_MTU = "1380" }
port = 8891
[safari]
pac_set = "streaming"
[pac_sets]
streaming = ["netflix.com"]
`), 0o600)
	t.Setenv("PORTHOLE_CONFIG", path)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile["kr"].Kind() != "wireguard" || cfg.Profile["us"].Kind() != "ssh" || cfg.Profile["uk"].Kind() != "cli-only" {
		t.Fatalf("kinds wrong: %+v", cfg.Profile)
	}
	if cfg.Resolve(cfg.Profile["kr"].WGConf) != filepath.Join(dir, "secrets/kr.conf") {
		t.Fatal("resolve")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if again.Profile["uk"].Env["WIREGUARD_MTU"] != "1380" || again.Profile["us"].SSHArgs[1] != "/k" ||
		again.PacSets["streaming"][0] != "netflix.com" || again.Defaults["browser"] != "chrome" || again.Safari.PacSet != "streaming" {
		data, _ := os.ReadFile(path)
		t.Fatalf("round trip lost data:\n%s", data)
	}
	if got := again.FreePort(8891); got != 8892 {
		t.Fatalf("FreePort = %d", got)
	}
}

func TestProxySocksAndHTTP(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello "+r.URL.Path) }))
	defer origin.Close()
	port, _ := freeLocalPort()
	d := &directDialer{}
	px, err := ServeProxy(port, d)
	if err != nil {
		t.Fatal(err)
	}
	defer px.Close()
	target := strings.Replace(origin.URL, "127.0.0.1", "localhost", 1) + "/x"
	for _, scheme := range []string{"socks5h", "http"} {
		out, err := exec.Command("curl", "-sS", "--max-time", "5", "-x", fmt.Sprintf("%s://127.0.0.1:%d", scheme, port), target).CombinedOutput()
		if err != nil || string(out) != "hello /x" {
			t.Fatalf("%s: %v %q", scheme, err, out)
		}
	}
	// Names must reach the dialer unresolved, so DNS happens at the far end.
	for _, a := range d.seen {
		if !strings.HasPrefix(a, "localhost:") {
			t.Fatalf("dialer saw %q, want a hostname", a)
		}
	}
	// HTTPS-style CONNECT through the HTTP side.
	out, err := exec.Command("curl", "-sS", "--max-time", "5", "-p", "-x", fmt.Sprintf("http://127.0.0.1:%d", port), target).CombinedOutput()
	if err != nil || string(out) != "hello /x" {
		t.Fatalf("connect: %v %q", err, out)
	}
}

func TestParseWGConf(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.conf")
	os.WriteFile(p, []byte("[Interface]\nAddress = 10.14.0.2/16, fd00::2/64\nPrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nDNS = 1.1.1.1\n[Peer]\nPublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA=\nEndpoint = vpn.example.net:51820\n"), 0o600)
	c, err := ParseWGConf(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Address.String() != "10.14.0.2/16" || c.Endpoint != "vpn.example.net:51820" || len(c.DNS) != 1 {
		t.Fatalf("%+v", c)
	}
	if _, err := keyHex(c.PrivateKey); err != nil {
		t.Fatal(err)
	}
}

// TestLive runs real profiles from ~/.config/porthole on alternate ports: PORTHOLE_LIVE=kr,in go test -run Live ./internal/core
func TestLive(t *testing.T) {
	names := os.Getenv("PORTHOLE_LIVE")
	if names == "" {
		t.Skip("set PORTHOLE_LIVE=profile,... to run")
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.DownAll()
	for i, name := range strings.Split(names, ",") {
		p := *cfg.Profile[name]
		p.Port = 28890 + i // stay clear of CLI tunnels on the real ports
		st, err := m.Up(cfg, &p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s (%s) exit %s %s, %s %s", name, p.Kind(), st.Exit.IP, st.Exit.City, st.Exit.Country, st.Warning)
		out, err := exec.Command("curl", "-sS", "--max-time", "15", "-x", fmt.Sprintf("http://127.0.0.1:%d", p.Port), "https://ipinfo.io/ip").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != st.Exit.IP {
			t.Fatalf("%s: HTTP proxy side gave %q (%v)", name, out, err)
		}
	}
}
