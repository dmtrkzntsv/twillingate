package archive

import (
	"errors"
	"fmt"
	"os"
)

// Sizes is each project's bytes on disk under dir, by project id: every
// file ListFiles lists for it, its Parquet, its logs and its current
// derived files. A project ListFiles leaves out is absent; the map is
// never nil, so a dir with no project answers an empty one. A file that
// vanishes between the listing and its stat (the pass at work) counts
// nothing.
func Sizes(dir string) (map[int64]int64, error) {
	files, err := ListFiles(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(files))
	for id, f := range files {
		paths := append(append(append([]string{}, f.Parquet...), f.Logs...), f.Sessions...)
		for _, p := range []string{f.Identities, f.Aliases} {
			if p != "" {
				paths = append(paths, p)
			}
		}
		var n int64
		for _, p := range paths {
			st, err := os.Stat(p)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("archive: %w", err)
			}
			n += st.Size()
		}
		out[id] = n
	}
	return out, nil
}
