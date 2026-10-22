package muxlisten

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/maauri/muster/internal/tlsid"
)

func TestServesTLSAndPlainOnOnePort(t *testing.T) {
	id, err := tlsid.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tls=%v", r.TLS != nil)
	})}
	go srv.Serve(New(ln, id.ServerConfig()))
	defer srv.Close()
	addr := ln.Addr().String()

	// A silent client must not block others.
	silent, _ := net.Dial("tcp", addr)
	defer silent.Close()

	get := func(c *http.Client, url string) string {
		resp, err := c.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	plain := &http.Client{Timeout: 3 * time.Second}
	if got := get(plain, "http://"+addr+"/"); got != "tls=false" {
		t.Fatalf("plain got %q", got)
	}
	var seen string
	tc := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection:   func(cs tls.ConnectionState) error { seen = tlsid.PeerFP(&cs); return nil },
	}}}
	if got := get(tc, "https://"+addr+"/"); got != "tls=true" {
		t.Fatalf("tls got %q", got)
	}
	if seen != id.FP {
		t.Fatalf("served cert fp %q, want %q", seen, id.FP)
	}
}

type flakyListener struct {
	net.Listener
	failed bool
}

type tempErr struct{}

func (tempErr) Error() string   { return "too many open files" }
func (tempErr) Timeout() bool   { return false }
func (tempErr) Temporary() bool { return true }

func (f *flakyListener) Accept() (net.Conn, error) {
	if !f.failed {
		f.failed = true
		return nil, tempErr{}
	}
	return f.Listener.Accept()
}

func TestSurvivesTemporaryAcceptError(t *testing.T) {
	id, _ := tlsid.LoadOrCreate(t.TempDir())
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}
	go srv.Serve(New(&flakyListener{Listener: ln}, id.ServerConfig()))
	defer srv.Close()
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatalf("listener died after a temporary error: %v", err)
	}
	resp.Body.Close()
}

func TestSilentFloodShedsInsteadOfBlocking(t *testing.T) {
	id, _ := tlsid.LoadOrCreate(t.TempDir())
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}
	go srv.Serve(New(ln, id.ServerConfig()))
	defer srv.Close()
	var silent []net.Conn
	for i := 0; i < maxSniffing+50; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			silent = append(silent, c)
		}
	}
	defer func() {
		for _, c := range silent {
			c.Close()
		}
	}()
	// Once the flood's sniff deadline passes, real clients get through.
	time.Sleep(sniffTimeout + 500*time.Millisecond)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatalf("server unusable after silent flood: %v", err)
	}
	resp.Body.Close()
}
