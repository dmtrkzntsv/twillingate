package store

// Dashboard owners (spec 2026-09-25, migration 021). A system dashboard is
// migrated in by the release on every run; a user dashboard is what an
// agent built. Ids 1-999 are reserved for system dashboards so an
// agent-made one (starting at 1001) can never collide with one.
const (
	OwnerSystem = "system"
	OwnerUser   = "user"
)

// Component is a row of components: a React component's contract, the
// JSON columns kept as the text the manifest carried.
type Component struct {
	Name, Description      string
	Accepts, Inputs, Props string // JSON
	DefaultWidth           int
	DefaultHeight          int
}

// Dashboard is a row of dashboards. The Last* fields are the viewer's
// stored selection; zero values mean none stored. GroupID is the group
// this dashboard is a tab of (migration 022); on insert, 0 means "a new
// group: its own id".
type Dashboard struct {
	ID                          int64
	Owner, Title, SortKey       string
	GroupID                     int64
	GroupTitle                  string // the group's name (migration 031); "" = none, the first live tab's title stands in
	LastProjectID               int64
	LastRange, LastFrom, LastTo string
	CreatedAt, UpdatedAt        string
	ArchivedAt                  string // "" = live
	LiveWidgets                 int    // filled by reads: widgets not archived
	Sidebar                     bool   // in the sidebar (migration 032); a built-in's Hide sets it false
	ProjectTab                  bool   // a new project gets this dashboard as a tab (032 D3)
}

// DashboardKey is one dashboard row's place: which group it belongs to
// and its sort key within its owner's order. MoveDashboards rewrites a
// batch of these in one transaction.
type DashboardKey struct {
	ID, GroupID int64
	SortKey     string
}

// GroupRekey moves a group's name from one group id to another, in the
// same transaction as the rows that change group (spec 2026-10-04 D3):
// order.handOver gives a group a new id when the dashboard whose id it
// used leaves. The zero value moves nothing.
type GroupRekey struct{ From, To int64 }

// Widget is a row of widgets. Component "" is NULL: the component was
// removed from the code (spec D14).
type Widget struct {
	ID, DashboardID      int64
	Component, SortKey   string
	Width, Height        int
	Name, Title, Props   string // Props: a JSON object
	SourceType, Source   string
	CreatedAt, UpdatedAt string
	ArchivedAt           string
}

// ReportingSync is the system state a release carries; SyncReporting
// makes the database match it in one transaction (spec D23).
type ReportingSync struct {
	Hash, Version string
	Components    []Component
	Dashboards    []SystemDashboard // ascending id
}

// PurgeResult is what PurgeArchived deleted, by id: a project's data goes
// with it (DeleteProjectData's table list), a purged dashboard takes its
// widgets by cascade, and a widget can also be purged on its own once it
// (not its dashboard) has aged out.
type PurgeResult struct{ Projects, Dashboards, Widgets []int64 }

// SystemDashboard is one system dashboard a release migrates in. GroupID
// is the group it is a tab of; 0 means its own id, same as Dashboard.
// Range is the starting selection: SyncReporting writes it only when the
// dashboard is first inserted, so a viewer's later SetDashboardView
// survives a resync.
type SystemDashboard struct {
	ID             int64
	Title, SortKey string
	GroupID        int64
	GroupTitle     string   // the group's name from the founding dashboard's fixture; "" = none
	Range          string   // the starting selection; written on insert only
	Widgets        []Widget // Name identifies the row; SortKey, sizes and content as in the files
	Sidebar        bool     // the fixture's "sidebar": written on insert only, then the install's (D6)
	ProjectTab     bool     // the fixture's "project_tab": the release's, re-synced every time (D6)
}
