// Package node describes a device in the cluster and collects facts about
// the local one.
package node

// Model is one model available on a node's inference backend.
type Model struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Loaded bool   `json:"loaded"`
}

// GPU is a discrete accelerator. Apple Silicon reports none; its unified
// memory is counted in Info.MemoryBytes.
type GPU struct {
	Name        string `json:"name"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

// Info is everything a node publishes about itself to the cluster.
type Info struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	CPUs        int     `json:"cpus"`
	MemoryBytes uint64  `json:"memory_bytes"`
	GPUs        []GPU   `json:"gpus,omitempty"`
	Backend     string  `json:"backend"`
	BackendUp   bool    `json:"backend_up"`
	Models      []Model `json:"models"`
	InFlight    int     `json:"in_flight"`
	// Port is the agent's API port, used for reachability probes.
	Port int `json:"port,omitempty"`
}

// AccelMemory is the memory available for model weights: total GPU memory
// if the node has discrete GPUs, otherwise system RAM.
func (i Info) AccelMemory() uint64 {
	var total uint64
	for _, g := range i.GPUs {
		total += g.MemoryBytes
	}
	if total > 0 {
		return total
	}
	return i.MemoryBytes
}

// Model returns the node's entry for name, matching normalised names.
func (i Info) Model(name string) (Model, bool) {
	want := NormalizeModel(name)
	for _, m := range i.Models {
		if NormalizeModel(m.Name) == want {
			return m, true
		}
	}
	return Model{}, false
}

// NormalizeModel makes "llama3" and "llama3:latest" compare equal.
func NormalizeModel(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		switch name[i] {
		case ':':
			return name
		case '/':
			return name + ":latest"
		}
	}
	return name + ":latest"
}
