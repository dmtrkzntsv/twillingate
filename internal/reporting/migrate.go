// The system-definition migrator (D20/D23): keeps components and system
// dashboards in sync with what this release ships, every time it runs.
// Migrate hashes the embedded system directory plus the UI's component
// manifest; when that hash matches what was last recorded (ReportingHash),
// it does nothing — the common case, every boot after the first on a
// given release. Otherwise it parses the manifest, loads the system
// directories, validates every widget's SQL on the event files, and
// hands the result to store.SyncReporting in one transaction. A check
// that could not run for want of time or memory syncs all the same, and
// the next run checks again (ErrUnchecked).
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
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"sort"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/sortkey"
	"github.com/dmtrkzntsv/twillingate/internal/shared/version"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

//go:embed system
var systemFS embed.FS

// ErrUnchecked is Migrate's error when it synced the release's system
// dashboards without checking every widget's SQL on the event files: a
// query ran out of time or memory, or the engine's child died under it
// (resourceErr), which says nothing of the SQL. On an upgrade's first
// start that is most likely the logs the daily pass has not dumped yet,
// which the first query loads whole; failing the start would stop that
// pass from ever running. The widgets after it are checked without a
// query (paramsOnly), and the sync is recorded under a hash no run
// matches, so the next one checks them all again. A plain timeout stops
// the queries too, unlike CheckUserWidgets, which skips only that widget:
// this check holds the start, ingest included, for up to the boot's
// timeout per query, and its SQL is the release's own, tested on a
// shape-only query, so one that runs that long says the engine is short,
// not that the widget is slow. A refusal of the SQL itself still fails
// Migrate: a release whose system SQL is broken must not start.
var ErrUnchecked = errors.New("reporting: the system widgets' SQL was not all checked on the event files")

// uncheckedPrefix marks the hash of a sync recorded with ErrUnchecked:
// it never equals hashSystem's, so the next run syncs and checks again.
const uncheckedPrefix = "unchecked:"

// Migrate syncs this release's embedded system definition (systemFS,
// Manifest()) into st, validating every system widget's SQL on files.
//
// A nil files means there are no event files to check on (a fresh
// install): the widgets are checked without a query (paramsOnly), which
// would spawn an engine child only to run every widget on nothing, and
// the sync is recorded under the unchecked hash, so the first start with
// files checks them all. Unlike ErrUnchecked this is no failure: Migrate
// returns nil.
func Migrate(ctx context.Context, st Store, files Files) error {
	system, err := fs.Sub(systemFS, "system")
	if err != nil {
		return fmt.Errorf("reporting: system fs: %w", err)
	}
	return migrateFrom(ctx, st, files, system, Manifest())
}

// migrateFrom is Migrate's logic against an arbitrary system directory
// and manifest, so a test can exercise it without the embedded release
// definition.
func migrateFrom(ctx context.Context, st Store, files Files, system fs.FS, manifest []byte) error {
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

	fds, err := LoadDashboards(system)
	if err != nil {
		return err
	}
	if err := checkGroups(fds); err != nil {
		return err
	}

	// Deviation 4: validation during migration checks shape only (LIMIT
	// 0), never sample rows — the sql content is the release's own, not
	// an agent's, and a young install may have no rows yet to sample.
	// now is the real clock rather than nil: a widget whose SQL follows
	// :from/:to still needs sample dates bound before the LIMIT 0 query,
	// or the engine refuses a parameter with no argument.
	svc := &Service{st: st, files: files, sources: newSources(files, false, time.Now, nil, nil), now: time.Now}
	var unchecked error
	noFiles := files == nil
	if noFiles {
		svc.sources["sql"] = paramsOnly{svc.sources["sql"]}
	}

	dashboards := make([]store.SystemDashboard, 0, len(fds))
	if len(fds) > 0 {
		dashKeys, err := sortkey.Spread("", "", len(fds))
		if err != nil {
			return fmt.Errorf("reporting: %w", err)
		}
		for i, fd := range fds {
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
					cause := resourceCause(err)
					if cause == nil {
						return fmt.Errorf("reporting: system dashboard %d widget %s: %w", fd.ID, fw.Name, err)
					}
					// The engine's own words: the refusal's advice is for
					// a person's query, not the release's.
					reason := cause.Error()
					if errors.Is(cause, context.DeadlineExceeded) {
						reason = fmt.Sprintf("its query took over %s", files.Timeout())
					}
					unchecked = fmt.Errorf("%w: system dashboard %d widget %s: %s; the next start checks them again",
						ErrUnchecked, fd.ID, fw.Name, reason)
					svc.sources["sql"] = paramsOnly{svc.sources["sql"]}
				}
				widgets = append(widgets, w)
			}
			dashboards = append(dashboards, store.SystemDashboard{
				ID: fd.ID, Title: fd.Title, SortKey: dashKeys[i], Range: fd.Range,
				GroupID: fd.Group, GroupTitle: fd.GroupTitle, Sidebar: fd.Sidebar, ProjectTab: fd.ProjectTab,
				Widgets: widgets,
			})
		}
	}

	rows := make([]store.Component, 0, len(comps))
	for _, c := range comps {
		rows = append(rows, c.row())
	}

	if unchecked != nil || noFiles {
		hash = uncheckedPrefix + hash
	}
	if err := st.SyncReporting(ctx, store.ReportingSync{
		Hash: hash, Version: version.Version,
		Components: rows, Dashboards: dashboards,
	}); err != nil {
		return err
	}
	return unchecked
}

// paramsOnly is the sql source with a Validate that runs no query: the
// check Migrate makes of the widgets after one whose query could not run,
// which still refuses what the scanner refuses.
type paramsOnly struct{ SourceType }

func (paramsOnly) Validate(_ context.Context, content string, _ Component) error {
	_, err := checkSQLParams(content)
	return err
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
// up into a contiguous run of sort keys either, and one whose members
// disagree on sidebar: the sidebar shows groups (spec 2026-10-05 D3).
func checkGroups(files []FileDashboard) error {
	byID := make(map[int64]FileDashboard, len(files))
	for _, fd := range files {
		byID[fd.ID] = fd
	}
	for _, fd := range files {
		if fd.GroupTitle != "" && fd.Group != 0 && fd.Group != fd.ID {
			return fmt.Errorf("reporting: system dashboard %d: group_title belongs on group %d's first dashboard", fd.ID, fd.Group)
		}
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
		if fd.Sidebar != target.Sidebar {
			return fmt.Errorf("reporting: system dashboard %d: sidebar differs from its group's (%d)", fd.ID, fd.Group)
		}
	}

	var seen int64 = -1 // no group id is negative
	closed := make(map[int64]bool, len(files))
	for _, fd := range files {
		g := fd.groupID()
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
