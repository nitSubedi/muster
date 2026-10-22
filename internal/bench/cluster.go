package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nitSubedi/muster/internal/peerauth"
	"github.com/nitSubedi/muster/internal/registry"
	"github.com/nitSubedi/muster/internal/tlsid"
)

const (
	// Prefix is where agents serve benchmark endpoints.
	Prefix = "/muster/v1/bench"
	// MaxDownload caps one throughput measurement.
	MaxDownload = 256 << 20
)

// Peer is a device to benchmark against.
type Peer struct {
	Name string
	Addr string
}

// Runner drives a cluster benchmark from one device.
type Runner struct {
	Client        *http.Client // peer client: TLS, proofs checked by Runner
	Auth          *peerauth.Auth
	Token         string // for the local agent
	Pings         int
	DownloadBytes int
}

// Local benchmarks this device through its own agent.
func (r Runner) Local(ctx context.Context, agentURL string, withLLM bool) (Device, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agentURL+Prefix+"/device?llm="+llmFlag(withLLM), nil)
	if err != nil {
		return Device{}, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		return Device{}, err
	}
	defer resp.Body.Close()
	return decodeDevice(resp)
}

// Remote asks a peer to benchmark itself.
func (r Runner) Remote(ctx context.Context, p Peer, withLLM bool) (Device, error) {
	resp, err := r.signed(ctx, http.MethodPost, p.Addr, Prefix+"/device", "llm="+llmFlag(withLLM))
	if err != nil {
		return Device{}, err
	}
	defer resp.Body.Close()
	return decodeDevice(resp)
}

// RTT times Pings authenticated round trips over one kept-alive connection,
// after a warm-up request that pays for the TLS handshake.
func (r Runner) RTT(ctx context.Context, p Peer) (Stats, error) {
	var samples []time.Duration
	for i := 0; i <= r.Pings; i++ {
		start := time.Now()
		resp, err := r.signed(ctx, http.MethodGet, p.Addr, Prefix+"/echo", "")
		if err != nil {
			return Stats{}, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if i > 0 {
			samples = append(samples, time.Since(start))
		}
	}
	return Summarize(samples), nil
}

// Throughput downloads DownloadBytes from the peer and returns MB/s.
func (r Runner) Throughput(ctx context.Context, p Peer) (float64, error) {
	start := time.Now()
	resp, err := r.signed(ctx, http.MethodGet, p.Addr, Prefix+"/download", "bytes="+strconv.Itoa(r.DownloadBytes))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, err
	}
	return float64(n) / time.Since(start).Seconds() / 1e6, nil
}

// signed sends a peer-authenticated request and requires a proof bound to
// the certificate the peer presented (ADR-0005).
func (r Runner) signed(ctx context.Context, method, addr, path, query string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, registry.PeerURL(addr, path, query), nil)
	if err != nil {
		return nil, err
	}
	nonce := r.Auth.Sign(req)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if !r.Auth.CheckProof(resp.Header, nonce, tlsid.PeerFP(resp.TLS)) {
		resp.Body.Close()
		return nil, errors.New("peer failed authentication")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", resp.Status, b)
	}
	return resp, nil
}

func decodeDevice(resp *http.Response) (Device, error) {
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Device{}, fmt.Errorf("%s: %s", resp.Status, b)
	}
	var d Device
	return d, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d)
}

func llmFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
