package reporting

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// countingStore counts the share reads the public routes make, and fails
// them with fail when it is set.
type countingStore struct {
	Store
	rows, images atomic.Int64
	fail         error
}

func (c *countingStore) GetWidgetShare(ctx context.Context, id string) (store.WidgetShare, error) {
	c.rows.Add(1)
	if c.fail != nil {
		return store.WidgetShare{}, c.fail
	}
	return c.Store.GetWidgetShare(ctx, id)
}

func (c *countingStore) WidgetShareImage(ctx context.Context, id string, twoX bool) ([]byte, error) {
	c.images.Add(1)
	if c.fail != nil {
		return nil, c.fail
	}
	return c.Store.WidgetShareImage(ctx, id, twoX)
}

// countedShareEnv is sharePagesEnv with the Service reading through a
// countingStore.
func countedShareEnv(t *testing.T) (*shareEnv, *countingStore, http.Handler) {
	t.Helper()
	e := &shareEnv{now: new(time.Time)}
	*e.now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	st, files := newTestStoreAndFiles(t, 1000)
	cs := &countingStore{Store: st}
	e.st = st
	e.svc = newFilesService(cs, files, Options{
		ShareBaseURL: "https://c.example", ArchivedDays: 30,
		Now: func() time.Time { return *e.now },
	})
	syncReporting(t, e.svc, nil)
	e.projectID = mustCreateProject(t, st, "blog")
	e.widgetID = mustCreate(t, e.svc, "Board", note("Visits")).Widgets[0].ID
	mux := http.NewServeMux()
	mux.Handle("GET /share/{file}", e.svc.SharePages())
	return e, cs, mux
}

func wantStatus(t *testing.T, h http.Handler, file string, code int) {
	t.Helper()
	if rec := getShare(h, file); rec.Code != code {
		t.Fatalf("/share/%s: status %d, want %d", file, rec.Code, code)
	}
}

func TestShareCacheServesRepeatsWithoutTheStore(t *testing.T) {
	e, cs, h := countedShareEnv(t)
	sh := e.create(t, "")
	for range 3 {
		wantStatus(t, h, sh.ID, 200)
		wantStatus(t, h, sh.ID+".png", 200)
		wantStatus(t, h, sh.ID+"@2x.png", 200)
	}
	if n := cs.rows.Load(); n != 1 {
		t.Errorf("row reads = %d, want 1", n)
	}
	if n := cs.images.Load(); n != 2 {
		t.Errorf("image reads = %d, want 2 (one per size)", n)
	}
}

func TestShareCacheInvalidatesOnServiceWrites(t *testing.T) {
	e, _, h := countedShareEnv(t)
	ctx := context.Background()
	sh := e.create(t, "")
	wantStatus(t, h, sh.ID, 200)
	if _, err := e.svc.ArchiveWidgetShare(ctx, "test", sh.ID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, h, sh.ID, 404)
	wantStatus(t, h, sh.ID+".png", 404)
	if _, err := e.svc.RestoreWidgetShare(ctx, "test", sh.ID, "7d"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, h, sh.ID, 200)
	// Liveness is worked out from the cached row on each request: the new
	// date takes it down without another read.
	*e.now = e.now.AddDate(0, 0, 8)
	wantStatus(t, h, sh.ID, 404)
	*e.now = e.now.AddDate(0, 0, -8)
	if _, err := e.svc.UpdateWidgetShare(ctx, "test", sh.ID, "project"); err != nil {
		t.Fatal(err)
	}
	*e.now = e.now.AddDate(0, 0, 8)
	wantStatus(t, h, sh.ID, 200)
}

func TestShareCacheExpires(t *testing.T) {
	e, cs, h := countedShareEnv(t)
	sh := e.create(t, "")
	wantStatus(t, h, sh.ID, 200)
	*e.now = e.now.Add(shareRowAge - 2*time.Second)
	wantStatus(t, h, sh.ID, 200)
	if n := cs.rows.Load(); n != 1 {
		t.Fatalf("row reads within shareRowAge = %d, want 1", n)
	}
	// Archived behind the Service's back, as another process would.
	if _, err := e.st.SetWidgetShareArchived(context.Background(), sh.ID, true, "",
		store.AuditEntry{Actor: "cli", Action: "widget_share.archive"}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, h, sh.ID, 200) // still the cached row
	*e.now = e.now.Add(3 * time.Second)
	wantStatus(t, h, sh.ID, 404)
	if n := cs.rows.Load(); n != 2 {
		t.Errorf("row reads after expiry = %d, want 2", n)
	}

	unknown := "0197a2c4-0000-7000-8000-000000000000"
	before := cs.rows.Load()
	wantStatus(t, h, unknown, 404)
	wantStatus(t, h, unknown+".png", 404)
	*e.now = e.now.Add(9 * time.Second)
	wantStatus(t, h, unknown, 404)
	if n := cs.rows.Load() - before; n != 1 {
		t.Errorf("reads of an unknown id within 10 s = %d, want 1", n)
	}
	*e.now = e.now.Add(2 * time.Second)
	wantStatus(t, h, unknown, 404)
	if n := cs.rows.Load() - before; n != 2 {
		t.Errorf("reads of an unknown id after 10 s = %d, want 2", n)
	}
}

func TestShareCacheImageLRU(t *testing.T) {
	c := newShareCache(time.Now, 10)
	loads := map[shareImageKey]int{}
	load := func(_ context.Context, id string, twoX bool) ([]byte, error) {
		loads[shareImageKey{id, twoX}]++
		n := 4
		if twoX {
			n = 11 // over the bound: served, never kept
		}
		return make([]byte, n), nil
	}
	get := func(id string, twoX bool) {
		t.Helper()
		if _, err := c.image(context.Background(), id, twoX, load); err != nil {
			t.Fatal(err)
		}
	}
	get("a", false)
	get("b", false)
	get("a", false) // a is now the most recently used
	get("c", false) // 12 bytes: b goes
	get("a", false)
	get("c", false)
	get("b", false)
	get("x", true)
	get("x", true)
	for k, want := range map[shareImageKey]int{
		{"a", false}: 1, {"b", false}: 2, {"c", false}: 1, {"x", true}: 2,
	} {
		if loads[k] != want {
			t.Errorf("loads of %v = %d, want %d", k, loads[k], want)
		}
	}
	if c.imgBytes > 10 {
		t.Errorf("cache holds %d bytes, over its 10", c.imgBytes)
	}
}

func TestShareStoreFailureAnswers503(t *testing.T) {
	e, cs, h := countedShareEnv(t)
	cached, uncached := e.create(t, ""), e.create(t, "")
	wantStatus(t, h, cached.ID, 200) // its row is cached, its images are not
	cs.fail = errors.New("database is locked")
	for _, f := range []string{uncached.ID, uncached.ID + ".png", cached.ID + ".png", cached.ID + "@2x.png"} {
		rec := getShare(h, f)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("/share/%s: status %d, want 503", f, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("/share/%s: Cache-Control %q, want no-store", f, got)
		}
		if body := rec.Body.String(); body != "temporarily unavailable\n" {
			t.Errorf("/share/%s: body %q", f, body)
		}
	}
	// Failures are not cached: the next request reads again and serves.
	cs.fail = nil
	wantStatus(t, h, uncached.ID, 200)
	wantStatus(t, h, cached.ID+".png", 200)
}

// Archiving a share takes it back from every cache on the way to a
// reader: nothing a live share is served with may be reused without
// asking (no max-age), a cache that asks gets 304 only while the share is
// live, and the archive answers 404, to a conditional request too, and
// frees the picture.
func TestArchivedShareIsRevokedFromCaches(t *testing.T) {
	e, _, h := countedShareEnv(t)
	sh := e.create(t, "")
	conditional := func(file string) *httptest.ResponseRecorder {
		etag := getShare(h, file).Header().Get("ETag")
		req := httptest.NewRequest(http.MethodGet, "/share/"+file, nil)
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	files := []string{sh.ID, sh.ID + ".png", sh.ID + "@2x.png"}
	for _, f := range files {
		rec := getShare(h, f)
		if cc := rec.Header().Get("Cache-Control"); rec.Code != 200 || cc != "no-cache, must-revalidate" {
			t.Errorf("/share/%s: %d, Cache-Control %q; want 200 revalidation: a stale copy outlives an archive", f, rec.Code, cc)
		}
	}
	for _, f := range files[1:] {
		if rec := conditional(f); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("/share/%s revalidated while live: %d with %d bytes, want 304 and none", f, rec.Code, rec.Body.Len())
		}
	}
	if e.svc.shares.imgBytes == 0 {
		t.Fatal("the images are not cached while live")
	}

	etags := map[string]string{}
	for _, f := range files[1:] {
		etags[f] = getShare(h, f).Header().Get("ETag")
	}
	if _, err := e.svc.ArchiveWidgetShare(context.Background(), "test", sh.ID); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		req := httptest.NewRequest(http.MethodGet, "/share/"+f, nil)
		if etags[f] != "" {
			req.Header.Set("If-None-Match", etags[f])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if cc := rec.Header().Get("Cache-Control"); rec.Code != http.StatusNotFound || cc != "no-store" || rec.Header().Get("ETag") != "" {
			t.Errorf("/share/%s after the archive: %d, Cache-Control %q, ETag %q; want 404 no-store and no ETag",
				f, rec.Code, cc, rec.Header().Get("ETag"))
		}
	}
	if n := e.svc.shares.imgBytes; n != 0 || len(e.svc.shares.imgs) != 0 || e.svc.shares.lru.Len() != 0 {
		t.Errorf("%d bytes of the archived share's images are still cached", n)
	}
}

// A share deleted behind the Service's back (the CLI's project delete in
// another process, the daily pass) is gone within shareRowAge, which is
// seconds: the bound on how long a revoked share can still be answered.
func TestDeletedShareGoesWithinTheRowAge(t *testing.T) {
	if shareRowAge > 10*time.Second {
		t.Fatalf("shareRowAge = %v: a share deleted from elsewhere stays up that long", shareRowAge)
	}
	e, _, h := countedShareEnv(t)
	sh := e.create(t, "")
	wantStatus(t, h, sh.ID, 200)
	wantStatus(t, h, sh.ID+".png", 200)
	if err := e.st.DeleteProjectData(context.Background(), e.projectID, store.AuditEntry{Actor: "cli", Action: "project.delete"}); err != nil {
		t.Fatal(err)
	}
	*e.now = e.now.Add(shareRowAge)
	wantStatus(t, h, sh.ID, 404)
	wantStatus(t, h, sh.ID+".png", 404)
}
