package engine

// request is one line the parent writes to the child's stdin. Cancel
// interrupts the request with the same ID; it gets no reply of its own.
type request struct {
	ID      uint64 `json:"id"`
	SQL     string `json:"sql,omitempty"`
	Exec    bool   `json:"exec,omitempty"`
	MaxRows int    `json:"max_rows,omitempty"`
	Cancel  bool   `json:"cancel,omitempty"`
}

// reply is one line the child writes to stdout: first {"ready":true} (or
// {"err":…} and the child exits), then one per request, in the order the
// requests finish. Numbers travel as JSON numbers and are decoded with
// UseNumber, so an int64 past 2^53 arrives whole.
type reply struct {
	ID        uint64   `json:"id,omitempty"`
	Ready     bool     `json:"ready,omitempty"`
	Columns   []string `json:"columns,omitempty"`
	Rows      [][]any  `json:"rows,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Err       string   `json:"err,omitempty"`
}
