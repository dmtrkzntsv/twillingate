// The system-definition migrator (D20/D23): keeps components and system
// dashboards in sync with what this release ships, every time it runs.
// Migrate hashes the embedded system directory plus the UI's component
// manifest; when that hash matches what was last recorded (ReportingHash),
// it does nothing — the common case, every boot after the first on a
// given release. Otherwise it parses the manifest, loads the system
// directories, validates every widget against a read-only handle on the
// database, and hands the result to store.SyncReporting in one
// transaction.
//
// Unlike create_dashboard (which defaults an omitted range to "7d"), a
// system dashboard.json must give one explicitly: the release is
// choosing the starting selection for every install, not leaving it to a
// runtime default that could silently change underneath it.
package reporting

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io/fs"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/shared/version"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

//go:embed system
var systemFS embed.FS

// Migrate syncs this release's embedded system definition (systemFS,
// Manifest()) into st, using db to validate every system widget's SQL.
func Migrate(ctx context.Context, st Store, db *readsql.DB) error {
	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		return fmt.Errorf("reporting: system fs: %w", err)
	}
	return migrateFrom(ctx, st, db, system, Manifest())
}

// migrateFrom is Migrate's logic against an arbitrary system directory
// and manifest, so a test can exercise it without the embedded release
// definition.
func migrateFrom(ctx context.Context, st Store, db *readsql.DB, system fs.FS, manifest []byte) error {
	hash, err := hashSystem(system, manifest)
	if err != nil {
		return err
	}
	current, err := st.ReportingHash(ctx)
	if err != nil {
		return err
	}
	if current == hash {
		return nil
	}

	comps, err := ParseManifest(manifest)
	if err != nil {
		return err
	}
	compsByName := make(map[string]Component, len(comps))
	for _, c := range comps {
		compsByName[c.Name] = c
	}

	files, err := LoadDashboards(system)
	if err != nil {
		return err
	}
	if err := checkGroups(files); err != nil {
		return err
	}

	// Deviation 4: validation during migration checks shape only (LIMIT
	// 0), never sample rows — the sql content is the release's own, not
	// an agent's, and a young install may have no rows yet to sample.
	// now is the real clock rather than nil: a widget whose SQL follows
	// :from/:to still needs sample dates bound before the LIMIT 0 query,
	// or SQLite errors "missing named argument".
	svc := &Service{st: st, db: db, sources: newSources(db, false, time.Now), now: time.Now}

	dashboards := make([]store.SystemDashboard, 0, len(files))
	if len(files) > 0 {
		dashKeys, err := sortkey.Spread("", "", len(files))
		if err != nil {
			return fmt.Errorf("reporting: %w", err)
		}
		for i, fd := range files {
			if fd.Range == "" {
				return fmt.Errorf("reporting: system dashboard %d: dashboard.json needs range", fd.ID)
			}
			if err := checkPreset(fd.Range); err != nil {
				return fmt.Errorf("reporting: system dashboard %d: %w", fd.ID, err)
			}
			if fd.Range == "custom" {
				return fmt.Errorf("reporting: system dashboard %d: range must be a fixed preset, not custom", fd.ID)
			}

			widgetKeys, err := sortkey.Spread("", "", len(fd.Widgets))
			if err != nil {
				return fmt.Errorf("reporting: system dashboard %d: %w", fd.ID, err)
			}
			widgets := make([]store.Widget, 0, len(fd.Widgets))
			for j, fw := range fd.Widgets {
				w := storeWidget(fw, widgetKeys[j], compsByName)
				if err := svc.validateWidget(ctx, compsByName, w); err != nil {
					return fmt.Errorf("reporting: system dashboard %d widget %s: %w", fd.ID, fw.Name, err)
				}
				widgets = append(widgets, w)
			}
			dashboards = append(dashboards, store.SystemDashboard{
				ID: fd.ID, Title: fd.Title, SortKey: dashKeys[i], Range: fd.Range,
				GroupID: fd.Group, Widgets: widgets,
			})
		}
	}

	rows := make([]store.Component, 0, len(comps))
	for _, c := range comps {
		rows = append(rows, c.row())
	}

	return st.SyncReporting(ctx, store.ReportingSync{
		Hash: hash, Version: version.Version,
		Components: rows, Dashboards: dashboards,
	})
}

// storeWidget converts fw to the store row Migrate writes, defaulting a
// size left at 0 (the file gave none) to comps' component's default —
// the same rule buildWidgets applies for an agent-built widget. A
// component unknown to comps is left unsized; validateWidget refuses it
// with a clearer message than an out-of-range size would.
func storeWidget(fw FileWidget, sortKey string, comps map[string]Component) store.Widget {
	width, height := fw.Width, fw.Height
	if c, ok := comps[fw.Component]; ok {
		if width == 0 {
			width = c.DefaultWidth
		}
		if height == 0 {
			height = c.DefaultHeight
		}
	}
	props := fw.Props
	if len(props) == 0 {
		props = json.RawMessage("{}")
	}
	return store.Widget{
		Component: fw.Component, SortKey: sortKey, Width: width, Height: height,
		Name: fw.Name, Title: fw.Title, Props: string(props),
		SourceType: fw.SourceType, Source: fw.Source,
	}
}

// checkGroups refuses a release whose "group" fields (D16) don't hold
// together: a file naming a group (fd.Group != 0) must name a dashboard
// in the same release, and that dashboard must not itself be in another
// group (a group names its first dashboard, which cannot be a tab of
// something else). It also refuses a release whose groups are not
// contiguous in files' order (LoadDashboards' id order, the same order
// sortkey.Spread assigns keys in), since a gap there could never close
// up into a contiguous run of sort keys either.
func checkGroups(files []FileDashboard) error {
	byID := make(map[int64]FileDashboard, len(files))
	for _, fd := range files {
		byID[fd.ID] = fd
	}
	for _, fd := range files {
		if fd.Group == 0 {
			continue
		}
		target, ok := byID[fd.Group]
		if !ok {
			return fmt.Errorf("reporting: system dashboard %d: group %d is not a dashboard in this release", fd.ID, fd.Group)
		}
		if target.Group != 0 && target.Group != target.ID {
			return fmt.Errorf("reporting: system dashboard %d: group %d is itself in another group", fd.ID, fd.Group)
		}
	}

	var seen int64 = -1 // no group id is negative
	closed := make(map[int64]bool, len(files))
	for _, fd := range files {
		g := fd.Group
		if g == 0 {
			g = fd.ID
		}
		if g == seen {
			continue
		}
		if closed[g] {
			return fmt.Errorf("reporting: system dashboard %d: group %d is not contiguous", fd.ID, g)
		}
		if seen != -1 {
			closed[seen] = true
		}
		seen = g
	}
	return nil
}

// writeFramed writes b's length as a big-endian uint64 before b itself,
// so concatenating several framed writes can never be reproduced by a
// different split of the same total bytes: hashSystem writes a file's
// path and content, and the manifest, each through this rather than back
// to back, so ("a","bx") and ("ab","x") no longer hash the same.
func writeFramed(h hash.Hash, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	h.Write(size[:])
	h.Write(b)
}

// hashSystem hashes every file path and content under system (sorted by
// path) plus manifest, each length-framed (writeFramed) so no path/content
// split can collide with a different one, hex-encoded: the same bytes on
// two runs mean nothing to sync.
func hashSystem(system fs.FS, manifest []byte) (string, error) {
	type file struct {
		path    string
		content []byte
	}
	var files []file
	err := fs.WalkDir(system, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(system, p)
		if err != nil {
			return err
		}
		files = append(files, file{p, b})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("reporting: hash system: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	h := sha256.New()
	for _, f := range files {
		writeFramed(h, []byte(f.path))
		writeFramed(h, f.content)
	}
	writeFramed(h, manifest)
	return hex.EncodeToString(h.Sum(nil)), nil
}
