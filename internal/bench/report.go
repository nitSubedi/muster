package bench

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Report is a whole-cluster benchmark.
type Report struct {
	Devices []Device `json:"devices"`
	Links   []Link   `json:"links"`
}

// Reference models for the "what this means" estimates (Q4 sizes).
var references = []struct {
	Name   string
	Bytes  int64
	Layers int
	Hidden int
}{
	{"7B", 4_700_000_000, 28, 3584},
	{"70B", 40_000_000_000, 80, 8192},
}

// Render writes the human-readable report.
func (r Report) Render(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tMEMORY READ\tLLM MODEL\tGENERATE\tREAD PROMPT\tEFFECTIVE")
	for _, d := range r.Devices {
		if d.LLM == nil {
			fmt.Fprintf(tw, "%s\t%.1f GB/s\t-\t-\t-\t(%s)\n", d.Name, d.MemReadGBs, d.LLMError)
			continue
		}
		fmt.Fprintf(tw, "%s\t%.1f GB/s\t%s\t%.1f tok/s\t%.0f tok/s\t%.1f GB/s\n",
			d.Name, d.MemReadGBs, d.LLM.Model, d.LLM.DecodeTokS, d.LLM.PrefillTokS, d.LLM.EffectiveGBs())
	}
	tw.Flush()
	for _, d := range r.Devices {
		if d.Busy() {
			fmt.Fprintf(w, "! %s is busy with other work (load %.1f on %d CPUs); its numbers show what's free now, not what it can do.\n", d.Name, d.Load1, d.CPUs)
		}
	}
	if len(r.Links) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "LINK\tROUND TRIP (median / p90 / min)\tTHROUGHPUT")
		for _, l := range r.Links {
			fmt.Fprintf(tw, "%s → %s\t%s / %s / %s\t%.1f MB/s\n", l.From, l.To, ms(l.RTT.Median), ms(l.RTT.P90), ms(l.RTT.Min), l.ThroughMB)
		}
		tw.Flush()
	}
	if lines := r.Implications(); len(lines) > 0 {
		fmt.Fprintln(w, "\nWHAT THIS MEANS (estimates from the numbers above)")
		for _, l := range lines {
			fmt.Fprintln(w, "  "+l)
		}
	}
}

// Implications turns measurements into the decisions that matter.
func (r Report) Implications() []string {
	var eff, pre []float64
	var best, bestPre float64
	bestName := ""
	for _, d := range r.Devices {
		if d.LLM == nil {
			continue
		}
		e := d.LLM.EffectiveGBs()
		p := d.LLM.PrefillTokS * float64(d.LLM.SizeBytes) / 1e9 // GB of weights processed per second
		eff, pre = append(eff, e), append(pre, p)
		if e > best {
			best, bestName = e, d.Name
		}
		bestPre = max(bestPre, p)
	}
	if len(eff) < 2 || len(r.Links) == 0 {
		return nil
	}
	var sumEff, sumPre float64
	for i := range eff {
		sumEff += eff[i]
		sumPre += pre[i]
	}
	var rtt time.Duration
	minThrough := r.Links[0].ThroughMB
	for _, l := range r.Links {
		rtt = max(rtt, l.RTT.Median)
		minThrough = min(minThrough, l.ThroughMB)
	}

	batch := fmt.Sprintf("Many independent jobs (batch): %.2f× the work of %s alone", sumEff/best, bestName)
	if sumEff/best < 1.05 {
		batch += ": no meaningful gain from the other devices."
	} else {
		batch += ", since every device's capacity adds up."
	}
	out := []string{batch}
	for _, ref := range references {
		need := SplitBreakEven(ref.Bytes, ref.Layers, eff)
		verdict := "slower than one device"
		if rtt < need {
			verdict = "faster"
		}
		alone := float64(ref.Bytes) / 1e9 / best
		split := float64(ref.Bytes)/1e9/sumEff + float64(2*ref.Layers)*rtt.Seconds()
		out = append(out, fmt.Sprintf(
			"Splitting a %s model layer by layer: needs round trips under %s; yours are %s, so %s (%.0f ms vs %.0f ms per token).",
			ref.Name, ms(need), ms(rtt), verdict, split*1000, alone*1000))
	}
	// Long prompt as an assembly line: compute in parallel, but every
	// boundary ships the prompt's activations (tokens × hidden × 2 bytes).
	ref := references[0]
	const promptTokens = 2000
	alone := promptTokens / (bestPre * 1e9 / float64(ref.Bytes))
	compute := promptTokens / (sumPre * 1e9 / float64(ref.Bytes))
	transfer := float64(promptTokens*ref.Hidden*2) / (minThrough * 1e6)
	pipe := max(compute, transfer) + rtt.Seconds()*float64(len(eff)-1)
	verdict := "no real difference"
	switch {
	case pipe < alone*0.95:
		verdict = "faster"
	case pipe > alone*1.05:
		verdict = "slower"
	}
	out = append(out, fmt.Sprintf(
		"Reading a %d-token prompt as an assembly line (%s model): %.1f s vs %.1f s alone, so %s.", promptTokens, ref.Name, pipe, alone, verdict))
	return out
}

func ms(d time.Duration) string {
	v := float64(d) / float64(time.Millisecond)
	switch {
	case v < 0.1:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".") + " ms"
	case v < 10:
		return fmt.Sprintf("%.1f ms", v)
	}
	return fmt.Sprintf("%.0f ms", v)
}
