package store

// Monitor is one origin's availability-monitor row (migration 044): its
// settings and its last certificate reading. An origin with no row reads as a
// Monitor that is off.
type Monitor struct {
	ProjectID int64
	Origin    string
	Enabled   bool
	// Expect is what the check expects of the site beyond a 2xx answer.
	Expect        MonitorExpect
	CertExpiresAt string // RFC 3339 UTC, "" unknown
	CertIssuer    string
	CertValid     bool
	CertError     string
	CertCheckedAt string
	Redirects     int // hops the last answered check followed

	// The last manual check: when it was requested and finished (RFC 3339 UTC,
	// "" never; pending while requested is the later).
	CheckRequestedAt, CheckFinishedAt string

	// The latest availability check of any kind, scheduled or manual.
	Last MonitorResult
}

// MonitorExpect is what an availability check expects of the site beyond a
// 2xx answer (migration 049 keeps it as one JSON object). Each field is one
// expectation, its zero value none: a new one is a field here, its
// validation in manage and its step in monitor's check, with no migration.
type MonitorExpect struct {
	// Contains is text the page must contain (case-sensitive).
	Contains string `json:"contains,omitempty"`
	// RedirectTo is the origin the redirects must end at, one of the
	// project's other exact origins.
	RedirectTo string `json:"redirect_to,omitempty"`
}

// MonitorSettings is what an operator edits on a monitor: nil for what to
// leave as the row has it. The store applies them to the row inside its
// transaction, so two edits of different fields cannot overwrite each other.
type MonitorSettings struct {
	Enabled *bool
	// Expect edits the expectations: given the row's, it returns them with
	// the edit applied, the fields it does not name kept.
	Expect func(MonitorExpect) MonitorExpect
}

// MonitorDomain is the last registration-expiry reading of a main domain, which
// a project's monitors on that domain share.
type MonitorDomain struct {
	ProjectID int64
	Domain    string
	ExpiresAt string // YYYY-MM-DD, "" unknown
	CheckedAt string
}

// MonitorResult is how an availability check went.
type MonitorResult struct {
	CheckedAt     string // RFC 3339 UTC, "" never
	Up            bool
	Status        int
	Reason, Error string
	MS            int
}

// CertState is one certificate reading.
type CertState struct {
	ExpiresAt, Issuer, Error, CheckedAt string
	Valid                               bool
}

// DomainState is one domain-expiry reading.
type DomainState struct{ ExpiresAt, CheckedAt string }

// CheckPending reports whether a manual check was requested and has not
// finished: requested, and finished empty (the request clears it) or older than
// the request.
func (m Monitor) CheckPending() bool {
	return m.CheckRequestedAt != "" && (m.CheckFinishedAt == "" || m.CheckFinishedAt < m.CheckRequestedAt)
}

// CheckNotRun reports whether the last manual check ended without an availability
// check at or after its request: the server was down, stopping, or the monitor
// went off. A result in the request's own second counts as the check's.
func (m Monitor) CheckNotRun() bool {
	return m.CheckRequestedAt != "" && !m.CheckPending() && m.Last.CheckedAt < m.CheckRequestedAt
}
