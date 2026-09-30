// The system directory format a release ships (D20): one directory per
// dashboard, holding dashboard.json (its title, starting range and
// widget layout) plus one $name.json/$name.(sql|md) pair per widget the
// layout places. LoadDashboard reads one such directory; LoadDashboards
// reads every one under a filesystem root and orders them by id, for
// migrate.go to hash and sync.
package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// FileWidget is one widget as the system directory format describes it:
// a $name.json config (component, title, props) paired with a $name.sql
// or $name.md data file, whose extension names the source type, placed
// by dashboard.json's layout.
type FileWidget struct {
	Name, Component, Title string
	Props                  json.RawMessage
	SourceType, Source     string
	Width, Height          int // 0 = the component's default
}

// FileDashboard is one directory under a system definition: its
// dashboard.json plus the widgets its layout places, in layout order.
type FileDashboard struct {
	ID           int64 // 0 when the file gives none (reporting dev only)
	Title, Range string
	Group        int64        // the group this dashboard is a tab of; 0 = its own id (D16)
	Widgets      []FileWidget // layout order
}

// groupID resolves Group: 0 (the file names no group) means this
// dashboard is its own, so its group is its own id.
func (fd FileDashboard) groupID() int64 {
	if fd.Group == 0 {
		return fd.ID
	}
	return fd.Group
}

// fileDashboardDoc is dashboard.json's own shape.
type fileDashboardDoc struct {
	ID     int64            `json:"id"`
	Title  string           `json:"title"`
	Range  string           `json:"range"`
	Group  int64            `json:"group"`
	Layout []fileLayoutItem `json:"layout"`
}

// fileLayoutItem is one entry of dashboard.json's layout: which widget,
// and its size (0 = the component's default).
type fileLayoutItem struct {
	Widget string `json:"widget"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// fileWidgetDoc is a widget's $name.json shape.
type fileWidgetDoc struct {
	Component string          `json:"component"`
	Title     string          `json:"title"`
	Props     json.RawMessage `json:"props"`
}

// widgetBuild collects the (at most one config, at most one data) files
// found for one widget base name, before LoadDashboard has read either.
type widgetBuild struct {
	configPath, dataPath, sourceType string
}

// decodeStrict unmarshals b into v, refusing unknown keys and naming
// name (the path a caller sees in the error) on any failure.
func decodeStrict(name string, b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("reporting: %s: %w", name, err)
	}
	return nil
}

// LoadDashboard reads one system directory: dashboard.json plus the
// $name.json/$name.(sql|md) pair for every widget its layout names. Any
// other file in the directory — an extension other than json/sql/md,
// most often a typo like visitors.sq — is refused rather than silently
// ignored. LoadDashboard does not check dashboard.json's id
// (LoadDashboards does, for a release's system directory; reporting dev
// reads directly and accepts no id at all).
func LoadDashboard(fsys fs.FS, dir string) (FileDashboard, error) {
	dashPath := path.Join(dir, "dashboard.json")
	dashBytes, err := fs.ReadFile(fsys, dashPath)
	if err != nil {
		return FileDashboard{}, fmt.Errorf("reporting: %s: %w", dashPath, err)
	}
	var doc fileDashboardDoc
	if err := decodeStrict(dashPath, dashBytes, &doc); err != nil {
		return FileDashboard{}, err
	}

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return FileDashboard{}, fmt.Errorf("reporting: %s: %w", dir, err)
	}

	builds := map[string]*widgetBuild{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "dashboard.json" {
			continue
		}
		ext := strings.TrimPrefix(path.Ext(name), ".")
		base := strings.TrimSuffix(name, path.Ext(name))
		if ext != "json" && ext != "sql" && ext != "md" {
			return FileDashboard{}, fmt.Errorf("reporting: %s: unexpected file %s", dir, name)
		}
		b := builds[base]
		if b == nil {
			b = &widgetBuild{}
			builds[base] = b
		}
		p := path.Join(dir, name)
		if ext == "json" {
			if b.configPath != "" {
				return FileDashboard{}, fmt.Errorf("reporting: %s: widget %q has two config files", dir, base)
			}
			b.configPath = p
			continue
		}
		if b.dataPath != "" {
			return FileDashboard{}, fmt.Errorf("reporting: %s: widget %q has two data files", dir, base)
		}
		b.dataPath, b.sourceType = p, ext
	}

	byName := make(map[string]FileWidget, len(builds))
	for name, b := range builds {
		switch {
		case b.configPath == "":
			return FileDashboard{}, fmt.Errorf("reporting: %s: widget %q has a data file but no config", dir, name)
		case b.dataPath == "":
			return FileDashboard{}, fmt.Errorf("reporting: %s: widget %q has a config but no data file", dir, name)
		}
		configBytes, err := fs.ReadFile(fsys, b.configPath)
		if err != nil {
			return FileDashboard{}, fmt.Errorf("reporting: %s: %w", b.configPath, err)
		}
		var wdoc fileWidgetDoc
		if err := decodeStrict(b.configPath, configBytes, &wdoc); err != nil {
			return FileDashboard{}, err
		}
		content, err := fs.ReadFile(fsys, b.dataPath)
		if err != nil {
			return FileDashboard{}, fmt.Errorf("reporting: %s: %w", b.dataPath, err)
		}
		byName[name] = FileWidget{
			Name: name, Component: wdoc.Component, Title: wdoc.Title, Props: wdoc.Props,
			SourceType: b.sourceType, Source: string(content),
		}
	}

	seen := make(map[string]bool, len(doc.Layout))
	widgets := make([]FileWidget, 0, len(doc.Layout))
	for _, item := range doc.Layout {
		if seen[item.Widget] {
			return FileDashboard{}, fmt.Errorf("reporting: %s: layout names %q twice", dashPath, item.Widget)
		}
		seen[item.Widget] = true
		w, ok := byName[item.Widget]
		if !ok {
			return FileDashboard{}, fmt.Errorf("reporting: %s: layout names %q, which has no widget files", dashPath, item.Widget)
		}
		w.Width, w.Height = item.Width, item.Height
		widgets = append(widgets, w)
	}
	for name := range byName {
		if !seen[name] {
			return FileDashboard{}, fmt.Errorf("reporting: %s: widget %q is not in dashboard.json's layout", dir, name)
		}
	}

	return FileDashboard{ID: doc.ID, Title: doc.Title, Range: doc.Range, Group: doc.Group, Widgets: widgets}, nil
}

// LoadDashboards loads every top-level directory in fsys as a
// FileDashboard, sorted by id, refusing a duplicate id or one outside
// 1-999 — the range reserved for system dashboards (store.OwnerSystem;
// an agent-made dashboard starts at 1001).
func LoadDashboards(fsys fs.FS) ([]FileDashboard, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reporting: system: %w", err)
	}
	seen := make(map[int64]bool, len(entries))
	out := make([]FileDashboard, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fd, err := LoadDashboard(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		if fd.ID < 1 || fd.ID > 999 {
			return nil, fmt.Errorf("reporting: %s: id %d is outside 1-999", e.Name(), fd.ID)
		}
		if seen[fd.ID] {
			return nil, fmt.Errorf("reporting: %s: id %d is already used by another system dashboard", e.Name(), fd.ID)
		}
		seen[fd.ID] = true
		out = append(out, fd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
