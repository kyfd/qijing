//go:build windows

package pathsafe

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const cloudPlaceholderAttributes = windows.FILE_ATTRIBUTE_OFFLINE |
	windows.FILE_ATTRIBUTE_RECALL_ON_OPEN |
	windows.FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS

// RejectUnsafeFile rejects all reparse points and files whose attributes may
// cause Windows to hydrate remote cloud content when opened.
func RejectUnsafeFile(path string) error {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(path16)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s", ErrReparse, path)
	}
	if attributes&cloudPlaceholderAttributes != 0 {
		return fmt.Errorf("%w: %s", ErrCloudFile, path)
	}
	return nil
}
