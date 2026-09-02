//go:build windows

package appdir

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errACLUnavailable reports that the filesystem cannot answer ACL questions
// (FAT/exFAT, unusual mounts). There is nothing to restrict there; the caller
// records the skip instead of failing the app.
var errACLUnavailable = errors.New("filesystem does not provide ACLs")

// restrictToCurrentUser gives the data root an explicit, protected DACL that
// grants Full control only to the current user, SYSTEM and Administrators.
//
// An explicit DACL is required rather than trusting profile inheritance:
// corporate templates, redirected profiles and sandboxed environments can
// legitimately attach broader inherited grants (for example a lab group with
// Read on the whole profile). A protected DACL strips those on this one
// dedicated directory and is deterministic on every machine.
func restrictToCurrentUser(path string) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	// P = protected (no inherited ACEs); OI CI = children inherit, so every
	// file the app creates below the root carries the same restriction.
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("build data directory DACL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("build data directory DACL: no DACL produced")
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("restrict data directory: %w", err)
	}
	return nil
}

// aclSupported reports whether the volume backing path answers ACL queries.
func aclSupported(path string) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	_, _, err = sd.DACL()
	return err == nil
}

// verifyUserPrivate fails when the directory's DACL grants any access to a
// broad principal. After restrictToCurrentUser this is an assertion of what
// the app itself set; it is also usable in tests against explicit DACLs.
func verifyUserPrivate(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%w: %v", errACLUnavailable, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%w: no readable DACL", errACLUnavailable)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		grants, trustee, err := allowedTrustee(dacl, index)
		if err != nil {
			return fmt.Errorf("read ACE %d: %w", index, err)
		}
		if grants && broadSIDs[trustee.String()] {
			return fmt.Errorf("DACL grants access to %s", trustee.String())
		}
	}
	return nil
}

func currentUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", fmt.Errorf("open process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read token user: %w", err)
	}
	return user.User.Sid.String(), nil
}

// broadSIDs are well-known principals that must never hold grants on the
// data root: Everyone, Authenticated Users and BUILTIN\Users.
var broadSIDs = map[string]bool{
	"S-1-1-0":      true, // Everyone
	"S-1-5-11":     true, // Authenticated Users
	"S-1-5-32-545": true, // BUILTIN\Users
}

// aceHeader mirrors ACE_HEADER (type, flags, size).
type aceHeader struct {
	aceType  byte
	aceFlags byte
	aceSize  uint16
}

const (
	accessAllowedAceType = 0x0
	inheritOnlyAceFlag   = 0x8
)

// allowedTrustee returns the trustee SID of the index-th ACE when that ACE
// grants access to the object itself (allowed type, not inherit-only).
// Deny and audit ACEs grant nothing and are reported as (false, nil, nil).
//
// The pointer arithmetic mirrors ACCESS_ALLOWED_ACE in winnt.h: an ACE
// starts after the ACL header, ACEs are chained by aceSize, and the trustee
// SID begins after the 4-byte access mask. Every dereference converts
// uintptr(unsafe.Pointer(acl)) + delta in one expression, as unsafe rules
// require.
func allowedTrustee(acl *windows.ACL, index uint32) (bool, *windows.SID, error) {
	if index >= uint32(acl.AceCount) {
		return false, nil, errors.New("ACE index out of range")
	}
	base := unsafe.Pointer(acl)
	aclHeaderSize := unsafe.Sizeof(windows.ACL{})
	headerSize := unsafe.Sizeof(aceHeader{})
	maskSize := unsafe.Sizeof(uint32(0))
	var offset uintptr
	for walked := uint32(0); walked < index; walked++ {
		size := *(*uint16)(unsafe.Pointer(uintptr(base) + aclHeaderSize + offset + 2))
		if size < uint16(headerSize) {
			return false, nil, errors.New("malformed ACE size")
		}
		offset += uintptr(size)
	}
	header := (*aceHeader)(unsafe.Pointer(uintptr(base) + aclHeaderSize + offset))
	if header.aceSize < uint16(headerSize) {
		return false, nil, errors.New("malformed ACE size")
	}
	if header.aceType != accessAllowedAceType || header.aceFlags&inheritOnlyAceFlag != 0 {
		return false, nil, nil
	}
	sid := (*windows.SID)(unsafe.Pointer(uintptr(base) + aclHeaderSize + offset + headerSize + maskSize))
	return true, sid, nil
}
