package store

// Monitor is a project's availability-monitor row (migration 044): the
// settings and the last certificate and domain readings. A project with no
// row reads as the zero Monitor carrying its id.
type Monitor struct {
	ProjectID        int64
	Enabled          bool
	Origin, Contains string
	CertExpiresAt    string // RFC 3339 UTC, "" unknown
	CertIssuer       string
	CertValid        bool
	CertError        string
	CertCheckedAt    string
	DomainExpiresAt  string // YYYY-MM-DD, "" unknown
	DomainCheckedAt  string
	Redirects        int // hops the last answered check followed; 0 on an origin change

	// The last manual check, all cleared on an origin change: when it was
	// requested and finished (RFC 3339 UTC, "" never; pending while requested
	// is the later), and the availability result it finished with.
	CheckRequestedAt, CheckFinishedAt string
	CheckUp                           bool
	CheckStatus                       int
	CheckReason, CheckError           string
	CheckMS                           int
}

// MonitorSettings is what an operator edits.
type MonitorSettings struct {
	Enabled          bool
	Origin, Contains string
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
