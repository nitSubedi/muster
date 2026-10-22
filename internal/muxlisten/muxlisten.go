// Package muxlisten serves TLS and plain HTTP on one port by looking at the
// first byte of each connection: 0x16 starts a TLS handshake. Agents talk
// TLS to each other while local apps keep using http://localhost.
package muxlisten

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

const (
	sniffTimeout = 3 * time.Second
	// maxSniffing bounds connections that have not yet sent a byte, so a
	// flood of silent sockets cannot exhaust file descriptors.
	maxSniffing = 256
)

type result struct {
	conn net.Conn
	err  error
}

// Listener yields *tls.Conn for TLS clients and plain conns otherwise.
// net/http recognises *tls.Conn and fills in Request.TLS.
type Listener struct {
	net.Listener
	cfg   *tls.Config
	sem   chan struct{}
	conns chan result
	once  sync.Once
	done  chan struct{}
}

// New wraps l. Sniffing happens off the accept loop, so a client that
// connects and stays silent cannot stall other connections.
func New(l net.Listener, cfg *tls.Config) *Listener {
	m := &Listener{Listener: l, cfg: cfg, sem: make(chan struct{}, maxSniffing), conns: make(chan result), done: make(chan struct{})}
	go m.acceptLoop()
	return m
}

// acceptLoop survives temporary errors (EMFILE, ECONNABORTED) with
// backoff, as net/http does; returning on them would leave Accept blocked
// forever while the process looks healthy.
func (m *Listener) acceptLoop() {
	backoff := 5 * time.Millisecond
	for {
		c, err := m.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Temporary() && !errors.Is(err, net.ErrClosed) { //nolint:staticcheck // Temporary is still the accept-retry signal net/http uses
				select {
				case <-time.After(backoff):
				case <-m.done:
					return
				}
				backoff = min(backoff*2, time.Second)
				continue
			}
			select {
			case m.conns <- result{err: err}:
			case <-m.done:
			}
			return
		}
		backoff = 5 * time.Millisecond
		select {
		case m.sem <- struct{}{}:
			go func() {
				defer func() { <-m.sem }()
				m.sniff(c)
			}()
		default:
			c.Close() // too many silent connections; shed load
		}
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
