//go:build windows

package diskspace

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func volumeKind(path string) string {
	return filepath.VolumeName(path) + " volume"
}

func freeBytes(path string) (int64, error) {
	utf16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(utf16, &available, &total, &totalFree); err != nil {
		return 0, err
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if available >= uint64(maxInt64) {
		// Impossible on real hardware; guards the int64 conversion anyway.
		return maxInt64, nil
	}
	return int64(available), nil
}
