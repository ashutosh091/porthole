package core

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// sshTunnel runs the system ssh client with -D, so ~/.ssh/config aliases, keys, agents and ProxyJump all work.
// Our own proxy fronts it, which gives Safari an HTTP endpoint and keeps DNS at the server.
type sshTunnel struct {
	cmd    *exec.Cmd
	socks  proxy.ContextDialer
	done   chan struct{}
	mu     sync.Mutex
	stderr bytes.Buffer
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func startSSH(p *Profile) (*sshTunnel, error) {
	port, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	target := p.Host
	if p.User != "" {
		target = p.User + "@" + p.Host
	}
	args := []string{"-N", "-D", fmt.Sprintf("127.0.0.1:%d", port),
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes"}
	args = append(append(args, p.SSHArgs...), target)
	t := &sshTunnel{cmd: exec.Command("ssh", args...), done: make(chan struct{})}
	t.cmd.Stderr = &lockedWriter{mu: &t.mu, buf: &t.stderr}
	if err := t.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ssh: %w", err)
	}
	go func() { t.cmd.Wait(); close(t.done) }()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-t.done:
			return nil, fmt.Errorf("ssh to %s exited: %s", target, t.errText())
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
			c.Close()
			d, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
			if err != nil {
				t.Close()
				return nil, err
			}
			t.socks = d.(proxy.ContextDialer)
			return t, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Close()
	return nil, fmt.Errorf("ssh to %s did not open its SOCKS port within 20s: %s", target, t.errText())
}

func (t *sshTunnel) errText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.stderr.String())
}

func (t *sshTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.socks.DialContext(ctx, network, addr)
}

// Done is closed when the ssh process exits (network drop, server reboot, killed).
func (t *sshTunnel) Done() <-chan struct{} { return t.done }

func (t *sshTunnel) Close() {
	if t.cmd.Process != nil {
		t.cmd.Process.Kill()
	}
	<-t.done
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(b)
}
