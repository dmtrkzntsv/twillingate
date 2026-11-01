package store

// Monitor is one origin's availability-monitor row (migration 044): its
// settings and its last certificate reading. An origin with no row reads as a
// Monitor that is off.
type Monitor struct {
	ProjectID     int64
	Origin        string
	Enabled       bool
	Contains      string
	CertExpiresAt string // RFC 3339 UTC, "" unknown
	CertIssuer    string
	CertValid     bool
	CertError     string
	CertCheckedAt string
	Redirects     int // hops the last answered check followed

	// The last manual check: when it was requested and finished (RFC 3339 UTC,
	// "" never; pending while requested is the later), and the availability
	// result it finished with.
	CheckRequestedAt, CheckFinishedAt string
	CheckUp                           bool
	CheckStatus                       int
	CheckReason, CheckError           string
	CheckMS                           int
}

// MonitorSettings is what an operator edits on a monitor.
type MonitorSettings struct {
	Enabled  bool
	Contains string
}

// MonitorDomain is the last registration-expiry reading of a main domain, which
// a project's monitors on that domain share.
type MonitorDomain struct {
	ProjectID int64
	Domain    string
	ExpiresAt string // YYYY-MM-DD, "" unknown
	CheckedAt string
}

// MonitorCheckResult is how a manual check finished.
type MonitorCheckResult struct {
	FinishedAt    string
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
