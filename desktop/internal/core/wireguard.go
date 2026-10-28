package core

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// WGConf is the subset of a WireGuard .conf that a client tunnel needs.
type WGConf struct {
	PrivateKey   string
	Address      netip.Prefix
	DNS          []netip.Addr
	MTU          int
	PublicKey    string
	PresharedKey string
	Endpoint     string // host:port
}

func ParseWGConf(path string) (*WGConf, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &WGConf{MTU: 1420}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "privatekey":
			c.PrivateKey = v
		case "publickey":
			c.PublicKey = v
		case "presharedkey":
			c.PresharedKey = v
		case "endpoint":
			c.Endpoint = v
		case "mtu":
			fmt.Sscan(v, &c.MTU)
		case "address":
			for _, a := range strings.Split(v, ",") {
				if pfx, err := netip.ParsePrefix(strings.TrimSpace(a)); err == nil && pfx.Addr().Is4() && !c.Address.IsValid() {
					c.Address = pfx
				}
			}
		case "dns":
			for _, a := range strings.Split(v, ",") {
				if ip, err := netip.ParseAddr(strings.TrimSpace(a)); err == nil {
					c.DNS = append(c.DNS, ip)
				}
			}
		}
	}
	switch {
	case c.PrivateKey == "":
		return nil, fmt.Errorf("%s: no PrivateKey", path)
	case c.PublicKey == "":
		return nil, fmt.Errorf("%s: no peer PublicKey", path)
	case c.Endpoint == "":
		return nil, fmt.Errorf("%s: no Endpoint", path)
	case !c.Address.IsValid():
		return nil, fmt.Errorf("%s: no IPv4 Address", path)
	}
	if len(c.DNS) == 0 {
		c.DNS = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	return c, nil
}

func keyHex(b64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("bad WireGuard key")
	}
	return hex.EncodeToString(b), nil
}

// wgTunnel is a userspace WireGuard client: nothing touches the OS routing table or needs root.
type wgTunnel struct {
	dev  *device.Device
	tnet *netstack.Net
}

func startWireGuard(c *WGConf) (*wgTunnel, error) {
	host, port, err := net.SplitHostPort(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint %q: %w", c.Endpoint, err)
	}
	// Resolve the endpoint on every start: provider IPs rotate, which is why gluetun's lists went stale.
	ips, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip4", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve %s: %v", host, err)
	}
	priv, err := keyHex(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	pub, err := keyHex(c.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("peer public key: %w", err)
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{c.Address.Addr()}, c.DNS, c.MTU)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	uapi := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=25\n",
		priv, pub, net.JoinHostPort(ips[0].String(), port))
	if c.PresharedKey != "" {
		psk, err := keyHex(c.PresharedKey)
		if err != nil {
			dev.Close()
			return nil, fmt.Errorf("preshared key: %w", err)
		}
		uapi += "preshared_key=" + psk + "\n"
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &wgTunnel{dev: dev, tnet: tnet}, nil
}

func (t *wgTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.tnet.DialContext(ctx, network, addr)
}

func (t *wgTunnel) Close() { t.dev.Close() }
