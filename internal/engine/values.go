package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"
)

// jsonValue turns what DuckDB scans into a value JSON carries faithfully:
// exact numbers stay numbers (json.Number, so an int64, a DECIMAL or a
// HUGEINT is never rounded through float64), dates and times become text,
// a UUID its usual text form, NaN and the infinities (which JSON cannot
// carry) the text "NaN", "Infinity" and "-Infinity". dbType is the column's DatabaseTypeName;
// values nested in a list or struct pass "" and keep the generic forms.
func jsonValue(v any, dbType string) any {
	switch x := v.(type) {
	case float32:
		return nonFinite(float64(x), x)
	case float64:
		return nonFinite(x, x)
	case nil, bool, string, int8, int16, int32, int64, uint8, uint16, uint32, uint64:
		return x
	case time.Time:
		switch dbType {
		case "DATE":
			return x.Format("2006-01-02")
		case "TIME":
			return x.Format("15:04:05.999999")
		}
		return x.UTC().Format(time.RFC3339Nano)
	case []byte:
		if dbType == "UUID" && len(x) == 16 {
			return uuid.UUID(x).String()
		}
		return x // base64, as encoding/json writes bytes
	case duckdb.Decimal:
		return json.Number(x.String())
	case *big.Int:
		return json.Number(x.String())
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonValue(e, "")
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonValue(e, "")
		}
		return out
	default:
		return fmt.Sprint(x) // MAP, INTERVAL, UNION: their text form
	}
}

// nonFinite returns the text for a NaN or infinity f, else v unchanged.
func nonFinite(f float64, v any) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return v
}
