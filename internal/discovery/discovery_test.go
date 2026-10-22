package discovery

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestEncodeDecode(t *testing.T) {
	b, err := Decode(Encode(Beacon{Cluster: "c1", ID: "n1", Port: 7878}))
	if err != nil || b.Cluster != "c1" || b.ID != "n1" || b.Port != 7878 || b.V != 1 {
		t.Fatalf("got %+v, %v", b, err)
	}
}

func TestDecodeRejectsInvalid(t *testing.T) {
	for _, in := range []string{
		`not json`,
		`{"v":2,"cluster":"c","id":"n","port":1}`,
		`{"v":1,"cluster":"","id":"n","port":1}`,
		`{"v":1,"cluster":"c","id":"n","port":0}`,
		`{"v":1,"cluster":"c","id":"n","port":70000}`,
	} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%s) accepted", in)
		}
	}
}

func TestDirectedBroadcast(t *testing.T) {
	_, n, _ := net.ParseCIDR("192.168.1.42/24")
	n.IP = net.ParseIP("192.168.1.42")
	got, ok := directedBroadcast(n)
	if !ok || got.String() != "192.168.1.255" {
		t.Fatalf("got %v %v", got, ok)
	}
}

func TestAnnounceAndListen(t *testing.T) {
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	target := func() []netip.AddrPort { return []netip.AddrPort{l.Addr()} }
	go Announce(ctx, Beacon{Cluster: "other", ID: "x", Port: 1}, 20*time.Millisecond, target)
	go Announce(ctx, Beacon{Cluster: "mine", ID: "self", Port: 2}, 20*time.Millisecond, target)
	go Announce(ctx, Beacon{Cluster: "mine", ID: "peer", Port: 7878}, 20*time.Millisecond, target)

	got := make(chan netip.AddrPort, 10)
	go l.Serve(ctx, "mine", "self", func(id string, addr netip.AddrPort) {
		if id != "peer" {
			t.Errorf("unexpected beacon from %s", id)
		}
		got <- addr
	})

	select {
	case addr := <-got:
		if addr.String() != "127.0.0.1:7878" {
			t.Fatalf("addr = %v", addr)
		}
	case <-ctx.Done():
		t.Fatal("no beacon received")
	}
}
