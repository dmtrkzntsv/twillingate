package alerts

import "testing"

func TestFormatValue(t *testing.T) {
	count, fraction, number := Parsed{Read: readCount}, Parsed{Read: readFraction}, Parsed{Read: readNumber}
	for _, c := range []struct {
		p    Parsed
		v    *float64
		want string
	}{
		{count, ptr(1240.0), "1,240"},
		{count, ptr(812.0), "812"},
		{count, ptr(0.0), "0"},
		{count, ptr(1234567.0), "1,234,567"},
		{count, ptr(-5015.0), "-5,015"},
		{fraction, ptr(0.0213), "2.1%"},
		{fraction, ptr(0.41), "41.0%"},
		{fraction, ptr(0.0), "0.0%"},
		{fraction, ptr(1.0), "100.0%"},
		{number, ptr(1234.567), "1,234.57"},
		{number, ptr(12.5), "12.5"},
		{number, ptr(12.0), "12"},
		{number, ptr(-0.001), "0"},
		{number, ptr(-1234.5), "-1,234.5"},
		{number, nil, "—"},
		{count, nil, "—"},
		{fraction, nil, "—"},
		{Parsed{}, ptr(3.25), "3.25"}, // a definition that no longer parses
	} {
		if got := formatValue(c.p, c.v); got != c.want {
			t.Errorf("formatValue(%v, %v) = %q, want %q", c.p.Read, deref(c.v), got, c.want)
		}
	}
}

func TestFormatLine(t *testing.T) {
	count, fraction, number := Parsed{Read: readCount}, Parsed{Read: readFraction}, Parsed{Read: readNumber}
	for _, c := range []struct {
		p    Parsed
		v    float64
		want string
	}{
		{fraction, 0.03, "3%"},
		{fraction, 0.025, "2.5%"},
		{fraction, 0.07, "7%"},
		{fraction, 0.5, "50%"},
		{count, 1000, "1,000"},
		{count, 2.5, "2.5"},
		{number, 1234.5, "1,234.5"},
		{number, 0.126, "0.13"},
	} {
		if got := formatLine(c.p, c.v); got != c.want {
			t.Errorf("formatLine(%v, %v) = %q, want %q", c.p.Read, c.v, got, c.want)
		}
	}
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
