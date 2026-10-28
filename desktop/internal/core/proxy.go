package core

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Dialer opens connections out through a tunnel. Names are resolved at the far end.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Proxy serves SOCKS5 and HTTP (CONNECT and plain requests) on one local port, sniffing the first byte.
// Browsers get SOCKS5; Safari's PAC gets HTTP, which keeps DNS at the far end on macOS too.
type Proxy struct {
	ln     net.Listener
	dialer Dialer
	wg     sync.WaitGroup
	conns  sync.Map
	plain  *http.Transport
}

func ServeProxy(port int, d Dialer) (*Proxy, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("port %d is busy (is the CLI running this profile? `porthole down`): %w", port, err)
	}
	p := &Proxy{ln: ln, dialer: d, plain: &http.Transport{
		DialContext:         d.DialContext,
		Proxy:               nil,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}}
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

func (p *Proxy) Close() {
	p.ln.Close()
	p.conns.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
	p.plain.CloseIdleConnections()
	p.wg.Wait()
}

func (p *Proxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.conns.Store(c, nil)
		go func() {
			defer func() { p.conns.Delete(c); c.Close() }()
			p.handle(c)
		}()
	}
}

func (p *Proxy) handle(c net.Conn) {
	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 5 {
		p.socks5(c, br)
	} else {
		p.http(c, br)
	}
}

func (p *Proxy) socks5(c net.Conn, br *bufio.Reader) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if _, err := io.ReadFull(br, make([]byte, hdr[1])); err != nil { // offered auth methods
		return
	}
	c.Write([]byte{5, 0}) // no auth; the listener is loopback-only
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	if req[1] != 1 { // CONNECT only
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(br, b)
		host = net.IP(b).String()
	case 3:
		l, _ := br.ReadByte()
		b := make([]byte, l)
		io.ReadFull(br, b)
		host = string(b)
	case 4:
		b := make([]byte, 16)
		io.ReadFull(br, b)
		host = net.IP(b).String()
	default:
		c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	up, err := p.dialer.DialContext(ctx, "tcp", addr)
	cancel()
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	c.SetReadDeadline(time.Time{})
	splice(c, br, up)
}

func (p *Proxy) http(c net.Conn, br *bufio.Reader) {
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		c.SetReadDeadline(time.Time{})
		if req.Method == http.MethodConnect {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			up, err := p.dialer.DialContext(ctx, "tcp", req.Host)
			cancel()
			if err != nil {
				io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
				return
			}
			defer up.Close()
			io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
			splice(c, br, up)
			return
		}
		if req.URL.Host == "" {
			io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
			return
		}
		req.RequestURI = ""
		req.Header.Del("Proxy-Connection")
		req.Header.Del("Proxy-Authorization")
		resp, err := p.plain.RoundTrip(req)
		if err != nil {
			io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		err = resp.Write(c)
		resp.Body.Close()
		if err != nil || req.Close || resp.Close {
			return
		}
	}
}

// splice copies both ways; br may hold bytes the client already sent after the handshake.
// When either side finishes, the other gets a short grace period for in-flight data, then both close.
func splice(c net.Conn, br *bufio.Reader, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(up, br)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(c, up)
		closeWrite(c)
		done <- struct{}{}
	}()
	<-done
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Close()
		up.Close()
		<-done
	}
}

func closeWrite(c net.Conn) {
	if w, ok := c.(interface{ CloseWrite() error }); ok {
		w.CloseWrite()
	}
}
