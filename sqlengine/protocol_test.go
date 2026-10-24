package sqlengine

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(b, "\n")
}

// roundTrip decodes a fixture strictly into v and encodes it again: the
// bytes must match, so the types carry every field and no other.
func roundTrip(t *testing.T, name string, v any) {
	t.Helper()
	in := fixture(t, name)
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Errorf("%s does not round-trip:\n in: %s\nout: %s", name, in, out)
	}
}

func TestFixturesRoundTrip(t *testing.T) {
	roundTrip(t, "request.json", &Request{})
	roundTrip(t, "request_all_projects.json", &Request{})
	roundTrip(t, "response.json", &Result{})
	roundTrip(t, "response_page.json", &Result{})
	roundTrip(t, "response_count.json", &Result{})
	for _, k := range []Kind{KindRefused, KindSQL, KindOutOfMemory, KindOutOfMemoryLoadingLog, KindBusy, KindTimeout, KindExited, KindInternal} {
		var eb ErrorBody
		roundTrip(t, "error_"+string(k)+".json", &eb)
		if eb.Error.Kind != k {
			t.Errorf("error_%s.json has kind %q", k, eb.Error.Kind)
		}
	}
}

func TestRequestFixtureFields(t *testing.T) {
	var r Request
	if err := json.Unmarshal(fixture(t, "request.json"), &r); err != nil {
		t.Fatal(err)
	}
	if r.Scope == nil || *r.Scope != 12 || !r.Confine || r.Mode != ModePage || r.Page == nil || r.Page.Sort == nil || !r.Page.Sort.Desc {
		t.Errorf("request fixture decoded wrong: %+v", r)
	}
	if r.Tables.Version != TablesVersion || len(r.Tables.Rules.Events) != 2 {
		t.Errorf("tables decoded wrong: %+v", r.Tables)
	}
	var all Request
	if err := json.Unmarshal(fixture(t, "request_all_projects.json"), &all); err != nil {
		t.Fatal(err)
	}
	if all.Scope != nil || all.Page != nil {
		t.Errorf("all-projects fixture: scope %v page %v", all.Scope, all.Page)
	}
}

// The package must stay importable by another module: standard library only.
func TestStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range strings.Fields(string(out)) {
		if first, _, _ := strings.Cut(imp, "/"); strings.Contains(first, ".") {
			t.Errorf("imports %s, which is not the standard library", imp)
		}
	}
}

// TestModeResponseFixtures: the page fixture ends in the two counts on
// every row, the count fixture is total and matched in one row.
func TestModeResponseFixtures(t *testing.T) {
	var page Result
	if err := json.Unmarshal(fixture(t, "response_page.json"), &page); err != nil {
		t.Fatal(err)
	}
	n := len(page.Columns)
	if n < 3 || page.Columns[n-2] != "__tw_matched" || page.Columns[n-1] != "__tw_total" || len(page.Rows) == 0 {
		t.Errorf("page fixture columns = %v", page.Columns)
	}
	for _, row := range page.Rows {
		if len(row) != n {
			t.Errorf("page fixture row %v has %d cells, want %d", row, len(row), n)
		}
	}
	var count Result
	if err := json.Unmarshal(fixture(t, "response_count.json"), &count); err != nil {
		t.Fatal(err)
	}
	if len(count.Columns) != 2 || count.Columns[0] != "total" || count.Columns[1] != "matched" || len(count.Rows) != 1 || len(count.Rows[0]) != 2 {
		t.Errorf("count fixture = %+v", count)
	}
}

func TestKindStatus(t *testing.T) {
	for k, want := range map[Kind]int{KindRefused: 400, KindSQL: 400, KindBusy: 503, KindTimeout: 504,
		KindOutOfMemory: 500, KindOutOfMemoryLoadingLog: 500, KindExited: 500, KindInternal: 500} {
		if got := k.Status(); got != want {
			t.Errorf("%s.Status() = %d, want %d", k, got, want)
		}
	}
}
