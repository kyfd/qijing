//go:build !windows

package sysinfo

import "runtime"

// Info mirrors the Windows report with honest fallbacks; the product and
// its benchmarks ship on Windows.
type Info struct {
	OS          string  `json:"os"`
	OSVersion   string  `json:"windows_version"`
	Arch        string  `json:"arch"`
	CPUModel    string  `json:"cpu_model"`
	LogicalCPUs int     `json:"logical_cpus"`
	TotalRAMGiB float64 `json:"total_ram_gib"`
	Volume      string  `json:"volume"`
	DiskBusType string  `json:"disk_bus_type"`
	DriveKind   string  `json:"drive_kind"`
}

// Collect fills what runtime knows; disk details are unavailable here.
func Collect(volume string) (Info, error) {
	return Info{OS: runtime.GOOS, Arch: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(),
		Volume: volume, DiskBusType: "unknown", DriveKind: "unknown"}, nil
}

// PeakWorkingSetBytes is unavailable off Windows. It reports ok=false rather
// than a substitute number, so a report never contains an invented figure.
func PeakWorkingSetBytes() (uint64, bool) { return 0, false }
