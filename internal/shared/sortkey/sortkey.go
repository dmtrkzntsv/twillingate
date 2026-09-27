// Package sortkey makes fractional sort keys: strings that sort in byte
// order where a key can always be made between any two others without
// touching them, so inserting never renumbers. The scheme is David
// Greenspan's "Implementing Fractional Indexing" (keys look like a0, a0V,
// a1): an integer part whose first character encodes its length, then a
// base-62 fraction that never ends in '0'. Appending stays short because
// the integer part grows first; only inserts between neighbours lengthen
// the fraction.
package sortkey

import (
	"errors"
	"fmt"
	"strings"
)

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// smallestInteger is the lowest integer part. It is not a key itself,
// or nothing could ever sort before it.
var smallestInteger = "A" + strings.Repeat("0", 26)

// Between returns a key sorting strictly after a and before b. An empty
// a means "before everything", an empty b "after everything".
func Between(a, b string) (string, error) {
	for _, k := range []string{a, b} {
		if k != "" {
			if err := validate(k); err != nil {
				return "", err
			}
		}
	}
	if a != "" && b != "" && a >= b {
		return "", fmt.Errorf("sortkey: %q is not before %q", a, b)
	}
	if a == "" {
		if b == "" {
			return "a0", nil
		}
		ib := integerPart(b)
		if ib == smallestInteger {
			m, err := midpoint("", b[len(ib):])
			return ib + m, err
		}
		if ib < b {
			return ib, nil
		}
		if d, ok := decrement(ib); ok {
			return d, nil
		}
		return "", fmt.Errorf("sortkey: nothing sorts before %q", b)
	}
	ia := integerPart(a)
	fa := a[len(ia):]
	if b == "" {
		if i, ok := increment(ia); ok {
			return i, nil
		}
		m, err := midpoint(fa, "")
		return ia + m, err
	}
	ib := integerPart(b)
	if ia == ib {
		m, err := midpoint(fa, b[len(ib):])
		return ia + m, err
	}
	i, ok := increment(ia)
	if !ok {
		return "", fmt.Errorf("sortkey: nothing sorts after %q", a)
	}
	if i < b {
		return i, nil
	}
	m, err := midpoint(fa, "")
	return ia + m, err
}

// Spread returns n ascending keys strictly between a and b (either may be
// "" for an open end), splitting the gap evenly so later inserts between
// them stay short.
func Spread(a, b string, n int) ([]string, error) {
	switch {
	case n <= 0:
		return nil, nil
	case n == 1:
		k, err := Between(a, b)
		if err != nil {
			return nil, err
		}
		return []string{k}, nil
	case b == "":
		out := make([]string, 0, n)
		k := a
		for range n {
			var err error
			if k, err = Between(k, ""); err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, nil
	case a == "":
		out := make([]string, n)
		k := b
		for i := n - 1; i >= 0; i-- {
			var err error
			if k, err = Between("", k); err != nil {
				return nil, err
			}
			out[i] = k
		}
		return out, nil
	}
	mid := n / 2
	c, err := Between(a, b)
	if err != nil {
		return nil, err
	}
	left, err := Spread(a, c, mid)
	if err != nil {
		return nil, err
	}
	right, err := Spread(c, b, n-mid-1)
	if err != nil {
		return nil, err
	}
	return append(append(left, c), right...), nil
}

// midpoint returns a fraction strictly between a and b, where b == ""
// means no upper bound. A bounded b is never "" here: callers only pass a
// fraction that follows an equal integer part, which is non-empty.
func midpoint(a, b string) (string, error) {
	if b != "" && a >= b {
		return "", fmt.Errorf("sortkey: fraction %q is not before %q", a, b)
	}
	if strings.HasSuffix(a, "0") || strings.HasSuffix(b, "0") {
		return "", errors.New("sortkey: fraction ends in 0")
	}
	if b != "" {
		n := 0 // the common prefix, reading a as 0-padded
		for n < len(b) && digitAt(a, n) == b[n] {
			n++
		}
		if n > 0 {
			m, err := midpoint(a[min(n, len(a)):], b[n:])
			return b[:n] + m, err
		}
	}
	da, db := 0, len(digits)
	if a != "" {
		da = strings.IndexByte(digits, a[0])
	}
	if b != "" {
		db = strings.IndexByte(digits, b[0])
	}
	if db-da > 1 {
		return string(digits[(da+db+1)/2]), nil
	}
	if len(b) > 1 {
		return b[:1], nil
	}
	m, err := midpoint(a[min(1, len(a)):], "")
	return string(digits[da]) + m, err
}

func digitAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return '0'
}

// integerLength is how long an integer part whose first character is
// head is: a–z are 2–27 characters (non-negative), Z–A the same lengths
// mirrored (negative), so byte order is numeric order.
func integerLength(head byte) int {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2
	}
	return 0
}

// integerPart assumes a validated key.
func integerPart(key string) string { return key[:integerLength(key[0])] }

func validate(key string) error {
	n := integerLength(key[0])
	if n == 0 || n > len(key) || key == smallestInteger {
		return fmt.Errorf("sortkey: invalid key %q", key)
	}
	for i := 1; i < len(key); i++ {
		if strings.IndexByte(digits, key[i]) < 0 {
			return fmt.Errorf("sortkey: invalid key %q", key)
		}
	}
	if strings.HasSuffix(key[n:], "0") {
		return fmt.Errorf("sortkey: invalid key %q (fraction ends in 0)", key)
	}
	return nil
}

func increment(x string) (string, bool) {
	head, digs := x[0], []byte(x[1:])
	for i := len(digs) - 1; i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) + 1
		if d < len(digits) {
			digs[i] = digits[d]
			return string(head) + string(digs), true
		}
		digs[i] = '0'
	}
	switch head {
	case 'Z':
		return "a0", true
	case 'z':
		return "", false
	}
	h := head + 1
	if h > 'a' {
		digs = append(digs, '0')
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), true
}

func decrement(x string) (string, bool) {
	head, digs := x[0], []byte(x[1:])
	for i := len(digs) - 1; i >= 0; i-- {
		d := strings.IndexByte(digits, digs[i]) - 1
		if d >= 0 {
			digs[i] = digits[d]
			return string(head) + string(digs), true
		}
		digs[i] = digits[len(digits)-1]
	}
	switch head {
	case 'a':
		return "Z" + digits[len(digits)-1:], true
	case 'A':
		return "", false
	}
	h := head - 1
	if h < 'Z' {
		digs = append(digs, digits[len(digits)-1])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), true
}
