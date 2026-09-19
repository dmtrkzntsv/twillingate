package dashboards

// Evidence's ButtonGroup passes defaultValue only to items it generates
// itself (from a preset or a query). Items written out in the page as
// <ButtonGroupItem> children never see it, and select themselves only when
// one carries `default`. Without that, the input starts empty, every query
// that interpolates it filters to nothing, and the page renders blank until
// someone clicks a button.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	buttonGroupRe = regexp.MustCompile(`(?s)<ButtonGroup\b([^>]*)>(.*?)</ButtonGroup>`)
	itemRe        = regexp.MustCompile(`<ButtonGroupItem\b[^>]*>`)
	defaultAttrRe = regexp.MustCompile(`\sdefault(\s|/|>|=)`)
)

func TestButtonGroupsMarkOneItemDefault(t *testing.T) {
	pages := filepath.Join(evidenceDir, "pages")
	groups := 0
	err := filepath.WalkDir(pages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(evidenceDir, path)
		for _, m := range buttonGroupRe.FindAllStringSubmatch(string(body), -1) {
			groups++
			attrs, children := m[1], m[2]
			if strings.Contains(attrs, "defaultValue") {
				t.Errorf("%s: ButtonGroup sets defaultValue, which its written-out items ignore; mark the item `default` instead", rel)
			}
			defaults := 0
			for _, item := range itemRe.FindAllString(children, -1) {
				if defaultAttrRe.MatchString(item) {
					defaults++
				}
			}
			if defaults != 1 {
				t.Errorf("%s: ButtonGroup%s has %d items marked default, want 1", rel, attrs, defaults)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", pages, err)
	}
	if groups == 0 {
		t.Fatalf("no ButtonGroup under %s; the check would be vacuous", pages)
	}
}

// A query that reads the URL can only be right in the browser: a prerendered
// URL has no query string. If it still resolves while prerendering (every
// input it names has a default), Evidence ships that result with the page
// and seeds the browser's first run with it, so the page shows the
// prerendered answer for the real URL until an input changes. Interpolating
// an input nobody sets on the prerender branch keeps the query unresolved
// there, and the browser runs it fresh.
var browserGuardRe = regexp.MustCompile(`\$\{browser \?.*?: ([^}]*)\}`)

func TestURLQueriesStayUnresolvedWhilePrerendering(t *testing.T) {
	pages := filepath.Join(evidenceDir, "pages")
	guards := 0
	err := filepath.WalkDir(pages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(evidenceDir, path)
		inSQL := false
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "```") {
				inSQL = strings.HasPrefix(line, "```sql")
				continue
			}
			if !inSQL {
				continue
			}
			for _, m := range browserGuardRe.FindAllStringSubmatch(line, -1) {
				guards++
				if !strings.HasPrefix(strings.TrimSpace(m[1]), "inputs.") {
					t.Errorf("%s:%d: prerender branch interpolates %q, which resolves; use an unset input", rel, i+1, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", pages, err)
	}
	if guards == 0 {
		t.Fatalf("no browser-guarded query under %s; the check would be vacuous", pages)
	}
}
