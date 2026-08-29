//go:build windows

package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrRecycleAborted reports that Windows did not recycle the item, either
// because the shell refused it or because the user dismissed the warning that
// the item was too large for the Recycle Bin.
var ErrRecycleAborted = errors.New("the item was not moved to the Recycle Bin")

const (
	foDelete           = 0x0003
	fofSilent          = 0x0004
	fofNoConfirmation  = 0x0010
	fofAllowUndo       = 0x0040
	fofNoErrorUI       = 0x0400
	fofWantNukeWarning = 0x4000
)

// shFileOpStructW mirrors SHFILEOPSTRUCTW. Field order and types reproduce the
// native alignment on both 386 and amd64.
type shFileOpStructW struct {
	hwnd                  windows.Handle
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

var (
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	shFileOperationW = shell32.NewProc("SHFileOperationW")
)

// MoveToRecycleBin moves one absolute path into the Windows Recycle Bin so the
// user can restore it from Explorer. FOF_WANTNUKEWARNING keeps the operation
// reversible: when the shell would have to destroy the item instead of
// recycling it, Windows asks first and an unconfirmed operation is reported as
// ErrRecycleAborted rather than silently deleting anything.
func MoveToRecycleBin(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("recycle path is not absolute: %q", path)
	}
	from, err := doubleNullTerminated(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("encode recycle path: %w", err)
	}
	operation := shFileOpStructW{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofWantNukeWarning | fofNoErrorUI | fofSilent,
	}
	result, _, _ := shFileOperationW.Call(uintptr(unsafe.Pointer(&operation)))
	if result != 0 {
		return fmt.Errorf("SHFileOperationW failed with code 0x%x", result)
	}
	if operation.fAnyOperationsAborted != 0 {
		return ErrRecycleAborted
	}
	return nil
}

// doubleNullTerminated encodes a single path as the double-null-terminated
// UTF-16 list that SHFileOperationW expects.
func doubleNullTerminated(path string) ([]uint16, error) {
	encoded, err := syscall.UTF16FromString(path)
	if err != nil {
		return nil, err
	}
	return append(encoded, 0), nil
}
