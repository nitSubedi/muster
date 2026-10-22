//go:build !windows

package node

func windowsMemory() uint64 { return 0 }
