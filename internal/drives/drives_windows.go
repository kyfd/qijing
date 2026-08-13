//go:build windows

package drives

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// List returns all logical Windows drives in drive-letter order.
func List() ([]Drive, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, fmt.Errorf("get logical drives: %w", err)
	}

	result := make([]Drive, 0, 26)
	for index := 0; index < 26; index++ {
		if mask&(1<<index) == 0 {
			continue
		}
		path := string(rune('A'+index)) + `:\`
		pathPointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, fmt.Errorf("encode drive path %q: %w", path, err)
		}
		driveType := windows.GetDriveType(pathPointer)
		_, accessErr := windows.GetFileAttributes(pathPointer)
		result = append(result, Drive{
			Path:       path,
			Type:       classify(driveType),
			Accessible: accessErr == nil && driveType != windows.DRIVE_NO_ROOT_DIR,
		})
	}
	return result, nil
}

func classify(driveType uint32) string {
	switch driveType {
	case windows.DRIVE_FIXED:
		return TypeFixed
	case windows.DRIVE_REMOVABLE:
		return TypeRemovable
	case windows.DRIVE_REMOTE:
		return TypeNetwork
	case windows.DRIVE_CDROM:
		return TypeCDROM
	case windows.DRIVE_RAMDISK:
		return TypeRAMDisk
	default:
		return TypeUnknown
	}
}
