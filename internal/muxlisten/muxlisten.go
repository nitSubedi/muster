// Package muxlisten serves TLS and plain HTTP on one port by looking at the
// first byte of each connection: 0x16 starts a TLS handshake. Agents talk
// TLS to each other while local apps keep using http://localhost.
package muxlisten

import (
	"crypto/tls"
	"net"
	"sync"
	"time"
)

const sniffTimeout = 10 * time.Second

type result struct {
	conn net.Conn
	err  error
}

// Listener yields *tls.Conn for TLS clients and plain conns otherwise.
// net/http recognises *tls.Conn and fills in Request.TLS.
type Listener struct {
	net.Listener
	cfg   *tls.Config
	conns chan result
	once  sync.Once
	done  chan struct{}
}

// New wraps l. Sniffing happens off the accept loop, so a client that
// connects and stays silent cannot stall other connections.
func New(l net.Listener, cfg *tls.Config) *Listener {
	m := &Listener{Listener: l, cfg: cfg, conns: make(chan result), done: make(chan struct{})}
	go m.acceptLoop()
	return m
}

func (m *Listener) acceptLoop() {
	for {
		c, err := m.Listener.Accept()
		if err != nil {
			select {
			case m.conns <- result{err: err}:
			case <-m.done:
			}
			return
		}
		go m.sniff(c)
	}
}

func (m *Listener) sniff(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	var b [1]byte
	n, err := c.Read(b[:])
	c.SetReadDeadline(time.Time{})
	if err != nil || n == 0 {
		c.Close()
		return
	}
	pc := &prefixConn{Conn: c, first: b[0], has: true}
	var out net.Conn = pc
	if b[0] == 0x16 {
		out = tls.Server(pc, m.cfg)
	}
	select {
	case m.conns <- result{conn: out}:
	case <-m.done:
		c.Close()
	}
}

// Accept returns the next sniffed connection.
func (m *Listener) Accept() (net.Conn, error) {
	select {
	case r := <-m.conns:
		return r.conn, r.err
	case <-m.done:
		return nil, net.ErrClosed
	}
}

// Close stops accepting.
func (m *Listener) Close() error {
	m.once.Do(func() { close(m.done) })
	return m.Listener.Close()
}

type prefixConn struct {
	net.Conn
	first byte
	has   bool
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if p.has && len(b) > 0 {
		b[0], p.has = p.first, false
		if len(b) == 1 {
			return 1, nil
		}
		n, err := p.Conn.Read(b[1:])
		return n + 1, err
	}
	return p.Conn.Read(b)
}
