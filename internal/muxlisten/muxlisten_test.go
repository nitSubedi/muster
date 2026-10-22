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
