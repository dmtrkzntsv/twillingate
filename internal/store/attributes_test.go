package store

import (
	"fmt"
	"maps"
	"reflect"
	"testing"
)

func TestMapAttributes(t *testing.T) {
	rename := func(pairs ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i < len(pairs); i += 2 {
			m[pairs[i]] = pairs[i+1]
		}
		return m
	}
	blocked := func(keys ...string) map[string]bool {
		m := map[string]bool{}
		for _, k := range keys {
			m[k] = true
		}
		return m
	}
	for _, tc := range []struct {
		name    string
		in      map[string]string
		rules   AttributeRuleSet
		want    map[string]string
		dropped []string
		same    bool // the input map itself comes back
	}{
		{name: "empty set", in: map[string]string{"a": "1"}, want: map[string]string{"a": "1"}, same: true},
		{name: "no key affected", in: map[string]string{"a": "1"},
			rules: AttributeRuleSet{Renamed: rename("x", "y"), Blocked: blocked("z")},
			want:  map[string]string{"a": "1"}, same: true},
		{name: "rename", in: map[string]string{"a": "1"},
			rules: AttributeRuleSet{Renamed: rename("a", "b")}, want: map[string]string{"b": "1"}},
		{name: "the target wins", in: map[string]string{"a": "1", "b": "2"},
			rules: AttributeRuleSet{Renamed: rename("a", "b")}, want: map[string]string{"b": "2"}},
		{name: "first in key order wins", in: map[string]string{"a1": "1", "a2": "2"},
			rules: AttributeRuleSet{Renamed: rename("a1", "b", "a2", "b")}, want: map[string]string{"b": "1"}},
		{name: "block", in: map[string]string{"x": "1", "y": "2"},
			rules: AttributeRuleSet{Blocked: blocked("x")}, want: map[string]string{"y": "2"}, dropped: []string{"x"}},
		{name: "renamed onto a blocked key", in: map[string]string{"a": "1"},
			rules: AttributeRuleSet{Renamed: rename("a", "b"), Blocked: blocked("b")},
			want:  map[string]string{}, dropped: []string{"b"}},
		{name: "several blocked, sorted", in: map[string]string{"z": "1", "m": "2", "a": "3", "k": "4"},
			rules: AttributeRuleSet{Blocked: blocked("z", "m", "a")},
			want:  map[string]string{"k": "4"}, dropped: []string{"a", "m", "z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := maps.Clone(tc.in)
			out, dropped := MapAttributes(tc.in, tc.rules)
			if !reflect.DeepEqual(out, tc.want) {
				t.Errorf("out = %v, want %v", out, tc.want)
			}
			if !reflect.DeepEqual(dropped, tc.dropped) {
				t.Errorf("dropped = %v, want %v", dropped, tc.dropped)
			}
			if same := reflect.ValueOf(out).Pointer() == reflect.ValueOf(tc.in).Pointer(); same != tc.same {
				t.Errorf("same map returned = %v, want %v", same, tc.same)
			}
			if !reflect.DeepEqual(tc.in, before) {
				t.Errorf("input changed: %v, was %v", tc.in, before)
			}
		})
	}
}

func benchAttrs() map[string]string {
	m := make(map[string]string, 20)
	for k := 0; k < 20; k++ {
		m[fmt.Sprintf("key%02d", k)] = fmt.Sprintf("v%d", k)
	}
	return m
}

func BenchmarkMapAttributesNoRules(b *testing.B) {
	attrs := benchAttrs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MapAttributes(attrs, AttributeRuleSet{})
	}
}

func BenchmarkMapAttributesUnaffected(b *testing.B) {
	attrs := benchAttrs()
	rules := AttributeRuleSet{
		Renamed: map[string]string{"old_plan": "plan", "tier": "plan", "ver": "version"},
		Blocked: map[string]bool{"email": true, "ssn": true},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MapAttributes(attrs, rules)
	}
}
