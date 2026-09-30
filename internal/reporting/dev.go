// twillingate reporting dev's preview server (D44): a no-login HTTP
// handler that answers the same JSON envelopes the API's reporting
// routes do, built directly from a set of system-directory-shaped
// directories on disk rather than from the store. There is no store
// here at all — dev dashboards are never saved anywhere, only read,
// validated and served fresh on every request, so an edit to a file
// shows up on the very next call without restarting the process.
package reporting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/shared/readsql"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// DevError is one directory reporting dev could not load, as
// GET /api/dashboards' errors array carries it: the failing directory
// and why, so the other directories can still be shown.
type DevError struct {
	Dir     string `json:"dir"`
	Message string `json:"message"`
}

// firstDevDashboardID is the first id a dashboard.json without an "id"
// gets, in argument order. It matches store.OwnerUser's range (1-999 is
// reserved for a real system dashboard; an agent-made one starts at
// 1001), so a dev dashboard's id never reads as a system one even
// against a database that has real system dashboards migrated into it.
const firstDevDashboardID = 1001

// DevHandler serves twillingate reporting dev: the routes a preview
// needs, without auth, over dirs (each either a dashboard directory —
// one holding dashboard.json — or a parent of several) and db, a
// read-only handle sql widgets run against. The component manifest is
// this build's own (ui/components.json, embedded, same as the real
// server); only dirs is re-read on every request.
func DevHandler(dirs []string, db *readsql.DB) http.Handler {
	svc := &Service{db: db, sources: newSources(db, true, time.Now), now: time.Now}
	comps, err := ParseManifest(Manifest())
	if err != nil {
		// Embedded at build time: broken here means this build is broken,
		// the same condition Manifest() itself already panics on.
		panic("reporting: dev: " + err.Error())
	}
	compsByName := compIndex(comps)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dashboards", devListDashboards(dirs))
	mux.HandleFunc("GET /api/dashboards/{id}", devGetDashboard(dirs, svc, compsByName))
	mux.HandleFunc("GET /api/widgets/{id}/data", devWidgetData(dirs, svc, compsByName))
	mux.HandleFunc("GET /api/components", devComponents(svc, comps))
	mux.HandleFunc("PUT /api/dashboards/{id}/view", devSetView)
	mux.HandleFunc("GET /api/projects", devProjects(db))
	mux.HandleFunc("GET /api/dev/version", devVersion(dirs))
	mux.Handle("/app/", UI())
	// As on the API's listener: the preview's address opens the dashboards.
	mux.Handle("GET /{$}", http.RedirectHandler("/app/", http.StatusFound))
	return loopbackOnly(mux)
}

// loopbackOnly answers 403 unless the request names a loopback host.
// Reporting dev has no login, so a page elsewhere that rebinds its own
// DNS name to 127.0.0.1 could otherwise read it through the victim's
// browser; such a request still carries that other name in Host.
func loopbackOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			msg := fmt.Sprintf("reporting dev answers only on a loopback host, not %q", r.Host)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": msg}})
			return
		}
		h.ServeHTTP(w, r)
	})
}

// compIndex keys comps by name, for validateWidget and storeWidget.
func compIndex(comps []Component) map[string]Component {
	out := make(map[string]Component, len(comps))
	for _, c := range comps {
		out[c.Name] = c
	}
	return out
}

// devDashboardDirs resolves dirs (CLI arguments) to the directories that
// are themselves dashboard directories: an argument that has its own
// dashboard.json is one; otherwise every immediate subdirectory of it
// that has one is. An argument that is neither (nor has any such
// subdirectory) yields nothing from it, silently — reporting dev takes
// whatever it is pointed at, rather than guessing a typo was meant.
func devDashboardDirs(dirs []string) ([]string, []DevError) {
	var out []string
	var errs []DevError
	for _, dir := range dirs {
		if hasDashboardJSON(dir) {
			out = append(out, dir)
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, DevError{Dir: dir, Message: err.Error()})
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if hasDashboardJSON(sub) {
				out = append(out, sub)
			}
		}
	}
	return out, errs
}

func hasDashboardJSON(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "dashboard.json"))
	return err == nil
}

// loadDevDashboards loads every dashboard directory dirs resolves to,
// fresh from disk: a directory whose dashboard.json fails to parse (or
// whose widget files don't match it) is reported in errs rather than
// aborting the rest. A dashboard.json that gives no id is assigned one,
// starting at firstDevDashboardID, in the order its directory was
// encountered (devDashboardDirs' scan order — alphabetical by directory
// name, os.ReadDir's own order). The result is then sorted by id, the
// same rule LoadDashboards uses for the real release: dev has no sort
// key to order by, and ordering by id is the only ordering that matches
// what a group's members will look like once it ships (D17), for both
// devListDashboards and a group's Tabs.
func loadDevDashboards(dirs []string) ([]FileDashboard, []DevError) {
	paths, errs := devDashboardDirs(dirs)
	var out []FileDashboard
	nextID := int64(firstDevDashboardID)
	for _, p := range paths {
		fd, err := LoadDashboard(os.DirFS(p), ".")
		if err != nil {
			errs = append(errs, DevError{Dir: p, Message: err.Error()})
			continue
		}
		if fd.ID == 0 {
			fd.ID = nextID
			nextID++
		}
		out = append(out, fd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, errs
}

func findDevDashboard(fds []FileDashboard, id int64) (FileDashboard, bool) {
	for _, fd := range fds {
		if fd.ID == id {
			return fd, true
		}
	}
	return FileDashboard{}, false
}

// devWidgetRow builds the store.Widget row for fd's widget at index i
// (0-based), with the dashboardID*1000+index(1-based) id the widget data
// route parses back apart.
func devWidgetRow(fd FileDashboard, i int, comps map[string]Component) store.Widget {
	w := storeWidget(fd.Widgets[i], "", comps)
	w.ID = fd.ID*1000 + int64(i+1)
	w.DashboardID = fd.ID
	return w
}

// devDashboardRow is the store.Dashboard dashboardInfo needs, built from
// a loaded file rather than a stored row: dev has no viewer selection to
// echo back (SetDashboardView is a no-op), so Last* stay unset. An id in
// the system range (1–999) is owned by "system", so a system directory
// previews among the report tabs as it will ship. GroupID mirrors D16:
// fd.Group, or fd.ID when the file names none.
func devDashboardRow(fd FileDashboard) store.Dashboard {
	owner := store.OwnerUser
	if fd.ID >= 1 && fd.ID <= 999 {
		owner = store.OwnerSystem
	}
	return store.Dashboard{ID: fd.ID, Owner: owner, GroupID: fd.groupID(), Title: fd.Title, LastRange: fd.Range, LiveWidgets: len(fd.Widgets)}
}

func devListDashboards(dirs []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fds, errs := loadDevDashboards(dirs)
		out := Dashboards{Timezone: "UTC", Dev: true, Dashboards: make([]DashboardInfo, 0, len(fds)), Errors: errs}
		for _, fd := range fds {
			out.Dashboards = append(out.Dashboards, dashboardInfo(devDashboardRow(fd)))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func devGetDashboard(dirs []string, svc *Service, comps map[string]Component) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeAPIErr(w, store.Refuse(store.ErrInvalid, "id must be an integer"))
			return
		}
		fds, _ := loadDevDashboards(dirs)
		fd, ok := findDevDashboard(fds, id)
		if !ok {
			writeAPIErr(w, store.Refuse(store.ErrNotFound, "dashboard %d not found", id))
			return
		}
		row := devDashboardRow(fd)
		// Mirrors Service.Dashboard's rule (read.go): the group's live
		// members, same owner, in list order. In dev mode every loaded
		// file counts as live — there is no archived state to skip.
		tabs := []Tab{}
		for _, x := range fds {
			xRow := devDashboardRow(x)
			if xRow.GroupID == row.GroupID && xRow.Owner == row.Owner {
				tabs = append(tabs, Tab{ID: xRow.ID, Title: xRow.Title})
			}
		}
		widgets := make([]WidgetInfo, 0, len(fd.Widgets))
		var followsProject, followsRange bool
		for i := range fd.Widgets {
			info := svc.widgetInfo(devWidgetRow(fd, i, comps))
			followsProject = followsProject || info.FollowsProject
			followsRange = followsRange || info.FollowsRange
			widgets = append(widgets, info)
		}
		out := DashboardDetail{
			DashboardInfo:  dashboardInfo(row),
			FollowsProject: followsProject, FollowsRange: followsRange,
			Tabs: tabs, Widgets: widgets,
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// devWidgetData answers a widget's data, fresh: unlike the API's own
// WidgetData (cached, against a widget already checked when it was
// saved), a dev widget was never saved, so this validates it — the same
// check a save would have run — before loading it, on every request; the
// content it validates and loads is whatever is on disk right now.
func devWidgetData(dirs []string, svc *Service, comps map[string]Component) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeAPIErr(w, store.Refuse(store.ErrInvalid, "id must be an integer"))
			return
		}
		fds, _ := loadDevDashboards(dirs)
		fd, ok := findDevDashboard(fds, id/1000)
		index := id % 1000
		if !ok || index < 1 || int(index) > len(fd.Widgets) {
			writeAPIErr(w, store.Refuse(store.ErrNotFound, "widget %d not found", id))
			return
		}
		row := devWidgetRow(fd, int(index-1), comps)

		ctx := r.Context()
		if err := svc.validateWidget(ctx, comps, row); err != nil {
			writeAPIErr(w, err)
			return
		}
		comp := comps[row.Component] // present: validateWidget just checked it

		q := r.URL.Query()
		var projectID int64
		if v := q.Get("project_id"); v != "" {
			projectID, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeAPIErr(w, store.Refuse(store.ErrInvalid, "project_id must be an integer"))
				return
			}
		}
		followsProject, followsRange := svc.follows(row)
		in := DataRequest{WidgetID: id, ProjectID: projectID, From: q.Get("from"), To: q.Get("to")}
		params, echo, err := svc.widgetParams(followsProject, followsRange, id, in)
		if err != nil {
			writeAPIErr(w, err)
			return
		}

		src := svc.sources[row.SourceType] // present: validateWidget just checked it
		v, err := src.Load(ctx, row.Source, params)
		if err != nil {
			writeAPIErr(w, wrapLoadErr(err))
			return
		}
		if res, ok := v.(readsql.Result); ok {
			if err := comp.checkRows(res); err != nil {
				writeAPIErr(w, wrapLoadErr(err))
				return
			}
		}
		out := WidgetData{WidgetID: id, SourceType: row.SourceType, ProjectID: echo.ProjectID, From: echo.From, To: echo.To, Data: v}
		writeJSON(w, http.StatusOK, out)
	}
}

func devComponents(svc *Service, comps []Component) http.HandlerFunc {
	out := struct {
		SourceTypes []string    `json:"source_types"`
		Components  []Component `json:"components"`
	}{SourceTypes: svc.SourceTypes(), Components: comps}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, out)
	}
}

// devSetView answers PUT .../view: a no-op (200 "saved") — reporting dev
// keeps no per-viewer selection to store, dashboards being read fresh
// from disk on every request rather than from a row it could update.
func devSetView(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// devProjectOut and devProjectsOut mirror internal/api/ops_read.go's
// projectOut/listProjectsOut — list_projects' JSON shape — field for
// field. reporting dev cannot import internal/api (a surface, above
// reporting in CLAUDE.md's package layout) to reuse its type, so the
// shape is repeated here instead.
type devProjectOut struct {
	ProjectID      int64    `json:"project_id"`
	Name           string   `json:"name"`
	Archived       bool     `json:"archived,omitempty"`
	AllowedOrigins []string `json:"allowed_origins"`
	Attributes     []string `json:"attributes,omitempty"`
}

type devProjectsOut struct {
	Projects []devProjectOut `json:"projects"`
}

// devListProjects lists every project, archived ones flagged, as
// list_projects does, straight off the projects table — trusted Go SQL
// against db, the same way sampleProject (source.go) reads projects and
// events directly, not a widget's checked custom SQL.
func devListProjects(ctx context.Context, db *readsql.DB) (devProjectsOut, error) {
	rows, err := db.Run(ctx,
		`SELECT id, name, allowed_origins, attributes, archived_at IS NOT NULL FROM projects ORDER BY id`)
	if err != nil {
		return devProjectsOut{}, err
	}
	out := devProjectsOut{Projects: []devProjectOut{}}
	for _, row := range rows.Rows {
		id, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return devProjectsOut{}, fmt.Errorf("reporting: dev: project id %q: %w", row[0], err)
		}
		po := devProjectOut{ProjectID: id, Name: row[1], Archived: row[4] == "1", AllowedOrigins: []string{}}
		if row[2] != "" {
			if err := json.Unmarshal([]byte(row[2]), &po.AllowedOrigins); err != nil {
				return devProjectsOut{}, fmt.Errorf("reporting: dev: project %d allowed_origins: %w", id, err)
			}
		}
		if row[3] != "" {
			if err := json.Unmarshal([]byte(row[3]), &po.Attributes); err != nil {
				return devProjectsOut{}, fmt.Errorf("reporting: dev: project %d attributes: %w", id, err)
			}
		}
		out.Projects = append(out.Projects, po)
	}
	return out, nil
}

func devProjects(db *readsql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := devListProjects(r.Context(), db)
		if err != nil {
			writeAPIErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// hashDirs hashes every regular file under dirs (sorted by path), the
// same length-framed way hashSystem (migrate.go) hashes the embedded
// system directory, so the UI's poll of /api/dev/version notices any
// edit under any of them — a changed dashboard.json, a renamed file, a
// single byte inside a .sql — without needing to know which files
// matter to which dashboard.
func hashDirs(dirs []string) (string, error) {
	type file struct {
		path    string
		content []byte
	}
	var files []file
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files = append(files, file{p, b})
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("reporting: dev: %w", err)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	h := sha256.New()
	for _, f := range files {
		writeFramed(h, []byte(f.path))
		writeFramed(h, f.content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func devVersion(dirs []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := hashDirs(dirs)
		if err != nil {
			writeAPIErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"version": v})
	}
}

// writeJSON writes v as the response body, status first.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIErr maps err to the API's own refusal shape (rest.go's
// apiError, repeated here for the same reason devProjectOut is): a typed
// refusal (store.Refuse, matched by errors.Is against the sentinels
// CLAUDE.md names) gets its status and code; anything else is a 500,
// its text kept as-is since reporting dev has no request logger of its
// own to hand internals to instead.
func writeAPIErr(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, store.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid"
	case errors.Is(err, store.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, store.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}
