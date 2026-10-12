#!/usr/bin/env python3
"""Seed a local database with demo traffic for every project in the database.

For dashboard development only -- writes straight to SQLite, bypassing the
HTTP API. Deterministic (fixed seed), with per-project traffic profiles, a
growth trend and weekend dips so the charts show a believable shape.

Rows are written as ingest stores them (migration 041, encode.go and
dict.go): the script generates plain text rows into a TEMP staging table
shaped like the events table before 041, then one SQL pass per project
encodes them -- text columns become strings ids, attributes become catalog
key ids and value numbers, ids become tagged bytes, timestamps become Unix
seconds -- creating the dictionary rows the way ingest does (with their
last_seen_day). That pass is the copy block of 041 written as an upsert, and
needs SQLite 3.41 for unhex() (Python 3.12 ships newer).

Covers 180 days so the widest dashboard range has data to plot. Idempotent:
seeded rows are cleared before reinsertion, so re-running refreshes the window
rather than stacking another copy on top.

The "dev" profile also sends Web Vitals from a share of its page views and
a backend's checkout_api timing, so the Web Vitals and Measures dashboards
have histograms to read.

Projects with an "app" profile also get screen views across two platforms and
three app versions, so the version-adoption chart has a rollout to show.
Projects whose profile sends ids ("ids": True) get stable actor ids plus
user and group identities, which is what makes the users, groups and
retention pages meaningful, the others get a per-day connection hash, as a
site with an anonymous tag would.

    python3 scripts/seed-demo.py local/twillingate.db

Projects are read from the projects table; each needs a traffic profile in
PROFILES, keyed by project name.
"""

import datetime
import hashlib
import json
import math
import random
import sqlite3
import sys
import uuid

DAYS = 180

# Per-project shape, keyed by project name: starting daily visitors, growth
# per day, and the content mix. Each profile reads differently on the
# dashboard -- a launch-driven marketing site, steady docs traffic, a small
# app, and a decaying blog.
PROFILES = {
    "dev": {
        "base": 18, "growth": 0.45, "product": True, "app": 12, "ids": True, "measures": True,
        "pages": [("/", 30), ("/pricing", 18), ("/docs", 15), ("/docs/quickstart", 10),
                  ("/blog/launch", 9), ("/blog/why-privacy", 7), ("/about", 6), ("/changelog", 5)],
        "refs": [("", 30), ("google", 25), ("hackernews", 15), ("reddit", 10),
                 ("twitter", 8), ("producthunt", 7), ("github", 5)],
    },
    "marketing": {
        "base": 120, "growth": 2.4, "product": False, "ids": False,
        "pages": [("/", 40), ("/pricing", 22), ("/features", 14), ("/customers", 9),
                  ("/blog/launch-week", 8), ("/contact", 7)],
        "refs": [("google", 34), ("", 20), ("twitter", 14), ("producthunt", 12),
                 ("linkedin", 10), ("hackernews", 10)],
    },
    "docs": {
        "base": 260, "growth": 1.1, "product": False, "ids": False,
        "pages": [("/getting-started", 26), ("/api/reference", 22), ("/guides/install", 16),
                  ("/guides/deploy", 12), ("/faq", 10), ("/api/webhooks", 8), ("/changelog", 6)],
        "refs": [("google", 46), ("", 24), ("github", 16), ("stackoverflow", 8), ("reddit", 6)],
    },
    "app": {
        "base": 40, "growth": 1.6, "product": True, "app": 55, "ids": True,
        "pages": [("/dashboard", 34), ("/settings", 16), ("/reports", 15), ("/billing", 12),
                  ("/team", 12), ("/integrations", 11)],
        "refs": [("", 62), ("google", 18), ("email", 12), ("slack", 8)],
    },
    "legacy": {
        "base": 90, "growth": -0.32, "product": False, "ids": False,
        "pages": [("/2019/hello-world", 28), ("/2020/lessons", 22), ("/2021/roadmap", 18),
                  ("/archive", 17), ("/about", 15)],
        "refs": [("google", 52), ("", 26), ("twitter", 12), ("reddit", 10)],
    },
}

COUNTRIES = [("US", 30), ("GB", 12), ("DE", 11), ("FR", 8), ("CA", 8),
             ("NL", 6), ("IN", 9), ("AU", 6), ("SE", 5), ("BR", 5)]
DEVICES = [("desktop", 60), ("mobile", 33), ("tablet", 7)]
# Vocabulary values, as a client declares them: lower-case, exactly the
# tokens internal/enrich/ua.go closes on.
BROWSERS = [("chrome", 48), ("safari", 26), ("firefox", 13), ("edge", 9), ("other", 4)]
OSES = [("macos", 32), ("windows", 30), ("ios", 18), ("android", 13), ("linux", 7)]
UTMS = [(("", "", ""), 62), (("twitter", "social", "launch"), 14),
        (("newsletter", "email", "weekly"), 10),
        (("producthunt", "referral", "launch"), 8), (("google", "cpc", "brand"), 6)]
EVENTS = [("signup", 12), ("activated", 8), ("subscribed", 4), ("invite_sent", 6), ("export", 5)]
PLANS = [("free", 60), ("pro", 30), ("team", 10)]
TEAM_SIZES = [("1", 40), ("2-5", 30), ("6-20", 18), ("21-100", 9), ("100+", 3)]
ROLES = [("member", 55), ("admin", 25), ("owner", 20)]

# Web Vitals: (name, measure, median, log-normal sigma). Mobile page loads
# are slower (MOBILE_SLOWDOWN on the time vitals). CLS is exactly 0 on
# CLS_ZERO of page loads and mostly under 0.1 otherwise.
VITALS = [("$lcp", "time", 1900, 0.45), ("$inp", "time", 140, 0.6),
          ("$cls", "number", 0.03, 0.9), ("$fcp", "time", 1200, 0.4),
          ("$ttfb", "time", 420, 0.5)]
VITALS_SHARE = 0.3       # the page views that report vitals (data-vitals="0.3")
MOBILE_SLOWDOWN = 1.4
CLS_ZERO = 0.4

# App dimensions. Versions are dated so the adoption chart shows one release
# superseding another rather than a flat stack.
SCREENS = [("/dashboard", 30), ("/settings", 18), ("/reports", 16), ("/inbox", 14),
           ("/billing", 12), ("/onboarding", 10)]
BROWSER_VERSIONS = ["126", "127", "128"]
DISPLAYS = [(1920, 1080), (1440, 900), (390, 844), (2560, 1440), (360, 800)]
PLATFORMS = [("ios", 55), ("android", 45)]
DEVICE_MODELS = {
    "ios": [("iPhone15,2", 34), ("iPhone14,5", 26), ("iPhone13,3", 20), ("iPad13,1", 12),
            ("iPhone12,1", 8)],
    "android": [("Pixel 8", 30), ("SM-S918B", 26), ("Pixel 7a", 20), ("SM-A546B", 14),
                ("moto g84", 10)],
}
OS_VERSIONS = {"ios": [("17.2", 55), ("16.6", 30), ("18.0", 15)],
               "android": [("14", 50), ("13", 35), ("15", 15)]}
LOCALES = [("en-US", 44), ("en-GB", 14), ("de-DE", 12), ("fr-FR", 10), ("pt-BR", 8),
           ("es-ES", 7), ("sv-SE", 5)]
# (version, days before today it started shipping)
APP_VERSIONS = [("2.2.0", 180), ("2.3.0", 96), ("2.4.1", 34)]
GROUPS = [("org-northwind", "Northwind Traders"), ("org-globex", "Globex"),
          ("org-initech", "Initech"), ("org-hooli", "Hooli"), ("org-acme", "Acme Corp")]
FIRST_NAMES = ["Ada", "Grace", "Alan", "Katherine", "Linus", "Barbara", "Ken", "Radia",
               "Dennis", "Margaret"]
LAST_NAMES = ["Lovelace", "Hopper", "Turing", "Johnson", "Torvalds", "Liskov", "Thompson",
              "Perlman", "Ritchie", "Hamilton"]


def pick(weighted):
    """Choose one value from a list of (value, weight) pairs."""
    return random.choices([v for v, _ in weighted], weights=[w for _, w in weighted], k=1)[0]


def actor_for(name, day, n, sends_ids):
    """A stable id when the client sends one, a per-day hash otherwise.

    This mirrors the server: an actor that arrives without an id is a hash
    of the connection under a key that rotates at midnight, so it cannot be
    followed across days and is never cohorted.
    """
    if sends_ids:
        return f"install-{name}-{n}"
    return hashlib.sha256(f"{name}-{day}-{n}".encode()).hexdigest()[:16]


def growing_pool(elapsed, final):
    """A stable id number from a pool that grows across the window.

    Drawing from a fixed pool would introduce every id within the first few
    weeks, leaving the recent days without a single new cohort -- and the
    retention page, which reads the last 90 days, empty or not depending on
    which weekday the window happens to start on. Growing the pool keeps
    newcomers arriving every day while most draws are returning ids.
    """
    return random.randint(1, max(1, final * (elapsed + 1) // DAYS))


# The text columns of an events row (each stored as a strings id, '' being 0)
# and the id columns (stored as tagged bytes when they are UUIDs or hex).
TEXT_COLS = ["event_name", "measure", "kind", "actor_kind", "host", "path", "referrer_source",
             "utm_source", "utm_medium", "utm_campaign", "platform", "os", "os_version",
             "os_name", "browser", "browser_version", "browser_locale", "app_version",
             "app_locale", "device", "device_model", "country"]
ID_COLS = ["actor_id", "user_id", "group_id", "session_id"]

# The rows as the script generates them: the events table as it was before
# migration 041, text throughout, so the generators below stay readable.
STAGING_SQL = f"""
CREATE TEMP TABLE IF NOT EXISTS seed_events (
    family TEXT NOT NULL, project_id INTEGER NOT NULL, day TEXT NOT NULL,
    id TEXT NOT NULL, ts TEXT NOT NULL, received_at TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL, user_id TEXT NOT NULL DEFAULT '',
    group_id TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '',
    {', '.join(f"{c} TEXT NOT NULL DEFAULT ''" for c in TEXT_COLS if c != 'platform')},
    platform TEXT NOT NULL DEFAULT 'unknown',
    display_width INTEGER NOT NULL DEFAULT 0, display_height INTEGER NOT NULL DEFAULT 0,
    consent INTEGER, attributes TEXT NOT NULL DEFAULT '{{}}',
    value REAL, sample_rate REAL NOT NULL DEFAULT 1)
"""


def _is_uuid(col):
    """SQL: col is a lowercase canonical UUID (encode.go uuidBytes)."""
    return (f"(length({col}) = 36 AND substr({col}, 9, 1) = '-' AND substr({col}, 14, 1) = '-'"
            f" AND substr({col}, 19, 1) = '-' AND substr({col}, 24, 1) = '-'"
            f" AND length(replace({col}, '-', '')) = 32 AND replace({col}, '-', '') NOT GLOB '*[^0-9a-f]*')")


def _enc_event_id(col):
    return f"CASE WHEN {_is_uuid(col)} THEN unhex(replace({col}, '-', '')) ELSE {col} END"


def _enc_id(col):
    """encode.go encodeID: x'01'+16 bytes for a UUID, x'02'+bytes for
    non-empty even-length lowercase hex, the text otherwise."""
    return (f"CASE WHEN {_is_uuid(col)} THEN unhex('01' || replace({col}, '-', ''))"
            f" WHEN {col} <> '' AND length({col}) % 2 = 0 AND {col} NOT GLOB '*[^0-9a-f]*'"
            f" THEN unhex('02' || {col}) ELSE {col} END")


def _enc_str(col):
    return f"COALESCE((SELECT id FROM strings WHERE value = s.{col}), 0)"


def encode_staged(cur):
    """Move the staged rows into events, encoded, and empty the staging table.

    The dictionary rows go first, as dict.go resolves them: a new text, key or
    value takes the next id (or number), a known one only raises
    last_seen_day. Inserting only the new ones, then raising the rest, keeps
    a re-run from burning AUTOINCREMENT ids on upserts that only update.
    Then the rows, which look every id up.
    """
    strings = " UNION ALL ".join(f"SELECT {c} AS v, day FROM seed_events" for c in TEXT_COLS)
    cur.execute("CREATE TEMP TABLE seed_dict (key_id INTEGER NOT NULL, value TEXT NOT NULL, day TEXT NOT NULL,"
                " PRIMARY KEY (key_id, value))")
    # strings (key_id 0), then catalog keys, then each key's values.
    cur.execute(f"INSERT INTO seed_dict SELECT 0, v, MAX(day) FROM ({strings}) WHERE v <> '' GROUP BY v")
    cur.execute("""INSERT INTO strings (value, last_seen_day)
        SELECT value, day FROM seed_dict t
        WHERE NOT EXISTS (SELECT 1 FROM strings x WHERE x.value = t.value) ORDER BY value""")
    cur.execute("""UPDATE strings SET last_seen_day = max(last_seen_day, t.day)
        FROM seed_dict t WHERE strings.value = t.value""")
    cur.execute("DELETE FROM seed_dict")

    cur.execute("""INSERT INTO project_attributes (project_id, key, last_seen_day)
        SELECT s.project_id, j.key, MAX(s.day) FROM seed_events s, json_each(s.attributes) j
        WHERE NOT EXISTS (SELECT 1 FROM project_attributes k WHERE k.project_id = s.project_id AND k.key = j.key)
        GROUP BY s.project_id, j.key ORDER BY s.project_id, j.key""")
    cur.execute("""UPDATE project_attributes SET last_seen_day = max(last_seen_day, t.day)
        FROM (SELECT s.project_id, j.key, MAX(s.day) AS day FROM seed_events s, json_each(s.attributes) j
              GROUP BY s.project_id, j.key) t
        WHERE project_attributes.project_id = t.project_id AND project_attributes.key = t.key""")

    cur.execute("""INSERT INTO seed_dict SELECT k.id, j.value, MAX(s.day)
        FROM seed_events s, json_each(s.attributes) j
        JOIN project_attributes k ON k.project_id = s.project_id AND k.key = j.key
        GROUP BY k.id, j.value""")
    cur.execute("""UPDATE attribute_values SET last_seen_day = max(last_seen_day, t.day)
        FROM seed_dict t WHERE attribute_values.key_id = t.key_id AND attribute_values.value = t.value""")
    # A new value takes the key's counter, which the attribute_values_next
    # trigger raises past it.
    cur.execute("""INSERT INTO attribute_values (key_id, n, value, last_seen_day)
        SELECT t.key_id, k.values_next + ROW_NUMBER() OVER (PARTITION BY t.key_id ORDER BY t.value) - 1,
               t.value, t.day
        FROM seed_dict t JOIN project_attributes k ON k.id = t.key_id
        WHERE NOT EXISTS (SELECT 1 FROM attribute_values v WHERE v.key_id = t.key_id AND v.value = t.value)""")
    cur.execute("DROP TABLE seed_dict")

    ids = ", ".join(_enc_id(f"s.{c}") for c in ID_COLS)
    texts = ", ".join(_enc_str(c) for c in TEXT_COLS)
    cur.execute(f"""INSERT INTO events (family, project_id, day, id, ts, received_at, actor_id, user_id,
        group_id, session_id, {', '.join(TEXT_COLS)}, display_width, display_height, consent,
        attributes, value, sample_rate)
        SELECT CASE s.family WHEN 'views' THEN 1 WHEN 'product' THEN 2 WHEN 'measures' THEN 3 END,
               s.project_id, s.day, {_enc_event_id('s.id')}, unixepoch(s.ts),
               CASE WHEN s.received_at = '' THEN 0 ELSE unixepoch(s.received_at) END,
               {ids}, {texts}, s.display_width, s.display_height, s.consent,
               (SELECT COALESCE(json_group_object(CAST(k.id AS TEXT), v.n), '{{}}')
                  FROM json_each(s.attributes) j
                  JOIN project_attributes k ON k.project_id = s.project_id AND k.key = j.key
                  JOIN attribute_values v ON v.key_id = k.id AND v.value = j.value),
               s.value, s.sample_rate
        FROM seed_events s""")
    cur.execute("DELETE FROM seed_events")


def seed(cur, pid, name, profile, today, sends_ids):
    for table in ("events", "actors", "identities"):
        cur.execute(f"DELETE FROM {table} WHERE project_id = ?", (pid,))
    cur.execute(STAGING_SQL)

    hits = 0
    for back in range(DAYS - 1, -1, -1):
        day = today - datetime.timedelta(days=back)
        elapsed = DAYS - 1 - back
        base = max(3.0, profile["base"] + elapsed * profile["growth"])
        if day.weekday() >= 5:
            base *= 0.62
        visitors = max(2, int(random.gauss(base, base * 0.16)))

        for v in range(visitors):
            # Web installs are numbered after the 400 signed-in users, so an
            # early web visit never claims a user's id (and its first seen day).
            vh = actor_for(name, day, 400 + growing_pool(elapsed, 1200) if sends_ids else v, sends_ids)
            device = pick(DEVICES)
            country = pick(COUNTRIES)
            browser = pick(BROWSERS)
            osname = pick(OSES)
            ref = pick(profile["refs"])
            us, um, uc = pick(UTMS)
            start = random.randint(7, 21) * 3600 + random.randint(0, 3599)
            for p in range(random.randint(1, 3 if device == "mobile" else 5)):
                ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                    seconds=start + p * random.randint(20, 600))
                path = pick(profile["pages"])
                cur.execute(
                    "INSERT INTO seed_events (id, project_id, ts, received_at, kind, actor_id, actor_kind,"
                    " user_id, group_id, path, referrer_source, country, device, browser,"
                    " browser_version, platform, os, utm_source, utm_medium, utm_campaign,"
                    " display_width, display_height, consent, day, family, event_name)"
                    " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'views','$page_view')",
                    (str(uuid.uuid4()), pid, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                     ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "web", vh,
                     "install" if sends_ids else "connection", "", "",
                     path, ref, country, device, browser,
                     random.choice(BROWSER_VERSIONS),
                     "web", osname, us, um, uc, *random.choice(DISPLAYS),
                     1 if sends_ids else random.choice([0, 0, 1]), day.isoformat()))
                hits += 1
                if profile.get("measures") and random.random() < VITALS_SHARE:
                    seed_vitals(cur, pid, ts, vh, sends_ids, path, country, device, browser, osname)

    events = 0
    if profile["product"]:
        for back in range(DAYS - 1, -1, -1):
            day = today - datetime.timedelta(days=back)
            scale = 1 + (DAYS - 1 - back) / 120
            for event_name, weight in EVENTS:
                for _ in range(max(0, int(random.gauss(weight * scale * 0.5, weight * 0.3)))):
                    ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                        seconds=random.randint(0, 86399))
                    n = growing_pool(DAYS - 1 - back, 400)
                    user = f"user-{name}-{n}" if sends_ids else ""
                    # This is the only event loop and it sits beside the web
                    # views above, so every product event is web-originated:
                    # platform web, and an OS drawn from the same web mix.
                    cur.execute(
                        "INSERT INTO seed_events (id, project_id, ts, day, received_at, event_name,"
                        " actor_id, actor_kind, user_id, group_id, platform, os, app_version,"
                        " attributes, consent, family) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'product')",
                        (str(uuid.uuid4()), pid, ts.strftime("%Y-%m-%dT%H:%M:%SZ"), day.isoformat(),
                         ts.strftime("%Y-%m-%dT%H:%M:%SZ"), event_name,
                         actor_for(name, day, n, sends_ids),
                         "user" if sends_ids else "connection", user,
                         GROUPS[n % len(GROUPS)][0] if sends_ids else "",
                         "web", pick(OSES), "", json.dumps({"plan": pick(PLANS), "team_size": pick(TEAM_SIZES), "role": pick(ROLES)}),
                         1 if sends_ids else random.choice([0, 0, 1])))
                    events += 1

    if profile.get("measures"):
        seed_backend(cur, pid, profile, today)
    views = hits + (seed_app(cur, pid, name, profile, today, sends_ids) if profile.get("app") else 0)
    encode_staged(cur)
    if sends_ids:
        seed_identities(cur, pid, name)
    return views, events


def insert_measure(cur, pid, ts, name, measure, value, actor, actor_kind, **dims):
    """One measures row, as ingest stores it: sample_rate 1 (the page load
    was sampled in whole), the day from the timestamp, and bucket left to
    the generated column."""
    cols = ["id", "project_id", "ts", "received_at", "day", "family", "event_name",
            "value", "measure", "sample_rate", "actor_id", "actor_kind", *dims]
    stamp = ts.strftime("%Y-%m-%dT%H:%M:%SZ")
    vals = [str(uuid.uuid4()), pid, stamp, stamp, ts.date().isoformat(), "measures", name,
            value, measure, 1, actor, actor_kind, *dims.values()]
    cur.execute(f"INSERT INTO seed_events ({', '.join(cols)}) VALUES ({', '.join('?' * len(cols))})", vals)


def seed_vitals(cur, pid, ts, actor, sends_ids, path, country, device, browser, osname):
    """The five Web Vitals of one page load, log-normal around typical values."""
    for name, measure, median, sigma in VITALS:
        if name == "$cls" and random.random() < CLS_ZERO:
            value = 0.0
        else:
            value = random.lognormvariate(math.log(median), sigma)
            if measure == "time" and device == "mobile":
                value *= MOBILE_SLOWDOWN
            value = round(value, 4 if measure == "number" else 1)
        insert_measure(cur, pid, ts + datetime.timedelta(seconds=2), name, measure, value,
                       actor, "install" if sends_ids else "connection",
                       kind="web", platform="web", path=path, country=country,
                       device=device, browser=browser, os=osname)


def seed_backend(cur, pid, profile, today):
    """A backend's checkout_api timing: a few dozen requests a day, growing
    with the site, with a slow tail."""
    for back in range(DAYS - 1, -1, -1):
        day = today - datetime.timedelta(days=back)
        n = max(3, int(random.gauss(20 + (DAYS - 1 - back) * profile["growth"], 5)))
        for _ in range(n):
            ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                seconds=random.randint(0, 86399))
            value = round(random.lognormvariate(math.log(320), 0.35), 1)
            insert_measure(cur, pid, ts, "checkout_api", "time", value,
                           hashlib.sha256(f"backend-{day}".encode()).hexdigest()[:16], "connection")


def seed_app(cur, pid, name, profile, today, sends_ids):
    """Screen views across two platforms, with versions rolling out over time.

    A native app's platform and OS are the same token (ios, android), which
    is what the app-versions dashboard keys on. Browser and device are
    unknown: a native client declares neither, and unknown -- not '' -- is
    what the server records for an undeclared value.
    """
    views = 0
    for back in range(DAYS - 1, -1, -1):
        day = today - datetime.timedelta(days=back)
        elapsed = DAYS - 1 - back
        base = max(3.0, profile["app"] + elapsed * profile["growth"] * 0.6)
        if day.weekday() >= 5:
            base *= 0.78          # apps dip less at the weekend than websites
        # Only versions already shipped on this day are in circulation, and the
        # newest one takes an increasing share as adoption ramps.
        live = [(v, d) for v, d in APP_VERSIONS if d >= back]
        if not live:
            live = [APP_VERSIONS[0]]
        weights = [(v, max(1, 40 - (d - back))) for v, d in live]

        for i in range(max(2, int(random.gauss(base, base * 0.14)))):
            n = growing_pool(elapsed, 400) if sends_ids else i
            actor = actor_for(name, day, n, sends_ids)
            platform = pick(PLATFORMS)
            version = pick(weights)
            session = str(uuid.uuid4())
            # The app is translated into the device language's family, or
            # falls back to English when it is not.
            browser_locale = pick(LOCALES)
            app_locale = browser_locale[:2] if random.random() < 0.7 else "en"
            start = random.randint(6, 22) * 3600 + random.randint(0, 3599)
            for s in range(random.randint(1, 6)):
                ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                    seconds=start + s * random.randint(15, 240))
                cur.execute(
                    "INSERT INTO seed_events (id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id,"
                    " group_id, session_id, path, platform, os, app_version, os_version,"
                    " browser, device, device_model, browser_locale, app_locale, country, consent,"
                    " day, family, event_name)"
                    " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'views','$screen_view')",
                    (str(uuid.uuid4()), pid, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                     ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "app", actor,
                     "user" if sends_ids else "connection",
                     f"user-{name}-{n}" if sends_ids else "",
                     GROUPS[n % len(GROUPS)][0] if sends_ids else "",
                     session, pick(SCREENS), platform, platform, version,
                     pick(OS_VERSIONS[platform]), "unknown", "unknown",
                     pick(DEVICE_MODELS[platform]),
                     browser_locale, app_locale, pick(COUNTRIES),
                     1 if sends_ids else random.choice([0, 1]), day.isoformat()))
                views += 1
    return views


def seed_identities(cur, pid, name):
    """Display names for the users and groups the seeded rows reference."""
    for gid, gname in GROUPS:
        cur.execute("INSERT OR REPLACE INTO identities (project_id, kind, id, name,"
                    " last_seen_day, updated_at) VALUES (?,?,?,?,?,datetime('now'))",
                    (pid, "group", gid, gname, ""))
    for n in range(1, 401):
        display = f"{FIRST_NAMES[n % len(FIRST_NAMES)]} {LAST_NAMES[(n // 7) % len(LAST_NAMES)]}"
        cur.execute("INSERT OR REPLACE INTO identities (project_id, kind, id, name,"
                    " last_seen_day, updated_at) VALUES (?,?,?,?,?,datetime('now'))",
                    (pid, "user", f"user-{name}-{n}", display, ""))


def main():
    db = sys.argv[1]
    con = sqlite3.connect(db)
    cur = con.cursor()
    projects = cur.execute("select id, name from projects order by id").fetchall()
    unknown = [name for _, name in projects if name not in PROFILES]
    if unknown:
        sys.exit(f"no traffic profile for {', '.join(unknown)}; add one to PROFILES (keyed by project name)")

    # views.ts is UTC and the dashboards window on SQLite's date('now'),
    # which is also UTC -- anchor the seeded range to the same clock so the
    # narrowest range (last 1 day) lands on a day that has rows.
    today = datetime.datetime.now(datetime.timezone.utc).date()

    random.seed(1337)
    for pid, name in projects:
        views, events = seed(cur, pid, name, PROFILES[name], today, PROFILES[name]["ids"])
        print(f"  {pid:<3} {name:<10} views={views:<7} events={events:<6} "
              f"({'ids' if PROFILES[name]['ids'] else 'no ids'})")
    con.commit()
    con.close()


if __name__ == "__main__":
    main()
