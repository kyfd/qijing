//go:build windows

package appdir

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// restrictToCurrentUser must make the directory private regardless of what
// the parent directory inherits: the test deliberately runs under t.TempDir,
// whose inherited ACLs vary between machines and may include broad grants.
func TestRestrictToCurrentUserMakesDirPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := restrictToCurrentUser(dir); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	if err := verifyUserPrivate(dir); err != nil {
		t.Fatalf("restricted dir must be user-private: %v", err)
	}
}

// A DACL that grants Everyone access must be rejected: it would expose the
// scan index beyond the current user.
func TestVerifyUserPrivateRejectsEveryoneGrant(t *testing.T) {
	dir := t.TempDir()
	applyDACL(t, dir, "D:P(A;;FA;;;WD)")
	err := verifyUserPrivate(dir)
	if err == nil {
		t.Fatal("an Everyone grant must be rejected")
	}
	if errors.Is(err, errACLUnavailable) {
		t.Fatalf("expected a policy rejection, got unavailable: %v", err)
	}
}

// The restriction must survive on a directory that previously held broad
// grants: the protected DACL replaces them.
func TestRestrictReplacesBroadInheritedGrants(t *testing.T) {
	dir := t.TempDir()
	if err := verifyUserPrivate(dir); err == nil {
		t.Skip("environment already restricts the temp dir; nothing to replace")
	}
	if err := restrictToCurrentUser(dir); err != nil {
		t.Fatalf("restrict: %v", err)
	}
	if err := verifyUserPrivate(dir); err != nil {
		t.Fatalf("restriction must remove broad grants: %v", err)
	}
}

func TestChildInheritsRestriction(t *testing.T) {
	dir := t.TempDir()
	if err := restrictToCurrentUser(dir); err != nil {
		t.Fatal(err)
	}
	// Files created below the root must carry the restricted DACL too,
	// without per-file work.
	child := dir + `\ecosystem.db`
	if err := os.WriteFile(child, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyUserPrivate(child); err != nil {
		t.Fatalf("child file must inherit the restricted DACL: %v", err)
	}
}

func applyDACL(t *testing.T, dir, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Skipf("cannot build test DACL: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Skipf("cannot read test DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Skipf("cannot apply test DACL: %v", err)
	}
}
