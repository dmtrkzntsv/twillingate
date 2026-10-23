//go:build unix

package native

import (
	"os"
	"syscall"
)

// fileIdentity is st's device and inode.
func fileIdentity(st os.FileInfo) (fileID, bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{dev: uint64(s.Dev), ino: uint64(s.Ino)}, true
}
