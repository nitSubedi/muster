// Package plan decides which role each device plays (ADR-0006). It is a
// pure function of measurements and live state, so every rule is tested
// without machines.
package plan

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nitSubedi/muster/internal/bench"
	"github.com/nitSubedi/muster/internal/node"
)

const (
	readableTokS   = 10   // chat must stay at least this fast
	minFreeForEmb  = 0.30 // embeddings go elsewhere only if that device is this free
	minBatchShare  = 0.05 // below this a device isn't worth coordinating
	memoryFitRatio = 0.75 // a model may use at most this much of device memory
	staleAfter     = 24 * time.Hour
)

// Device is one device's live state.
type Device struct {
	Name        string
	Healthy     bool
	BackendUp   bool
	CPUs        int
	Load1       float64
	MemoryBytes uint64 // accelerator memory: GPU if present, else RAM
	Models      []node.Model
}

// Input is everything the planner looks at.
type Input struct {
	Now   time.Time
	Bench bench.Report
	Live  []Device
}

// Assignment gives a device a role.
type Assignment struct {
	Role   string
	Device string
	Detail string
}

// Plan is the planner's output.
type Plan struct {
	Assignments []Assignment
	Suggestions []string
}

func freeCPU(load float64, cpus int) float64 {
	if cpus <= 0 {
		return 1
	}
	return min(max(1-load/float64(cpus), 0.05), 1)
}

// capacityNow is measured effective bandwidth (GB/s) scaled by how much
// more or less free the device is now than when it was measured.
func capacityNow(measured bench.Device, now Device) float64 {
	if measured.LLM == nil {
		return 0
	}
	return measured.LLM.EffectiveGBs() * freeCPU(now.Load1, now.CPUs) / freeCPU(measured.Load1, measured.CPUs)
}

func isEmbedding(name string) bool { return strings.Contains(strings.ToLower(name), "embed") }

type candidate struct {
	Device
	capacity float64
	share    float64
}

// Make produces the plan.
func Make(in Input) Plan {
	var p Plan
	measuredBy := map[string]bench.Device{}
	for _, d := range in.Bench.Devices {
		measuredBy[d.Name] = d
	}
	standby := map[string]string{}
	var eligible []*candidate
	for _, d := range in.Live {
		mdev, ok := measuredBy[d.Name]
		switch {
		case !d.Healthy:
			standby[d.Name] = "unreachable right now"
		case !d.BackendUp:
			standby[d.Name] = "Ollama isn't running, so it can route but not serve"
		case !ok:
			standby[d.Name] = "not measured yet; run `muster bench`"
		case mdev.LLM == nil:
			standby[d.Name] = "its LLM benchmark failed: " + mdev.LLMError
		default:
			eligible = append(eligible, &candidate{Device: d, capacity: capacityNow(mdev, d)})
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].capacity > eligible[j].capacity })
	var total float64
	for _, c := range eligible {
		total += c.capacity
	}
	for _, c := range eligible {
		if total > 0 {
			c.share = c.capacity / total
		}
	}
	// spare: free enough and strong enough to take a role off another device.
	spare := func(c *candidate) bool {
		return freeCPU(c.Load1, c.CPUs) >= minFreeForEmb && c.share >= minBatchShare
	}
	hasRole := map[string]bool{}
	assign := func(role, device, detail string) {
		p.Assignments = append(p.Assignments, Assignment{role, device, detail})
		hasRole[device] = true
	}

	// chat: the largest model, on any device, that stays readable.
	var chatDev *candidate
	var chatModel node.Model
	var chatTokS float64
	for _, c := range eligible {
		for _, mdl := range c.Models {
			if isEmbedding(mdl.Name) || mdl.Size == 0 || float64(mdl.Size) > memoryFitRatio*float64(c.MemoryBytes) {
				continue
			}
			tokS := c.capacity * 1e9 / float64(mdl.Size)
			if tokS < readableTokS {
				continue
			}
			if chatDev == nil || mdl.Size > chatModel.Size || (mdl.Size == chatModel.Size && tokS > chatTokS) {
				chatDev, chatModel, chatTokS = c, mdl, tokS
			}
		}
	}
	if chatDev != nil {
		assign("chat", chatDev.Name, fmt.Sprintf("%s at ~%.0f tok/s (largest model that stays above %d tok/s)", chatModel.Name, chatTokS, readableTokS))
	} else if len(eligible) > 0 {
		p.Suggestions = append(p.Suggestions, fmt.Sprintf("No device runs any installed model at %d tok/s or more. Pull a smaller model (e.g. `ollama pull qwen2.5:3b`) on %s.", readableTokS, eligible[0].Name))
	}

	// embeddings: off the chat device if another device is free enough.
	hasEmb := func(c *candidate) (node.Model, bool) {
		for _, mdl := range c.Models {
			if isEmbedding(mdl.Name) {
				return mdl, true
			}
		}
		return node.Model{}, false
	}
	var embDev *candidate
	var embModel node.Model
	for _, c := range eligible {
		if c == chatDev || !spare(c) {
			continue
		}
		if mdl, ok := hasEmb(c); ok {
			embDev, embModel = c, mdl
			break
		}
	}
	switch {
	case embDev != nil:
		assign("embeddings", embDev.Name, embModel.Name+", keeping chat's device free")
	case chatDev != nil:
		if mdl, ok := hasEmb(chatDev); ok {
			assign("embeddings", chatDev.Name, mdl.Name+" (shares the device with chat; no other device has enough spare capacity)")
		}
	}
	if embDev == nil {
		for _, c := range eligible {
			if c != chatDev && spare(c) {
				if _, ok := hasEmb(c); !ok {
					p.Suggestions = append(p.Suggestions, fmt.Sprintf("Run `ollama pull nomic-embed-text` on %s to move embeddings off %s.", c.Name, nameOr(chatDev, "the chat device")))
				}
				break
			}
		}
	}

	// batch: every device worth at least 5% of capacity, by share.
	for _, c := range eligible {
		if c.share >= minBatchShare {
			assign("batch", c.Name, fmt.Sprintf("%.0f%% of batch work", c.share*100))
		}
	}

	// standby: everyone else, with the reason.
	for _, c := range eligible {
		if hasRole[c.Name] {
			continue
		}
		reason := "would add only " + pct(c.share) + " of the cluster's capacity"
		if freeCPU(c.Load1, c.CPUs) < 0.5 {
			reason = fmt.Sprintf("busy with other work (load %.1f on %d CPUs); %s", c.Load1, c.CPUs, reason)
			p.Suggestions = append(p.Suggestions, fmt.Sprintf("%s has capacity tied up in other work (load %.1f on %d CPUs). Free it up, then run `muster bench` and `muster plan` again.", c.Name, c.Load1, c.CPUs))
		}
		standby[c.Name] = reason
	}
	for _, d := range in.Live {
		if r, ok := standby[d.Name]; ok {
			p.Assignments = append(p.Assignments, Assignment{"standby", d.Name, r})
		}
	}

	if age := in.Now.Sub(in.Bench.MeasuredAt); !in.Bench.MeasuredAt.IsZero() && age > staleAfter {
		p.Suggestions = append(p.Suggestions, fmt.Sprintf("Measurements are %d days old; run `muster bench` for a fresh plan.", int(age.Hours()/24)))
	}
	return p
}

func pct(f float64) string {
	if f < 0.001 {
		return "under 0.1%"
	}
	return fmt.Sprintf("%.1f%%", f*100)
}

func nameOr(c *candidate, def string) string {
	if c == nil {
		return def
	}
	return c.Name
}

// Render writes the plan as a table plus suggestions.
func (p Plan) Render(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tDEVICE\tWHY")
	for _, a := range p.Assignments {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", a.Role, a.Device, a.Detail)
	}
	tw.Flush()
	if len(p.Suggestions) > 0 {
		fmt.Fprintln(w, "\nSUGGESTIONS")
		for _, s := range p.Suggestions {
			fmt.Fprintln(w, "  - "+s)
		}
	}
	fmt.Fprintln(w, "\nThis is a dry run: nothing was changed.")
}
