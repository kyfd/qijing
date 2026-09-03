//go:build windows

// Package sysinfo collects the environment facts a benchmark report must
// record so its numbers can be interpreted honestly: Windows version, CPU
// identity, memory, and the kind of disk holding the fixture.
package sysinfo

import (
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Info is a machine-readable snapshot of the benchmark environment.
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

// Collect gathers everything except the disk kind, which depends on the
// volume the benchmark targets.
func Collect(volume string) (Info, error) {
	info := Info{OS: "Windows", Arch: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(), Volume: volume}
	if v := windows.RtlGetVersion(); v != nil {
		info.OSVersion = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
	info.CPUModel = cpuModelFromRegistry()
	if totalGiB, ok := totalRAMGiB(); ok {
		info.TotalRAMGiB = totalGiB
	}
	bus, err := busTypeOf(volume)
	if err != nil {
		info.DiskBusType = "unknown: " + err.Error()
	} else {
		info.DiskBusType = bus
	}
	info.DriveKind = driveKind(volume)
	return info, nil
}

// memoryStatusEx mirrors MEMORYSTATUSEX; x/sys/windows does not declare
// GlobalMemoryStatusEx.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

var (
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	globalMemoryStatus = kernel32.NewProc("GlobalMemoryStatusEx")
	// K32GetProcessMemoryInfo lives in kernel32 on Windows 7+, so the
	// benchmark does not need to link psapi separately.
	getProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

func totalRAMGiB() (float64, bool) {
	var status memoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))
	result, _, err := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return 0, false
	}
	_ = err
	return float64(status.totalPhys) / (1 << 30), true
}

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS. Only PeakWorkingSetSize
// is read; the rest is declared so the struct size matches what the API expects.
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// PeakWorkingSetBytes reports the current process's peak working set, which is
// the memory figure a benchmark can state without modelling the allocator.
// It reports ok=false when the API fails rather than returning a guess.
func PeakWorkingSetBytes() (uint64, bool) {
	var counters processMemoryCounters
	counters.cb = uint32(unsafe.Sizeof(counters))
	result, _, _ := getProcessMemoryInfo.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.cb))
	if result == 0 {
		return 0, false
	}
	return uint64(counters.peakWorkingSetSize), true
}

func cpuModelFromRegistry() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "unknown"
	}
	defer key.Close()
	name, _, err := key.GetStringValue("ProcessorNameString")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(name)
}

// driveKind reports the GetDriveType classification as a fallback signal.
func driveKind(volume string) string {
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return "unknown"
	}
	switch windows.GetDriveType(root) {
	case windows.DRIVE_FIXED:
		return "fixed"
	case windows.DRIVE_REMOVABLE:
		return "removable"
	case windows.DRIVE_REMOTE:
		return "remote"
	case windows.DRIVE_CDROM:
		return "cdrom"
	default:
		return "unknown"
	}
}

const (
	// IOCTL_STORAGE_QUERY_PROPERTY from winioctl.h.
	ioctlStorageQueryProperty = 0x002D1400
	// StorageDeviceProperty from STORAGE_PROPERTY_ID.
	storageDeviceProperty = 0
)

// busTypeOf resolves the storage bus type (NVMe, SSD-class SATA, USB...)
// through IOCTL_STORAGE_QUERY_PROPERTY. SATA devices report BusTypeSata
// without distinguishing SSD from HDD; the report keeps the raw bus type
// instead of guessing.
func busTypeOf(volume string) (string, error) {
	root, err := windows.UTF16PtrFromString(`\\.\` + strings.TrimSuffix(volume, `\`))
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(root, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", fmt.Errorf("open volume: %w", err)
	}
	defer windows.CloseHandle(handle)

	// STORAGE_PROPERTY_QUERY: PropertyId + QueryType + AdditionalParameters.
	query := make([]byte, 16)
	query[0] = storageDeviceProperty // PropertyId = StorageDeviceProperty
	query[4] = 0                     // QueryType = PropertyStandardQuery
	out := make([]byte, 1024)
	var returned uint32
	err = windows.DeviceIoControl(handle, ioctlStorageQueryProperty,
		&query[0], uint32(len(query)), &out[0], uint32(len(out)), &returned, nil)
	if err != nil {
		return "", fmt.Errorf("storage query: %w", err)
	}
	if returned < 64 {
		return "", fmt.Errorf("storage descriptor too short: %d", returned)
	}
	// STORAGE_DEVICE_DESCRIPTOR: BusType lives at byte offset 28 within the
	// descriptor, and the descriptor starts at offset 0 of the buffer.
	busType := out[28]
	names := map[byte]string{
		1: "scsi", 2: "atapi", 3: "ata", 4: "ieee1394", 7: "sata",
		8: "sd", 9: "mmc", 11: "svm", 12: "nvme", 15: "ufs", 17: "scsi_ufs",
	}
	if name, ok := names[busType]; ok {
		return name, nil
	}
	return fmt.Sprintf("bus_type_%d", busType), nil
}
