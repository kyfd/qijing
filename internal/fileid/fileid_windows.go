//go:build windows

package fileid

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Identify opens the path read-only — metadata only, no content access and
// no sharing interference — and reports its stable identity.
//
// The handle is opened with zero desired access plus FILE_FLAG_BACKUP_
// SEMANTICS: that grants metadata queries without read or write rights, and
// it works on directories too, so callers get consistent refusals when the
// target is not a regular file.
func Identify(path string) (Identity, error) {
	if !filepath.IsAbs(path) {
		return Identity{}, fmt.Errorf("file identity requires an absolute path: %q", path)
	}
	name, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return Identity{}, err
	}
	handle, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return Identity{}, err
	}
	defer windows.CloseHandle(handle)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return Identity{}, fmt.Errorf("read file information: %w", err)
	}
	return Identity{
		VolumeSerial: info.VolumeSerialNumber,
		FileID:       uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		Size:         int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow),
		ModTime:      filetimeToTime(info.LastWriteTime),
		CreationTime: filetimeToTime(info.CreationTime),
	}, nil
}

// extendedPath prefixes an absolute path with `\\?\` so long paths resolve
// without relying on the machine's LongPathsEnabled setting. UNC paths use
// the `\\?\UNC\` form. The prefix disables normalization, which is safe
// here because callers pass already-cleaned absolute paths.
func extendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\\?\` + path
}

func filetimeToTime(ft windows.Filetime) time.Time {
	return time.Unix(0, ft.Nanoseconds())
}
