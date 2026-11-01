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
}

// MonitorSettings is what an operator edits.
type MonitorSettings struct {
	Enabled          bool
	Origin, Contains string
}

// CertState is one certificate reading.
type CertState struct {
	ExpiresAt, Issuer, Error, CheckedAt string
	Valid                               bool
}

// DomainState is one domain-expiry reading.
type DomainState struct{ ExpiresAt, CheckedAt string }
