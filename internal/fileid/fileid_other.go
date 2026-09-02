//go:build !windows

package fileid

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// Identify is the non-Windows development fallback: it reports the inode
// where available plus the observed metadata. The product ships only on
// Windows; this keeps the tree compiling and tests honest about the weaker
// guarantees (Matches treats a zero FileID as "no stable identity").
func Identify(path string) (Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	if !filepathIsAbsForTests(path) {
		return Identity{}, fmt.Errorf("file identity requires an absolute path: %q", path)
	}
	identity := Identity{Size: info.Size(), ModTime: info.ModTime()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.FileID = uint64(stat.Ino)
		identity.CreationTime = time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
	}
	return identity, nil
}

func filepathIsAbsForTests(path string) bool {
	return len(path) > 0 && path[0] == '/'
}
