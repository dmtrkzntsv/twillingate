package reporting

import (
	"os"
	"strings"
	"testing"
)

// TestManifestParses is the spec's Tests "manifest": the built
// components.json this binary embeds must parse as a valid manifest.
func TestManifestParses(t *testing.T) {
	if _, err := ParseManifest(Manifest()); err != nil {
		t.Fatal(err)
	}
}

// TestManifestNamesMatchWidgetFiles checks, in both directions, that the
// embedded manifest names exactly the widget components the web build
// ships: web/src/components/widgets/*.tsx, excluding index.ts, types.ts
// and *.test.tsx.
func TestManifestNamesMatchWidgetFiles(t *testing.T) {
	comps, err := ParseManifest(Manifest())
	if err != nil {
		t.Fatal(err)
	}
	have := make(map[string]bool, len(comps))
	for _, c := range comps {
		have[c.Name] = true
	}

	entries, err := os.ReadDir("../../web/src/components/widgets")
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".test.tsx") {
			continue
		}
		want[strings.TrimSuffix(name, ".tsx")] = true
	}
	if len(want) == 0 {
		t.Fatal("no widget .tsx files found; check the relative path")
	}

	for name := range want {
		if !have[name] {
			t.Errorf("widget file %s.tsx has no manifest entry", name)
		}
	}
	for name := range have {
		if !want[name] {
			t.Errorf("manifest names %s, which has no widget file", name)
		}
	}
}
