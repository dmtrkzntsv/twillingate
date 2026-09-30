package reporting

import (
	"strings"
	"testing"
	"testing/fstest"
)

func mapFile(content string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(content)}
}

func TestLoadDashboardConfigWithNoDataFile(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"a"}]}`),
		"d/a.json":         mapFile(`{"component":"markdown","title":"A"}`),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: a config with no data file")
	}
}

func TestLoadDashboardConfigWithTwoDataFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"a"}]}`),
		"d/a.json":         mapFile(`{"component":"markdown","title":"A"}`),
		"d/a.md":           mapFile("hi"),
		"d/a.sql":          mapFile("select 1"),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: a config with two data files")
	}
}

func TestLoadDashboardDataFileWithNoConfig(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[]}`),
		"d/a.md":           mapFile("hi"),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: a data file with no config")
	}
}

func TestLoadDashboardRefusesStrayFile(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"visitors"}]}`),
		"d/visitors.json":  mapFile(`{"component":"stat","title":"Visitors"}`),
		"d/visitors.sq":    mapFile("select 1 as value"), // typo: .sq, not .sql
	}
	_, err := LoadDashboard(fsys, "d")
	if err == nil {
		t.Fatal("want error: a stray file with an unexpected extension")
	}
	if !strings.Contains(err.Error(), "unexpected file") || !strings.Contains(err.Error(), "visitors.sq") {
		t.Errorf("error = %q, want it to name the stray file", err)
	}
}

func TestLoadDashboardLayoutNamesMissingWidget(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"missing"}]}`),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: layout names a missing widget")
	}
}

func TestLoadDashboardLayoutNamesWidgetTwice(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"a"},{"widget":"a"}]}`),
		"d/a.json":         mapFile(`{"component":"markdown","title":"A"}`),
		"d/a.md":           mapFile("hi"),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: layout names a widget twice")
	}
}

func TestLoadDashboardWidgetLeftOutOfLayout(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[]}`),
		"d/a.json":         mapFile(`{"component":"markdown","title":"A"}`),
		"d/a.md":           mapFile("hi"),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: a widget the layout leaves out")
	}
}

func TestLoadDashboardBrokenJSONNamesFile(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,`),
	}
	_, err := LoadDashboard(fsys, "d")
	if err == nil {
		t.Fatal("want error: broken JSON")
	}
	if !strings.Contains(err.Error(), "dashboard.json") {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

func TestLoadDashboardBrokenWidgetJSONNamesFile(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"a"}]}`),
		"d/a.json":         mapFile(`{"component":`),
		"d/a.md":           mapFile("hi"),
	}
	_, err := LoadDashboard(fsys, "d")
	if err == nil {
		t.Fatal("want error: broken JSON")
	}
	if !strings.Contains(err.Error(), "a.json") {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

func TestLoadDashboardUnknownKeyRefused(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[],"bogus":true}`),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: unknown key in dashboard.json")
	}
}

func TestLoadDashboardUnknownWidgetKeyRefused(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[{"widget":"a"}]}`),
		"d/a.json":         mapFile(`{"component":"markdown","title":"A","bogus":true}`),
		"d/a.md":           mapFile("hi"),
	}
	if _, err := LoadDashboard(fsys, "d"); err == nil {
		t.Fatal("want error: unknown key in widget config")
	}
}

func TestLoadDashboardHappyPath(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":5,"title":"D","range":"7d",
			"layout":[{"widget":"a","width":4,"height":3},{"widget":"b"}]}`),
		"d/a.json": mapFile(`{"component":"stat","title":"A","props":{"format":"number"}}`),
		"d/a.sql":  mapFile("select 1 as value"),
		"d/b.json": mapFile(`{"component":"markdown","title":"B"}`),
		"d/b.md":   mapFile("hello"),
	}
	fd, err := LoadDashboard(fsys, "d")
	if err != nil {
		t.Fatal(err)
	}
	if fd.ID != 5 || fd.Title != "D" || fd.Range != "7d" {
		t.Errorf("dashboard = %+v", fd)
	}
	if len(fd.Widgets) != 2 {
		t.Fatalf("widgets = %d, want 2", len(fd.Widgets))
	}
	a := fd.Widgets[0]
	if a.Name != "a" || a.Component != "stat" || a.Title != "A" || a.Width != 4 || a.Height != 3 {
		t.Errorf("widget a = %+v", a)
	}
	if a.SourceType != "sql" || a.Source != "select 1 as value" {
		t.Errorf("widget a source = %+v", a)
	}
	if string(a.Props) != `{"format":"number"}` {
		t.Errorf("widget a props = %s", a.Props)
	}
	b := fd.Widgets[1]
	if b.Name != "b" || b.SourceType != "md" || b.Source != "hello" {
		t.Errorf("widget b = %+v", b)
	}
	if b.Width != 0 || b.Height != 0 {
		t.Errorf("widget b size = %dx%d, want 0x0 (component default)", b.Width, b.Height)
	}
}

func TestLoadDashboardParsesGroup(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":2,"title":"D","range":"7d","group":1,"layout":[]}`),
	}
	fd, err := LoadDashboard(fsys, "d")
	if err != nil {
		t.Fatal(err)
	}
	if fd.Group != 1 {
		t.Errorf("Group = %d, want 1", fd.Group)
	}
}

func TestLoadDashboardGroupDefaultsToZero(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"id":1,"title":"D","range":"7d","layout":[]}`),
	}
	fd, err := LoadDashboard(fsys, "d")
	if err != nil {
		t.Fatal(err)
	}
	if fd.Group != 0 {
		t.Errorf("Group = %d, want 0 (no \"group\" key)", fd.Group)
	}
}

func TestLoadDashboardAcceptsNoID(t *testing.T) {
	fsys := fstest.MapFS{
		"d/dashboard.json": mapFile(`{"title":"D","range":"7d","layout":[]}`),
	}
	fd, err := LoadDashboard(fsys, "d")
	if err != nil {
		t.Fatal(err)
	}
	if fd.ID != 0 {
		t.Errorf("ID = %d, want 0", fd.ID)
	}
}

func TestLoadDashboardsSortsByID(t *testing.T) {
	fsys := fstest.MapFS{
		"b/dashboard.json": mapFile(`{"id":2,"title":"B","range":"7d","layout":[]}`),
		"a/dashboard.json": mapFile(`{"id":1,"title":"A","range":"7d","layout":[]}`),
	}
	fds, err := LoadDashboards(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 2 || fds[0].ID != 1 || fds[1].ID != 2 {
		t.Fatalf("LoadDashboards order = %+v", fds)
	}
}

func TestLoadDashboardsRefusesDuplicateID(t *testing.T) {
	fsys := fstest.MapFS{
		"a/dashboard.json": mapFile(`{"id":1,"title":"A","range":"7d","layout":[]}`),
		"b/dashboard.json": mapFile(`{"id":1,"title":"B","range":"7d","layout":[]}`),
	}
	if _, err := LoadDashboards(fsys); err == nil {
		t.Fatal("want error: duplicate id")
	}
}

func TestLoadDashboardsRefusesIDOutsideRange(t *testing.T) {
	fsys := fstest.MapFS{
		"a/dashboard.json": mapFile(`{"id":1000,"title":"A","range":"7d","layout":[]}`),
	}
	if _, err := LoadDashboards(fsys); err == nil {
		t.Fatal("want error: id outside 1-999")
	}
}

func TestLoadDashboardsRefusesNoID(t *testing.T) {
	fsys := fstest.MapFS{
		"a/dashboard.json": mapFile(`{"title":"A","range":"7d","layout":[]}`),
	}
	if _, err := LoadDashboards(fsys); err == nil {
		t.Fatal("want error: id 0 is outside 1-999")
	}
}
