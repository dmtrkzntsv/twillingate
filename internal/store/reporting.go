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
// stored selection; zero values mean none stored.
type Dashboard struct {
	ID                          int64
	Owner, Title, SortKey       string
	LastProjectID               int64
	LastRange, LastFrom, LastTo string
	CreatedAt, UpdatedAt        string
	ArchivedAt                  string // "" = live
	LiveWidgets                 int    // filled by reads: widgets not archived
}

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

// SystemDashboard is one system dashboard a release migrates in.
// Range is the starting selection: SyncReporting writes it only when the
// dashboard is first inserted, so a viewer's later SetDashboardView
// survives a resync.
type SystemDashboard struct {
	ID             int64
	Title, SortKey string
	Range          string   // the starting selection; written on insert only
	Widgets        []Widget // Name identifies the row; SortKey, sizes and content as in the files
}
