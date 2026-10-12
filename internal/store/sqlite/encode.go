package sqlite

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The stored forms of events' columns (spec D1–D6). Every encoding
// converts back to exactly the text that was sent; the raw_* views and
// v_events_flat decode with the sql* fragments below, and the migration
// (041) carries its own copy of the same rules, pinned by
// TestMigrationEncodesLikeIngest.

const (
	tagUUID = 0x01 // a lowercase canonical UUID, 16 bytes follow
	tagHex  = 0x02 // non-empty even-length lowercase hex, its bytes follow
)

func familyCode(f store.Family) int64 {
	switch f {
	case store.FamilyViews:
		return 1
	case store.FamilyProduct:
		return 2
	case store.FamilyMeasures:
		return 3
	}
	panic(fmt.Sprintf("familyCode: %q", f))
}

// isLowerHex reports s is non-empty lowercase hex.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// uuidBytes returns the 16 bytes of a lowercase canonical UUID, or nil.
func uuidBytes(s string) []byte {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if !isLowerHex(h) {
		return nil
	}
	b, _ := hex.DecodeString(h)
	return b
}

func encodeEventID(id string) any {
	if b := uuidBytes(id); b != nil {
		return b
	}
	return id
}

func encodeID(s string) any {
	if b := uuidBytes(s); b != nil {
		return append([]byte{tagUUID}, b...)
	}
	if len(s)%2 == 0 && isLowerHex(s) {
		b, _ := hex.DecodeString(s)
		return append([]byte{tagHex}, b...)
	}
	return s
}

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

func unixSeconds(t time.Time) int64 { return t.UTC().Unix() }

// todayUTC is the current day as events.day spells it, for the writes
// outside ingest that raise a dictionary row's last_seen_day (D7) or ask
// which rows a raw row may still carry.
func todayUTC() string { return time.Now().UTC().Format("2006-01-02") }

// sqlUUIDText formats a 32-character lowercase hex expression with dashes.
func sqlUUIDText(h string) string {
	return fmt.Sprintf(`substr(%[1]s, 1, 8) || '-' || substr(%[1]s, 9, 4) || '-' || substr(%[1]s, 13, 4) || '-' || substr(%[1]s, 17, 4) || '-' || substr(%[1]s, 21, 12)`, h)
}

func sqlFamily(col string) string {
	return fmt.Sprintf(`CASE %s WHEN 1 THEN 'views' WHEN 2 THEN 'product' WHEN 3 THEN 'measures' END`, col)
}

func sqlEventID(col string) string {
	return fmt.Sprintf(`CASE WHEN typeof(%[1]s) = 'blob' THEN %[2]s ELSE %[1]s END`,
		col, sqlUUIDText(fmt.Sprintf(`lower(hex(%s))`, col)))
}

func sqlID(col string) string {
	h := fmt.Sprintf(`lower(hex(substr(%s, 2)))`, col)
	return fmt.Sprintf(`CASE WHEN typeof(%[1]s) <> 'blob' THEN %[1]s WHEN substr(%[1]s, 1, 1) = x'01' THEN %[2]s ELSE %[3]s END`,
		col, sqlUUIDText(h), h)
}

// sqlString decodes a strings id; id 0 has no row and reads as the empty string.
func sqlString(col string) string {
	return fmt.Sprintf(`COALESCE((SELECT value FROM strings WHERE id = %s), '')`, col)
}

func sqlTS(col string) string {
	return fmt.Sprintf(`strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', %s, 'unixepoch')`, col)
}

func sqlReceivedAt(col string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s = 0 THEN '' ELSE %[2]s END`, col, sqlTS(col))
}

// sqlAttributes rebuilds {"key":"value"} from {"<key id>":<value number>};
// a value stored as a string (none today) passes through.
func sqlAttributes(col string) string {
	return fmt.Sprintf(`(SELECT json_group_object(k.key, COALESCE(v.value, j.value))
  FROM json_each(%s) j
  JOIN project_attributes k ON k.id = CAST(j.key AS INTEGER)
  LEFT JOIN attribute_values v ON v.key_id = k.id AND v.n = j.value AND j.type = 'integer')`, col)
}
