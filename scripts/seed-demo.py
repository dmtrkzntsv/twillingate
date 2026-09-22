#!/usr/bin/env python3
"""Seed a local database with demo traffic for every project in the database.

For dashboard development only -- writes straight to SQLite, bypassing the
HTTP API. Deterministic (fixed seed), with per-project traffic profiles, a
growth trend and weekend dips so the charts show a believable shape.

Covers 180 days so the widest dashboard range has data to plot. Idempotent:
seeded rows are cleared before reinsertion, so re-running refreshes the window
rather than stacking another copy on top.

Projects with an "app" profile also get screen views across two platforms and
three app versions, so the version-adoption chart has a rollout to show.
Projects in identified mode get stable actor ids plus user and group
identities, which is what makes the users, groups and retention pages
meaningful -- under anonymous mode those ids would rotate daily and the pages
render their explanatory branch instead.

    python3 scripts/seed-demo.py local/twillingate.db

Projects are read from the projects table; each needs a traffic profile in
PROFILES, keyed by project name.
"""

import datetime
import hashlib
import json
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
        "base": 18, "growth": 0.45, "product": True, "app": 12,
        "pages": [("/", 30), ("/pricing", 18), ("/docs", 15), ("/docs/quickstart", 10),
                  ("/blog/launch", 9), ("/blog/why-privacy", 7), ("/about", 6), ("/changelog", 5)],
        "refs": [("", 30), ("google", 25), ("hackernews", 15), ("reddit", 10),
                 ("twitter", 8), ("producthunt", 7), ("github", 5)],
    },
    "marketing": {
        "base": 120, "growth": 2.4, "product": False,
        "pages": [("/", 40), ("/pricing", 22), ("/features", 14), ("/customers", 9),
                  ("/blog/launch-week", 8), ("/contact", 7)],
        "refs": [("google", 34), ("", 20), ("twitter", 14), ("producthunt", 12),
                 ("linkedin", 10), ("hackernews", 10)],
    },
    "docs": {
        "base": 260, "growth": 1.1, "product": False,
        "pages": [("/getting-started", 26), ("/api/reference", 22), ("/guides/install", 16),
                  ("/guides/deploy", 12), ("/faq", 10), ("/api/webhooks", 8), ("/changelog", 6)],
        "refs": [("google", 46), ("", 24), ("github", 16), ("stackoverflow", 8), ("reddit", 6)],
    },
    "app": {
        "base": 40, "growth": 1.6, "product": True, "app": 55,
        "pages": [("/dashboard", 34), ("/settings", 16), ("/reports", 15), ("/billing", 12),
                  ("/team", 12), ("/integrations", 11)],
        "refs": [("", 62), ("google", 18), ("email", 12), ("slack", 8)],
    },
    "legacy": {
        "base": 90, "growth": -0.32, "product": False,
        "pages": [("/2019/hello-world", 28), ("/2020/lessons", 22), ("/2021/roadmap", 18),
                  ("/archive", 17), ("/about", 15)],
        "refs": [("google", 52), ("", 26), ("twitter", 12), ("reddit", 10)],
    },
}

COUNTRIES = [("US", 30), ("GB", 12), ("DE", 11), ("FR", 8), ("CA", 8),
             ("NL", 6), ("IN", 9), ("AU", 6), ("SE", 5), ("BR", 5)]
DEVICES = [("desktop", 60), ("mobile", 33), ("tablet", 7)]
BROWSERS = [("Chrome", 48), ("Safari", 26), ("Firefox", 13), ("Edge", 9), ("Other", 4)]
OSES = [("macOS", 32), ("Windows", 30), ("iOS", 18), ("Android", 13), ("Linux", 7)]
UTMS = [(("", "", ""), 62), (("twitter", "social", "launch"), 14),
        (("newsletter", "email", "weekly"), 10),
        (("producthunt", "referral", "launch"), 8), (("google", "cpc", "brand"), 6)]
EVENTS = [("signup", 12), ("activated", 8), ("subscribed", 4), ("invite_sent", 6), ("export", 5)]
PLANS = [("free", 60), ("pro", 30), ("team", 10)]

# App dimensions. Versions are dated so the adoption chart shows one release
# superseding another rather than a flat stack.
SCREENS = [("/dashboard", 30), ("/settings", 18), ("/reports", 16), ("/inbox", 14),
           ("/billing", 12), ("/onboarding", 10)]
BROWSER_VERSIONS = ["126", "127", "128"]
DISPLAYS = [(1920, 1080), (1440, 900), (390, 844), (2560, 1440), (360, 800)]
PLATFORMS = [("iOS", 55), ("Android", 45)]
DEVICE_MODELS = {
    "iOS": [("iPhone15,2", 34), ("iPhone14,5", 26), ("iPhone13,3", 20), ("iPad13,1", 12),
            ("iPhone12,1", 8)],
    "Android": [("Pixel 8", 30), ("SM-S918B", 26), ("Pixel 7a", 20), ("SM-A546B", 14),
                ("moto g84", 10)],
}
OS_VERSIONS = {"iOS": [("17.2", 55), ("16.6", 30), ("18.0", 15)],
               "Android": [("14", 50), ("13", 35), ("15", 15)]}
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


def actor_for(name, day, n, identified):
    """A stable id in identified mode, a per-day hash otherwise.

    This mirrors what the server does: anonymous projects salt the identifier
    with a key that rotates at midnight, so an actor cannot be followed across
    days and cohorts are undefined for them.
    """
    if identified:
        return f"install-{name}-{n}"
    return hashlib.sha256(f"{name}-{day}-{n}".encode()).hexdigest()[:16]


def seed(cur, pid, name, profile, today, identified):
    for table in ("views", "events", "actors", "identities"):
        cur.execute(f"DELETE FROM {table} WHERE project_id = ?", (pid,))

    hits = 0
    for back in range(DAYS - 1, -1, -1):
        day = today - datetime.timedelta(days=back)
        elapsed = DAYS - 1 - back
        base = max(3.0, profile["base"] + elapsed * profile["growth"])
        if day.weekday() >= 5:
            base *= 0.62
        visitors = max(2, int(random.gauss(base, base * 0.16)))

        for v in range(visitors):
            vh = actor_for(name, day, v, identified)
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
                cur.execute(
                    "INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind,"
                    " user_id, group_id, path, referrer_source, country, device, browser,"
                    " browser_version, os, utm_source, utm_medium, utm_campaign, display_width, display_height)"
                    " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (str(uuid.uuid4()), pid, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                     ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "web", vh,
                     "install" if identified else "connection", "", "",
                     pick(profile["pages"]), ref, country, device, browser,
                     random.choice(BROWSER_VERSIONS),
                     osname, us, um, uc, *random.choice(DISPLAYS)))
                hits += 1

    events = 0
    if profile["product"]:
        for back in range(DAYS - 1, -1, -1):
            day = today - datetime.timedelta(days=back)
            scale = 1 + (DAYS - 1 - back) / 120
            for event_name, weight in EVENTS:
                for _ in range(max(0, int(random.gauss(weight * scale * 0.5, weight * 0.3)))):
                    ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                        seconds=random.randint(0, 86399))
                    n = random.randint(1, 400)
                    user = f"user-{name}-{n}" if identified else ""
                    cur.execute(
                        "INSERT INTO events (id, project_id, ts, received_at, event_name,"
                        " actor_id, actor_kind, user_id, group_id, os, app_version, attributes)"
                        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
                        (str(uuid.uuid4()), pid, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                         ts.strftime("%Y-%m-%dT%H:%M:%SZ"), event_name,
                         actor_for(name, day, n, identified),
                         "user" if identified else "connection", user,
                         GROUPS[n % len(GROUPS)][0] if identified else "",
                         "", "", json.dumps({"plan": pick(PLANS)})))
                    events += 1

    views = hits + (seed_app(cur, pid, name, profile, today, identified) if profile.get("app") else 0)
    if identified:
        seed_identities(cur, pid, name)
    return views, events


def seed_app(cur, pid, name, profile, today, identified):
    """Screen views across two platforms, with versions rolling out over time."""
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

        for n in range(max(2, int(random.gauss(base, base * 0.14)))):
            actor = actor_for(name, day, n, identified)
            platform = pick(PLATFORMS)
            version = pick(weights)
            session = str(uuid.uuid4())
            start = random.randint(6, 22) * 3600 + random.randint(0, 3599)
            for s in range(random.randint(1, 6)):
                ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                    seconds=start + s * random.randint(15, 240))
                cur.execute(
                    "INSERT INTO views (id, project_id, ts, received_at, kind, actor_id, actor_kind, user_id,"
                    " group_id, session_id, path, os, app_version, os_version,"
                    " device_model, locale, country) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (str(uuid.uuid4()), pid, ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
                     ts.strftime("%Y-%m-%dT%H:%M:%SZ"), "app", actor,
                     "user" if identified else "install",
                     f"user-{name}-{n}" if identified else "",
                     GROUPS[n % len(GROUPS)][0] if identified else "",
                     session, pick(SCREENS), platform, version,
                     pick(OS_VERSIONS[platform]), pick(DEVICE_MODELS[platform]),
                     pick(LOCALES), pick(COUNTRIES)))
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
    projects = cur.execute("select id, name, identity from projects order by id").fetchall()
    unknown = [name for _, name, _ in projects if name not in PROFILES]
    if unknown:
        sys.exit(f"no traffic profile for {', '.join(unknown)}; add one to PROFILES (keyed by project name)")

    # views.ts is UTC and the dashboards window on SQLite's date('now'),
    # which is also UTC -- anchor the seeded range to the same clock so the
    # narrowest range (last 1 day) lands on a day that has rows.
    today = datetime.datetime.now(datetime.timezone.utc).date()

    random.seed(1337)
    for pid, name, identity in projects:
        views, events = seed(cur, pid, name, PROFILES[name], today, identity == "identified")
        print(f"  {pid:<3} {name:<10} views={views:<7} events={events:<6} ({identity})")
    con.commit()
    con.close()


if __name__ == "__main__":
    main()
