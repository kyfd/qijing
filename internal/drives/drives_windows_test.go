//go:build windows

package drives

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyDriveTypes(t *testing.T) {
	tests := []struct {
		value uint32
		want  string
	}{
		{windows.DRIVE_FIXED, TypeFixed},
		{windows.DRIVE_REMOVABLE, TypeRemovable},
		{windows.DRIVE_REMOTE, TypeNetwork},
		{windows.DRIVE_CDROM, TypeCDROM},
		{windows.DRIVE_RAMDISK, TypeRAMDisk},
		{windows.DRIVE_UNKNOWN, TypeUnknown},
		{windows.DRIVE_NO_ROOT_DIR, TypeUnknown},
	}

	for _, test := range tests {
		if got := classify(test.value); got != test.want {
			t.Errorf("classify(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestListReturnsCanonicalDriveRoots(t *testing.T) {
	listed, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, drive := range listed {
		if len(drive.Path) != 3 || drive.Path[0] < 'A' || drive.Path[0] > 'Z' || drive.Path[1:] != `:\` {
			t.Errorf("drive path = %q, want canonical X:\\ root", drive.Path)
		}
		switch drive.Type {
		case TypeFixed, TypeRemovable, TypeNetwork, TypeCDROM, TypeRAMDisk, TypeUnknown:
		default:
			t.Errorf("drive type = %q, want a documented type", drive.Type)
		}
	}
}
