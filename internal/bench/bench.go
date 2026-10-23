// Package bench measures the quantities that decide whether spreading work
// across devices helps: each device's memory bandwidth and real LLM speed,
// and each link's round-trip time and throughput.
//
// The rule they feed (see docs/BENCHMARKS.md): a device helps a job when
// the work it takes off the others exceeds the synchronisation it adds,
// i.e. (time saved) > (exchanges per step × round-trip time).
package bench

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nitSubedi/muster/internal/node"
)

// Device is one device's local measurements.
type Device struct {
	Name       string     `json:"name"`
	CPUs       int        `json:"cpus"`
	Load1      float64    `json:"load1"` // 1-minute load average; 0 if unknown
	MemReadGBs float64    `json:"mem_read_gbs"`
	LLM        *LLMResult `json:"llm,omitempty"`
	LLMError   string     `json:"llm_error,omitempty"`
}

// LLMResult is measured through the device's own Ollama.
type LLMResult struct {
	Model       string  `json:"model"`
	SizeBytes   int64   `json:"size_bytes"`
	DecodeTokS  float64 `json:"decode_tok_s"`
	PrefillTokS float64 `json:"prefill_tok_s"`
}

// EffectiveGBs is the model bytes streamed per second while generating:
// the bandwidth that actually bounds token-by-token decoding.
func (r LLMResult) EffectiveGBs() float64 { return float64(r.SizeBytes) * r.DecodeTokS / 1e9 }

// MemRead measures sustained multi-core memory read bandwidth in GB/s using
// a buffer far larger than any CPU cache. Best of three passes.
func MemRead(bufBytes int) float64 {
	words := bufBytes / 8
	buf := make([]uint64, words)
	for i := range buf { // fault every page in before timing
		buf[i] = uint64(i)
	}
	threads := runtime.NumCPU()
	chunk := words / threads
	best := 0.0
	var sink uint64
	var mu sync.Mutex
	for pass := 0; pass < 3; pass++ {
		var wg sync.WaitGroup
		start := time.Now()
		for t := 0; t < threads; t++ {
			part := buf[t*chunk : (t+1)*chunk]
			wg.Add(1)
			go func() {
				defer wg.Done()
				var a, b, c, d, e, f, g, h uint64
				for i := 0; i+8 <= len(part); i += 8 {
					a += part[i]
					b += part[i+1]
					c += part[i+2]
					d += part[i+3]
					e += part[i+4]
					f += part[i+5]
					g += part[i+6]
					h += part[i+7]
				}
				mu.Lock()
				sink += a + b + c + d + e + f + g + h
				mu.Unlock()
			}()
		}
		wg.Wait()
		gbs := float64(chunk*threads*8) / time.Since(start).Seconds() / 1e9
		if gbs > best {
			best = gbs
		}
	}
	_ = sink
	return best
}

// PickModel chooses a model to benchmark: a loaded one if possible (no
// cold start), else the smallest. Embedding-only models can't generate.
func PickModel(models []node.Model) (node.Model, bool) {
	var gen []node.Model
	for _, m := range models {
		if !strings.Contains(strings.ToLower(m.Name), "embed") {
			gen = append(gen, m)
		}
	}
	if len(gen) == 0 {
		return node.Model{}, false
	}
	sort.Slice(gen, func(i, j int) bool {
		if gen[i].Loaded != gen[j].Loaded {
			return gen[i].Loaded
		}
		return gen[i].Size < gen[j].Size
	})
	return gen[0], true
}

type generateResponse struct {
	PromptEvalCount    int   `json:"prompt_eval_count"`
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int   `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
}

// LLM measures decode and prefill speed for model on the Ollama server at
// ollamaURL. A random prefix defeats Ollama's prompt cache so prefill is
// really computed.
func LLM(ctx context.Context, client *http.Client, ollamaURL string, m node.Model, decodeTokens int) (LLMResult, error) {
	run := func(prompt string, predict int) (generateResponse, error) {
		body, _ := json.Marshal(map[string]any{
			"model": m.Name, "prompt": prompt, "stream": false, "keep_alive": "10m",
			"options": map[string]any{"num_predict": predict, "temperature": 0},
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ollamaURL, "/")+"/api/generate", bytes.NewReader(body))
		if err != nil {
			return generateResponse{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return generateResponse{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return generateResponse{}, fmt.Errorf("ollama: %s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		var g generateResponse
		return g, json.NewDecoder(resp.Body).Decode(&g)
	}
	if _, err := run("hi", 1); err != nil { // load the model; not timed
		return LLMResult{}, err
	}
	g, err := run(benchPrompt(), decodeTokens)
	if err != nil {
		return LLMResult{}, err
	}
	if g.EvalCount == 0 || g.EvalDuration == 0 || g.PromptEvalDuration == 0 {
		return LLMResult{}, fmt.Errorf("ollama returned no timing data for %s", m.Name)
	}
	return LLMResult{
		Model:       m.Name,
		SizeBytes:   m.Size,
		DecodeTokS:  float64(g.EvalCount) / (float64(g.EvalDuration) / 1e9),
		PrefillTokS: float64(g.PromptEvalCount) / (float64(g.PromptEvalDuration) / 1e9),
	}, nil
}

func benchPrompt() string {
	nonce := make([]byte, 8)
	rand.Read(nonce)
	var b strings.Builder
	b.WriteString("Reference " + hex.EncodeToString(nonce) + ". Summarise the following notes in one line.\n")
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "Note %d: devices on a local network share work when the time saved exceeds the time spent waiting for each other.\n", i)
	}
	return b.String()
}

// Stats summarises round-trip samples.
type Stats struct {
	Median time.Duration `json:"median"`
	P90    time.Duration `json:"p90"`
	Min    time.Duration `json:"min"`
}

// Summarize computes median, p90 and min. samples must be non-empty.
func Summarize(samples []time.Duration) Stats {
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := func(q float64) time.Duration { return s[int(q*float64(len(s)-1)+0.5)] }
	return Stats{Median: idx(0.5), P90: idx(0.9), Min: s[0]}
}

// Link is one device-to-device measurement.
type Link struct {
	From, To  string
	RTT       Stats   `json:"rtt"`
	ThroughMB float64 `json:"throughput_mbs"`
}

// SplitBreakEven returns the round-trip time below which splitting a model
// of sizeBytes with the given layer count across devices (each taking a
// share proportional to its bandwidth) beats the fastest single device for
// token-by-token decoding. Splitting each layer needs ~2 exchanges per
// layer per token. Bandwidths are in GB/s.
func SplitBreakEven(sizeBytes int64, layers int, bandwidths []float64) time.Duration {
	var sum, best float64
	for _, b := range bandwidths {
		sum += b
		best = max(best, b)
	}
	if sum == 0 || layers == 0 {
		return 0
	}
	w := float64(sizeBytes) / 1e9
	saved := w/best - w/sum // seconds per token
	return time.Duration(saved / float64(2*layers) * float64(time.Second))
}

// Busy reports whether other work on the device is using most of its CPU,
// so its measurements reflect what is free now, not what the hardware can do.
func (d Device) Busy() bool { return d.CPUs > 0 && d.Load1 > float64(d.CPUs) }
