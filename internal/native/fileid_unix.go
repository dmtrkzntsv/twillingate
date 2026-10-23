//go:build unix && !linux

package native

import (
	"os"
	"syscall"
)

// fileIdentity is f's device and inode.
func fileIdentity(f *os.File) (fileID, bool) {
	st, err := f.Stat()
	if err != nil {
		return fileID{}, false
	}
	return statID(st)
}

// pathIdentity is fileIdentity of the file at path.
func pathIdentity(path string) (fileID, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return fileID{}, false
	}
	return statID(st)
}

func statID(st os.FileInfo) (fileID, bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{dev: uint64(s.Dev), ino: uint64(s.Ino)}, true
}
