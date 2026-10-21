#!/usr/bin/env python3
"""Seed a local database with demo traffic for every project in the database.

For dashboard development only -- bypasses the HTTP API. The events go to
the event files as closed logs (raw/<project id>/<day>.1.closed, one NDJSON
line per event in the shape of internal/archive's log lines), which the first
start's pass dumps to Parquet; the database is only read, for the project
list. Deterministic (fixed seed), with per-project traffic profiles, a
growth trend and weekend dips so the charts show a believable shape.

Covers 180 days so the widest dashboard range has data to plot. Re-running
replaces the seeded closed logs of days the pass has not dumped yet; once a
start has dumped them, seed into an empty events directory.

The "dev" profile also sends Web Vitals from a share of its page views and
a backend's checkout_api timing, so the Web Vitals and Measures dashboards
have histograms to read.

Projects with an "app" profile also get screen views across two platforms and
three app versions, so the version-adoption chart has a rollout to show.
Projects whose profile sends ids ("ids": True) get stable actor ids plus
user and group names on every event of an identified user or group, which is
what makes the users, groups and retention pages meaningful, the others get a
per-day connection hash, as a site with an anonymous tag would.

    python3 scripts/seed-demo.py local/twillingate.db [--events-dir DIR]

DIR defaults to raw/ beside the database.

Projects are read from the projects table; each needs a traffic profile in
PROFILES, keyed by project name.
"""

import datetime
import hashlib
import json
import math
import os
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


LINE_VERSION = 1


def stamp(ts):
    return ts.strftime("%Y-%m-%dT%H:%M:%SZ")


class Sink:
    """The lines of one project, grouped by day, as internal/archive.line
    writes them: the non-empty fields only, attributes as attr_<key>."""

    def __init__(self, pid):
        self.pid = pid
        self.days = {}

    def add(self, family, name, ts, attributes=None, **fields):
        m = {"v": LINE_VERSION, "id": str(uuid.uuid4()), "project_id": self.pid,
             "family": family, "event_name": name, "ts": stamp(ts),
             "day": ts.date().isoformat(), "received_at": stamp(ts)}
        for k, v in fields.items():
            if v != "" and v is not None:
                m[k] = v
        m.setdefault("sample_rate", 1)
        for k, v in (attributes or {}).items():
            if not k.startswith("$"):
                m["attr_" + k] = v
        self.days.setdefault(m["day"], []).append(json.dumps(m, sort_keys=True))

    def write(self, events_dir):
        """<day>.import, then renamed to <day>.1.closed: a reader or a pass
        sees the whole closed log or none (archive.WriteClosed)."""
        d = os.path.join(events_dir, str(self.pid))
        os.makedirs(d, exist_ok=True)
        for day, lines in self.days.items():
            tmp = os.path.join(d, day + ".import")
            with open(tmp, "w") as f:
                f.write("\n".join(lines) + "\n")
                f.flush()
                os.fsync(f.fileno())
            os.chmod(tmp, 0o640)
            os.replace(tmp, os.path.join(d, day + ".1.closed"))


def user_name(name, n):
    return f"{FIRST_NAMES[n % len(FIRST_NAMES)]} {LAST_NAMES[(n // 7) % len(LAST_NAMES)]}"


def who(name, n):
    """The identity fields of signed-in user n of the project."""
    group_id, group_name = GROUPS[n % len(GROUPS)]
    return {"user_id": f"user-{name}-{n}", "user_name": user_name(name, n),
            "group_id": group_id, "group_name": group_name}


def seed(sink, name, profile, today, sends_ids):
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
                sink.add("views", "$page_view", ts, kind="web", actor_id=vh,
                         actor_kind="install" if sends_ids else "connection",
                         path=path, referrer_source=ref, country=country, device=device,
                         browser=browser, browser_version=random.choice(BROWSER_VERSIONS),
                         platform="web", os=osname, utm_source=us, utm_medium=um,
                         utm_campaign=uc,
                         **dict(zip(("display_width", "display_height"), random.choice(DISPLAYS))),
                         consent=1 if sends_ids else random.choice([0, 0, 1]))
                hits += 1
                if profile.get("measures") and random.random() < VITALS_SHARE:
                    seed_vitals(sink, ts, vh, sends_ids, path, country, device, browser, osname)

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
                    # This is the only event loop and it sits beside the web
                    # views above, so every product event is web-originated:
                    # platform web, and an OS drawn from the same web mix.
                    sink.add("product", event_name, ts,
                             {"plan": pick(PLANS), "team_size": pick(TEAM_SIZES), "role": pick(ROLES)},
                             actor_id=actor_for(name, day, n, sends_ids),
                             actor_kind="user" if sends_ids else "connection",
                             platform="web", os=pick(OSES),
                             consent=1 if sends_ids else random.choice([0, 0, 1]),
                             **(who(name, n) if sends_ids else {}))
                    events += 1

    if profile.get("measures"):
        seed_backend(sink, profile, today)
    views = hits + (seed_app(sink, name, profile, today, sends_ids) if profile.get("app") else 0)
    return views, events


def insert_measure(sink, ts, name, measure, value, actor, actor_kind, **dims):
    """One measures line, as ingest writes it: sample_rate 1 (the page load
    was sampled in whole)."""
    sink.add("measures", name, ts, actor_id=actor, actor_kind=actor_kind,
             measure=measure, value=value, **dims)


def seed_vitals(sink, ts, actor, sends_ids, path, country, device, browser, osname):
    """The five Web Vitals of one page load, log-normal around typical values."""
    for name, measure, median, sigma in VITALS:
        if name == "$cls" and random.random() < CLS_ZERO:
            value = 0.0
        else:
            value = random.lognormvariate(math.log(median), sigma)
            if measure == "time" and device == "mobile":
                value *= MOBILE_SLOWDOWN
            value = round(value, 4 if measure == "number" else 1)
        insert_measure(sink, ts + datetime.timedelta(seconds=2), name, measure, value,
                       actor, "install" if sends_ids else "connection",
                       kind="web", platform="web", path=path, country=country,
                       device=device, browser=browser, os=osname)


def seed_backend(sink, profile, today):
    """A backend's checkout_api timing: a few dozen requests a day, growing
    with the site, with a slow tail."""
    for back in range(DAYS - 1, -1, -1):
        day = today - datetime.timedelta(days=back)
        n = max(3, int(random.gauss(20 + (DAYS - 1 - back) * profile["growth"], 5)))
        for _ in range(n):
            ts = datetime.datetime.combine(day, datetime.time()) + datetime.timedelta(
                seconds=random.randint(0, 86399))
            value = round(random.lognormvariate(math.log(320), 0.35), 1)
            insert_measure(sink, ts, "checkout_api", "time", value,
                           hashlib.sha256(f"backend-{day}".encode()).hexdigest()[:16], "connection")


def seed_app(sink, name, profile, today, sends_ids):
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
                sink.add("views", "$screen_view", ts, kind="app", actor_id=actor,
                         actor_kind="user" if sends_ids else "connection",
                         session_id=session, path=pick(SCREENS), platform=platform,
                         os=platform, app_version=version,
                         os_version=pick(OS_VERSIONS[platform]), browser="unknown",
                         device="unknown", device_model=pick(DEVICE_MODELS[platform]),
                         browser_locale=browser_locale, app_locale=app_locale,
                         country=pick(COUNTRIES),
                         consent=1 if sends_ids else random.choice([0, 1]),
                         **(who(name, n) if sends_ids else {}))
                views += 1
    return views


def main():
    args = sys.argv[1:]
    events_dir = None
    if "--events-dir" in args:
        i = args.index("--events-dir")
        events_dir = args[i + 1]
        del args[i:i + 2]
    if len(args) != 1:
        sys.exit("usage: seed-demo.py <db> [--events-dir DIR]")
    db = args[0]
    if events_dir is None:
        events_dir = os.path.join(os.path.dirname(os.path.abspath(db)), "raw")
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    projects = con.execute("select id, name from projects order by id").fetchall()
    con.close()
    unknown = [name for _, name in projects if name not in PROFILES]
    if unknown:
        sys.exit(f"no traffic profile for {', '.join(unknown)}; add one to PROFILES (keyed by project name)")

    # Anchor the seeded range to the UTC clock the files' days use, so the
    # narrowest range (last 1 day) lands on a day that has rows.
    today = datetime.datetime.now(datetime.timezone.utc).date()

    random.seed(1337)
    for pid, name in projects:
        sink = Sink(pid)
        views, events = seed(sink, name, PROFILES[name], today, PROFILES[name]["ids"])
        sink.write(events_dir)
        print(f"  {pid:<3} {name:<10} views={views:<7} events={events:<6} "
              f"({'ids' if PROFILES[name]['ids'] else 'no ids'})")


if __name__ == "__main__":
    main()
