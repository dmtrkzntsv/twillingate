//go:build !unix

package native

import "os"

// fileIdentity is unknown here: the log cache is not used.
func fileIdentity(*os.File) (fileID, bool) { return fileID{}, false }

// pathIdentity is unknown here too.
func pathIdentity(string) (fileID, bool) { return fileID{}, false }
