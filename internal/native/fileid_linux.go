//go:build linux

package native

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileIdentity is f's device, inode and birth time (statx; zero where
// the file system keeps none): a file deleted and another made under its
// name may reuse its inode, never its birth.
func fileIdentity(f *os.File) (fileID, bool) {
	rc, err := f.SyscallConn()
	if err != nil {
		return fileID{}, false
	}
	var stx unix.Statx_t
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.Statx(int(fd), "", unix.AT_EMPTY_PATH, unix.STATX_INO|unix.STATX_BTIME, &stx)
	}); err != nil || serr != nil {
		return fileID{}, false
	}
	return statxID(&stx), true
}

// pathIdentity is fileIdentity of the file at path.
func pathIdentity(path string) (fileID, bool) {
	var stx unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_INO|unix.STATX_BTIME, &stx) != nil {
		return fileID{}, false
	}
	return statxID(&stx), true
}

func statxID(stx *unix.Statx_t) fileID {
	id := fileID{dev: uint64(stx.Dev_major)<<32 | uint64(stx.Dev_minor), ino: stx.Ino}
	if stx.Mask&unix.STATX_BTIME != 0 {
		id.birth = stx.Btime.Sec*1e9 + int64(stx.Btime.Nsec)
	}
	return id
}
