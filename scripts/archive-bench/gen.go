package main

// gen: prod-shaped events, 3 years, inserted with twillingate's own ingest
// statement (internal/store/sqlite/write.go insertEvents) in batches.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"os"
	"strconv"
	"time"
)

type Ev struct {
	ID, Family                                         string
	ProjectID                                          int64
	EventName, TS, Day, ReceivedAt, Kind               string
	ActorID, ActorKind, UserID, GroupID, SessionID     string
	Host, Path, Ref, UTMSource, UTMMedium, UTMCampaign string
	Platform, OS, OSVersion, OSName                    string
	Browser, BrowserVersion, BrowserLocale             string
	AppVersion, AppLocale, Device, DeviceModel         string
	DisplayW, DisplayH                                 int64
	Country                                            string
	Consent                                            *int64
	Attributes                                         string
	Value                                              *float64
	Measure                                            string
	SampleRate                                         float64
}

const insertSQL = `INSERT OR IGNORE INTO events
		(id, project_id, family, event_name, ts, day, received_at, kind,
		 actor_id, actor_kind, user_id, group_id, session_id,
		 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
		 platform, os, os_version, os_name, browser, browser_version, browser_locale,
		 app_version, app_locale, device, device_model, display_width, display_height,
		 country, consent, attributes, value, measure, sample_rate)
		VALUES (?,?,?,
		 COALESCE((SELECT to_name FROM rules WHERE project_id=? AND kind='event' AND family=? AND name=? AND action='rename'), ?),
		 ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

type user struct {
	id, group      string
	start, end     int
	web            bool
	browser, os    string
	osv, bv, loc   string
	country        string
	attrs          map[string]string
	accounts, cats int
}

var (
	r         = mrand.New(mrand.NewPCG(1, 2))
	names     []string
	paths     []string
	tailPaths []string
	appPaths  = []string{"/budget", "/account/:id", "/settings", "/reports", "/categories", "/payees", "/tags", "/connections", "/onboarding", "/billing"}
	countries = []string{"US", "US", "US", "GB", "DE", "DE", "FR", "CA", "NL", "IN", "AU", "SE", "BR", "PL", "ES", "IT", "JP", "RU", "UA", "MX"}
	browsers  = [][3]string{{"chrome", "windows", "10"}, {"chrome", "macos", "15.1"}, {"safari", "ios", "18.7"}, {"safari", "macos", "15.1"}, {"firefox", "linux", ""}, {"chrome", "android", "15"}, {"edge", "windows", "11"}}
	refs      = []string{"", "", "", "google", "google", "github", "reddit", "hackernews", "twitter", "duckduckgo", "bing"}
	locales   = []string{"en-US", "en-GB", "de-DE", "fr-FR", "ru-RU", "en-CA", "es-ES", "zh-CN"}
	users     []*user
)

func hexs(n int) string {
	b := make([]byte, n/2)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return hex.EncodeToString(b)
}

func uuid7(t time.Time) string {
	var b [16]byte
	rand.Read(b[:])
	ms := uint64(t.UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = 0x70 | (b[6] & 0x0f)
	b[8] = 0x80 | (b[8] & 0x3f)
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// zipf-ish index in [0,n): skewed to small indices.
func skew(n int, p float64) int {
	return int(float64(n) * math.Pow(r.Float64(), p))
}

func pick[T any](s []T) T { return s[r.IntN(len(s))] }

func setup(days int) {
	verbs := []string{"create", "update", "delete", "open", "list", "change_date", "order_list", "archive", "export", "import", "share", "view"}
	nouns := []string{"transaction", "account", "budget", "category", "payee", "tag", "connection", "report", "api_account", "budget_transactions"}
	for _, n := range nouns {
		for _, v := range verbs {
			names = append(names, n+"_"+v)
		}
	}
	names = append([]string{"page_view"}, names...)
	for i := 0; i < 400; i++ {
		paths = append(paths, fmt.Sprintf("/catalog/%s/%s/", hexs(6), pick([]string{"releases", "docs", "pricing", "changelog", "install"})))
	}
	for i := 0; i < 300000; i++ {
		tailPaths = append(tailPaths, fmt.Sprintf("/catalog/%s/releases/%d.%d.%d/", hexs(8), r.IntN(30), r.IntN(20), r.IntN(10)))
	}
	// 60k product users over [-365, days): signup spread, lifetime pareto.
	for i := 0; i < 60000; i++ {
		start := -365 + r.IntN(days+365)
		life := int(30 / math.Pow(r.Float64()+1e-3, 0.8))
		b := pick(browsers)
		u := &user{id: hexs(32), start: start, end: start + life, web: r.IntN(10) < 4,
			browser: b[0], os: b[1], osv: b[2], bv: strconv.Itoa(100 + r.IntN(40)), loc: pick(locales), country: pick(countries),
			accounts: 1 + r.IntN(60), cats: 5 + r.IntN(180)}
		if i%3 == 0 || len(users) == 0 {
			u.group = hexs(12)
		} else {
			u.group = users[len(users)-1-r.IntN(min(len(users), 3))].group
		}
		u.attrs = map[string]string{
			"access_state": pick([]string{"full_access", "full_access", "trial", "expired"}),
			"deployment":   pick([]string{"cloud", "cloud", "self-hosted"}),
			"auth_apple":   pick([]string{"on", "off"}), "auth_google": pick([]string{"on", "off"}),
			"auth_password": "on", "auth_sso": "off",
			"locale": u.loc[:2], "mode": pick([]string{"desktop", "mobile"}),
			"signup_year": strconv.Itoa(2021 + r.IntN(6)), "signup_month": strconv.Itoa(1 + r.IntN(12)),
			"payees": strconv.Itoa(r.IntN(500)), "tags": strconv.Itoa(r.IntN(50)),
			"payees_archived": strconv.Itoa(r.IntN(100)), "tags_archived": strconv.Itoa(r.IntN(40)),
			"categories_archived": strconv.Itoa(r.IntN(40)), "connections": strconv.Itoa(r.IntN(3)),
		}
		users = append(users, u)
	}
}

func dayEvents(d0 time.Time, di, n int) []Ev {
	day := d0.AddDate(0, 0, di)
	ds := day.Format("2006-01-02")
	var active []*user
	for _, u := range users {
		if u.start <= di && di < u.end {
			active = append(active, u)
		}
	}
	evs := make([]Ev, 0, n)
	visitors := make([]string, n/3)
	for i := range visitors {
		visitors[i] = hexs(16)
	}
	for i := 0; i < n; i++ {
		ts := day.Add(time.Duration(r.Int64N(int64(24 * time.Hour)))).Truncate(time.Second)
		e := Ev{ID: uuid7(ts), TS: ts.Format("2006-01-02T15:04:05Z"), Day: ds,
			ReceivedAt: ts.Add(time.Duration(r.IntN(10)) * time.Second).Format("2006-01-02T15:04:05Z"),
			Attributes: "{}", SampleRate: 1, Platform: "unknown"}
		x := r.IntN(100)
		switch {
		case x < 45 && len(active) > 0: // product, project 1
			u := active[skew(len(active), 2)]
			e.Family, e.ProjectID, e.EventName = "product", 1, names[skew(len(names), 2.5)]
			e.ActorID, e.ActorKind, e.UserID, e.GroupID = u.id, "user", u.id, u.group
			a := make(map[string]string, len(u.attrs)+4)
			for k, v := range u.attrs {
				a[k] = v
			}
			a["accounts"] = strconv.Itoa(u.accounts + (di-u.start)/30)
			a["categories"] = strconv.Itoa(u.cats + (di-u.start)/20)
			a["months_since_signup"] = strconv.Itoa(max(0, (di-u.start)/30))
			if u.web {
				e.Kind, e.Platform, e.Host, e.Path = "web", "web", "app.example.com", pick(appPaths)
				e.Browser, e.OS, e.OSVersion, e.BrowserVersion, e.BrowserLocale = u.browser, u.os, u.osv, u.bv, u.loc
				e.Device, e.Country, e.AppVersion = "desktop", u.country, "dev"
				e.DisplayW, e.DisplayH = 1920, 1080
				c := int64(0)
				e.Consent = &c
			} else {
				e.OS, e.AppVersion = "other", fmt.Sprintf("v1.%d.%d", 3+di/200, r.IntN(4))
				a["current_url"] = "https://app.example.com" + pick(appPaths)
				a["host"] = "app.example.com"
			}
			bs, _ := json.Marshal(a)
			e.Attributes = string(bs)
		case x < 55 && len(active) > 0: // app views, project 1, ids
			u := active[skew(len(active), 2)]
			e.Family, e.ProjectID, e.EventName, e.Kind, e.Platform = "views", 1, "$page_view", "web", "web"
			e.ActorID, e.ActorKind, e.UserID, e.GroupID = u.id, "user", u.id, u.group
			e.Host, e.Path, e.Browser, e.OS, e.OSVersion, e.BrowserVersion, e.BrowserLocale = "app.example.com", pick(appPaths), u.browser, u.os, u.osv, u.bv, u.loc
			e.Device, e.Country, e.DisplayW, e.DisplayH = "desktop", u.country, 1920, 1080
		case x < 95: // site views, project 2, anonymous daily hash
			b := pick(browsers)
			e.Family, e.ProjectID, e.EventName, e.Kind, e.Platform = "views", 2, "$page_view", "web", "web"
			e.ActorID, e.ActorKind = visitors[skew(len(visitors), 1.3)], "anonymous"
			e.Host = "site.example.com"
			if r.IntN(10) < 6 {
				e.Path = paths[skew(len(paths), 3)]
			} else {
				e.Path = pick(tailPaths)
			}
			e.Ref = pick(refs)
			if r.IntN(20) == 0 {
				e.UTMSource, e.UTMMedium, e.UTMCampaign = "newsletter", "email", "launch-"+strconv.Itoa(di/30)
			}
			e.Browser, e.OS, e.OSVersion, e.BrowserVersion, e.BrowserLocale = b[0], b[1], b[2], strconv.Itoa(100+r.IntN(40)), pick(locales)
			e.OSName = b[1] + " " + b[2]
			e.Device, e.Country = pick([]string{"desktop", "desktop", "mobile", "tablet"}), pick(countries)
			e.DisplayW, e.DisplayH = pick([]int64{1920, 2560, 1440, 390, 1280}), 1080
			c := int64(0)
			e.Consent = &c
		default: // measures, project 1
			b := pick(browsers)
			e.Family, e.ProjectID, e.Kind, e.Platform = "measures", 1, "web", "web"
			e.EventName = pick([]string{"$fcp", "$lcp", "$ttfb", "$inp", "$cls"})
			e.Measure = "time"
			v := math.Exp(6.5 + r.NormFloat64()*0.7)
			e.Value = &v
			e.ActorID, e.ActorKind = hexs(16), "anonymous"
			e.Host, e.Path = "app.example.com", pick(appPaths)
			e.Browser, e.OS, e.OSVersion, e.BrowserVersion = b[0], b[1], b[2], strconv.Itoa(100+r.IntN(40))
			e.Device, e.Country, e.DisplayW, e.DisplayH = "desktop", pick(countries), 1920, 1080
			c := int64(0)
			e.Consent = &c
		}
		evs = append(evs, e)
	}
	return evs
}

func insertBatch(db *sql.DB, evs []Ev) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		return err
	}
	for _, e := range evs {
		var consent, value any
		if e.Consent != nil {
			consent = *e.Consent
		}
		if e.Value != nil {
			value = *e.Value
		}
		if _, err := stmt.Exec(e.ID, e.ProjectID, e.Family,
			e.ProjectID, e.Family, e.EventName, e.EventName,
			e.TS, e.Day, e.ReceivedAt, e.Kind,
			e.ActorID, e.ActorKind, e.UserID, e.GroupID, e.SessionID,
			e.Host, e.Path, e.Ref, e.UTMSource, e.UTMMedium, e.UTMCampaign,
			e.Platform, e.OS, e.OSVersion, e.OSName, e.Browser, e.BrowserVersion, e.BrowserLocale,
			e.AppVersion, e.AppLocale, e.Device, e.DeviceModel, e.DisplayW, e.DisplayH,
			e.Country, consent, e.Attributes, value, e.Measure, e.SampleRate); err != nil {
			return err
		}
	}
	stmt.Close()
	return tx.Commit()
}

func openDB(path string) *sql.DB {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-16000)&_pragma=temp_store(2)&_pragma=journal_mode(WAL)")
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func fsize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// gen <db> <from YYYY-MM-DD> <days> <perDay> <batch>
func cmdGen(args []string) {
	path := args[0]
	d0, _ := time.Parse("2006-01-02", args[1])
	days, _ := strconv.Atoi(args[2])
	perDay, _ := strconv.Atoi(args[3])
	batch, _ := strconv.Atoi(args[4])
	setup(days)
	db := openDB(path)
	defer db.Close()
	var total int
	var insDur time.Duration
	monthRows, monthStart := 0, time.Duration(0)
	for di := 0; di < days; di++ {
		evs := dayEvents(d0, di, perDay)
		t := time.Now()
		for i := 0; i < len(evs); i += batch {
			if err := insertBatch(db, evs[i:min(i+batch, len(evs))]); err != nil {
				panic(err)
			}
		}
		insDur += time.Since(t)
		total += len(evs)
		monthRows += len(evs)
		if (di+1)%30 == 0 || di == days-1 {
			dur := insDur - monthStart
			fmt.Printf("day %4d rows %9d  month %.0f rows/s  db %6.0f MB  wal %5.0f MB\n", di+1, total,
				float64(monthRows)/dur.Seconds(), float64(fsize(path))/1e6, float64(fsize(path+"-wal"))/1e6)
			monthRows, monthStart = 0, insDur
		}
	}
	fmt.Printf("TOTAL %d rows, insert time %s, %.0f rows/s\n", total, insDur.Round(time.Second), float64(total)/insDur.Seconds())
}
