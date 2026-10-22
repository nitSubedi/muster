package bench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nitSubedi/muster/internal/node"
)

func TestMemReadPositive(t *testing.T) {
	if gbs := MemRead(16 << 20); gbs <= 0 {
		t.Fatalf("MemRead = %v", gbs)
	}
}

func TestPickModelPrefersLoadedThenSmallestAndSkipsEmbeddings(t *testing.T) {
	models := []node.Model{
		{Name: "nomic-embed-text:latest", Size: 1},
		{Name: "qwen2.5:7b", Size: 7},
		{Name: "qwen2.5:3b", Size: 3},
	}
	if m, _ := PickModel(models); m.Name != "qwen2.5:3b" {
		t.Fatalf("got %s, want smallest generative", m.Name)
	}
	models[1].Loaded = true
	if m, _ := PickModel(models); m.Name != "qwen2.5:7b" {
		t.Fatalf("got %s, want loaded", m.Name)
	}
	if _, ok := PickModel([]node.Model{{Name: "nomic-embed-text"}}); ok {
		t.Fatal("picked an embedding model")
	}
}

func TestLLMComputesRatesAndDefeatsPromptCache(t *testing.T) {
	var prompts []string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Prompt string }
		json.NewDecoder(r.Body).Decode(&req)
		prompts = append(prompts, req.Prompt)
		calls.Add(1)
		// 300 prompt tokens in 0.5 s, 20 tokens in 1 s.
		w.Write([]byte(`{"prompt_eval_count":300,"prompt_eval_duration":500000000,"eval_count":20,"eval_duration":1000000000}`))
	}))
	defer srv.Close()

	r, err := LLM(context.Background(), srv.Client(), srv.URL, node.Model{Name: "m", Size: 2e9}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if r.DecodeTokS != 20 || r.PrefillTokS != 600 || r.EffectiveGBs() != 40 {
		t.Fatalf("got %+v eff=%v", r, r.EffectiveGBs())
	}
	if calls.Load() != 2 {
		t.Fatalf("want warm-up + measured call, got %d", calls.Load())
	}
	if p1, p2 := benchPrompt(), benchPrompt(); p1 == p2 || !strings.Contains(p1, "Note 23") {
		t.Fatal("bench prompt must be unique per run and long enough to measure prefill")
	}
}

func TestLLMReportsOllamaErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := LLM(context.Background(), srv.Client(), srv.URL, node.Model{Name: "x"}, 8); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestSummarize(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 10; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	st := Summarize(s)
	if st.Min != time.Millisecond || st.Median != 6*time.Millisecond || st.P90 != 9*time.Millisecond {
		t.Fatalf("got %+v", st)
	}
}

func TestSplitBreakEven(t *testing.T) {
	// Two equal 100 GB/s devices, 4 GB model, 28 layers:
	// alone 40 ms/token, split 20 ms -> saves 20 ms over 56 exchanges.
	got := SplitBreakEven(4e9, 28, []float64{100, 100})
	ms := 20.0 / 56 * float64(time.Millisecond)
	want := time.Duration(ms)
	if diff := got - want; diff > time.Microsecond || diff < -time.Microsecond {
		t.Fatalf("got %v, want %v", got, want)
	}
	// A very slow second device barely helps, so the bar is tiny.
	if SplitBreakEven(4e9, 28, []float64{130, 1}) > 10*time.Microsecond {
		t.Fatal("slow device should need an almost zero round trip to help")
	}
	if SplitBreakEven(4e9, 28, []float64{100}) != 0 {
		t.Fatal("single device has nothing to gain")
	}
}
