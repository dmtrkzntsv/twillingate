package sqlite

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The inverse of encodeID and encodeEventID, which only the tests need: the
// raw_* views decode in SQL.
func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func decodeEventID(v any) string {
	if b, ok := v.([]byte); ok {
		return formatUUID(b)
	}
	return fmt.Sprint(v)
}

func decodeID(v any) string {
	b, ok := v.([]byte)
	if !ok {
		return fmt.Sprint(v)
	}
	if b[0] == tagUUID {
		return formatUUID(b[1:])
	}
	return hex.EncodeToString(b[1:])
}

var idCases = []string{
	"0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5f6", // lowercase UUID
	"0192F1A2-7C3B-7D4E-8F90-A1B2C3D4E5F6", // uppercase UUID: stays text
	"0192f1a27c3b7d4e8f90a1b2c3d4e5f6",     // 32 hex
	"a1b2c3d4e5f6",                         // 12 hex
	"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3",       // 30 hex (15 bytes)
	"abc",                                  // odd length
	"ABCDEF",                               // uppercase hex
	"user@example.com",
	"42",
	"",
	"0192f1a2-7c3b-7d4e-8f90-a1b2c3d4e5fg", // UUID-shaped, not hex
}

func TestEncodeIDRoundTrip(t *testing.T) {
	for _, s := range idCases {
		if got := decodeID(encodeID(s)); got != s {
			t.Errorf("decodeID(encodeID(%q)) = %q", s, got)
		}
		if got := decodeEventID(encodeEventID(s)); got != s {
			t.Errorf("decodeEventID(encodeEventID(%q)) = %q", s, got)
		}
	}
	if b, ok := encodeID(idCases[0]).([]byte); !ok || len(b) != 17 || b[0] != 1 {
		t.Errorf("uuid: got %#v, want 17 bytes tagged 0x01", encodeID(idCases[0]))
	}
	if b, ok := encodeID(idCases[2]).([]byte); !ok || len(b) != 17 || b[0] != 2 {
		t.Errorf("hex: got %#v, want 17 bytes tagged 0x02", encodeID(idCases[2]))
	}
	if b, ok := encodeEventID(idCases[0]).([]byte); !ok || len(b) != 16 {
		t.Errorf("event id: got %#v, want 16 untagged bytes", encodeEventID(idCases[0]))
	}
	for _, s := range []string{idCases[1], idCases[5], idCases[6], idCases[7], "", idCases[10]} {
		if _, ok := encodeID(s).(string); !ok {
			t.Errorf("encodeID(%q) should stay text", s)
		}
	}
}

// TestSQLDecodersMatchGo runs every SQL fragment on Go-encoded values, so
// the views (which carry these fragments) decode what ingest writes.
func TestSQLDecodersMatchGo(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	one := func(expr string, arg any) string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT `+expr+` FROM (SELECT ? AS c)`, arg).Scan(&s); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		return s
	}
	for _, s := range idCases {
		if got := one(sqlID("c"), encodeID(s)); got != s {
			t.Errorf("sqlID(%q) = %q", s, got)
		}
		if got := one(sqlEventID("c"), encodeEventID(s)); got != s {
			t.Errorf("sqlEventID(%q) = %q", s, got)
		}
	}
	ts := time.Date(2026, 10, 11, 21, 58, 3, 0, time.UTC)
	if got := one(sqlTS("c"), unixSeconds(ts)); got != "2026-10-11T21:58:03Z" {
		t.Errorf("sqlTS = %q", got)
	}
	if got := one(sqlReceivedAt("c"), int64(0)); got != "" {
		t.Errorf("sqlReceivedAt(0) = %q, want empty", got)
	}
	for f, name := range map[store.Family]string{store.FamilyViews: "views", store.FamilyProduct: "product", store.FamilyMeasures: "measures"} {
		if got := one(sqlFamily("c"), familyCode(f)); got != name {
			t.Errorf("sqlFamily(%d) = %q", familyCode(f), got)
		}
	}
}
