// Package discovery finds other Muster agents on the local network with UDP
// broadcast beacons. A beacon only says "an agent of cluster C listens on
// port P at my source IP"; everything else is fetched over authenticated
// HTTP by the registry.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// Port is the UDP port beacons are sent to.
const Port = 7879

const protocolVersion = 1

// Beacon is the broadcast payload.
type Beacon struct {
	V       int    `json:"v"`
	Cluster string `json:"cluster"`
	ID      string `json:"id"`
	Port    int    `json:"port"`
}

// Encode serialises b with the current protocol version.
func Encode(b Beacon) []byte {
	b.V = protocolVersion
	out, _ := json.Marshal(b)
	return out
}

// Decode parses and validates a beacon.
func Decode(p []byte) (Beacon, error) {
	var b Beacon
	if err := json.Unmarshal(p, &b); err != nil {
		return Beacon{}, err
	}
	if b.V != protocolVersion || b.Cluster == "" || b.ID == "" || b.Port < 1 || b.Port > 65535 {
		return Beacon{}, errors.New("invalid beacon")
	}
	return b, nil
}

// Announce sends b to every target each interval until ctx is done. If
// targets is nil, BroadcastTargets is used, re-evaluated each round so
// network changes (Wi-Fi roaming, VPN up) are picked up.
func Announce(ctx context.Context, b Beacon, interval time.Duration, targets func() []netip.AddrPort) error {
	if targets == nil {
		targets = BroadcastTargets
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	payload := Encode(b)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for _, dst := range targets() {
			if _, err := conn.WriteToUDPAddrPort(payload, dst); err != nil {
				slog.Debug("beacon send failed", "dst", dst, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// BroadcastTargets returns the limited broadcast address plus the directed
// broadcast address of every up, non-loopback IPv4 interface. macOS only
// sends 255.255.255.255 out of the primary interface, so directed broadcasts
// are needed to reach peers on secondary interfaces.
func BroadcastTargets() []netip.AddrPort {
	out := []netip.AddrPort{netip.AddrPortFrom(netip.AddrFrom4([4]byte{255, 255, 255, 255}), Port)}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if bc, ok := directedBroadcast(n); ok {
					out = append(out, netip.AddrPortFrom(bc, Port))
				}
			}
		}
	}
	return out
}

func directedBroadcast(n *net.IPNet) (netip.Addr, bool) {
	ip4 := n.IP.To4()
	if ip4 == nil || len(n.Mask) != net.IPv4len {
		return netip.Addr{}, false
	}
	var b [4]byte
	for i := range b {
		b[i] = ip4[i] | ^n.Mask[i]
	}
	return netip.AddrFrom4(b), true
}

// Listener receives beacons.
type Listener struct {
	conn *net.UDPConn
}

// Listen binds addr, e.g. ":7879".
func Listen(addr string) (*Listener, error) {
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", ua)
	if err != nil {
		return nil, err
	}
	return &Listener{conn: conn}, nil
}

// Addr is the bound address.
func (l *Listener) Addr() netip.AddrPort {
	return l.conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// Serve calls found with the HTTP address of every valid beacon from
// cluster that is not from selfID, until ctx is done.
func (l *Listener) Serve(ctx context.Context, cluster, selfID string, found func(id string, addr netip.AddrPort)) {
	go func() {
		<-ctx.Done()
		l.conn.Close()
	}()
	buf := make([]byte, 1024)
	for {
		n, src, err := l.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("beacon read failed", "err", err)
			continue
		}
		b, err := Decode(buf[:n])
		if err != nil || b.Cluster != cluster || b.ID == selfID {
			continue
		}
		found(b.ID, netip.AddrPortFrom(src.Addr().Unmap(), uint16(b.Port)))
	}
}
