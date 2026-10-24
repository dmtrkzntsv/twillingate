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
	for _, k := range []Kind{KindRefused, KindSQL, KindOutOfMemory, KindOutOfMemoryLoadingLog, KindBusy, KindInternal} {
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
