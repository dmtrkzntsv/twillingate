//go:build !unix

package native

import "os"

// fileIdentity is unknown here: the log cache is not used.
func fileIdentity(os.FileInfo) (fileID, bool) { return fileID{}, false }
