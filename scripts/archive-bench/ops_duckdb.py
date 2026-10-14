"""Design C's write side on a copy of the DuckDB file: appends while queries
run, retention (delete the oldest month) and erasure (delete one actor), and
the file size after each.

    python ops_duckdb.py events.duckdb queries_duckdb.sql <actor>
"""

import os
import shutil
import statistics
import sys
import threading
import time
import uuid

import duckdb

src, qfile, actor = sys.argv[1], sys.argv[2], sys.argv[3]
path = "ops.duckdb"
shutil.copy(src, path)


def size():
    return os.path.getsize(path) / 1e6


def query(name, frm, to):
    text = open(qfile).read()
    for part in text.split("-- name: ")[1:]:
        n, body = part.split("\n", 1)
        if n.strip() == name:
            return body.replace("{from}", frm).replace("{to}", to)
    raise KeyError(name)


con = duckdb.connect(path)
con.execute("SET threads=2; SET memory_limit='256MB'")
print(f"file {size():.0f} MB, rows {con.execute('SELECT count(*) FROM events').fetchone()[0]}")

# A template batch: 500 recent rows, re-keyed and re-stamped on each append.
con.execute("CREATE TABLE tmpl AS SELECT * FROM events WHERE day = DATE '2026-10-12' LIMIT 500")
cols = [r[0] for r in con.execute("DESCRIBE events").fetchall()]
sel = ", ".join("uuid() AS id" if c == "id" else "now()::TIMESTAMP AS ts" if c == "ts" else c for c in cols)


def run_queries(cur, names, frm, to, reps):
    out = {}
    for n in names:
        q = query(n, frm, to)
        ts = []
        for _ in range(reps):
            t = time.perf_counter()
            cur.execute(q).fetchall()
            ts.append(time.perf_counter() - t)
        out[n] = min(ts)
    return out


names = ["sessions_bounce", "top_pages", "two_attrs", "dau"]
frm, to = "2025-10-13", "2026-10-12"
print("1-year queries, no writes:", {k: f"{v:.3f}s" for k, v in run_queries(con.cursor(), names, frm, to, 3).items()})

stop = threading.Event()
lat = []


def appender():
    cur = con.cursor()
    while not stop.is_set():
        t = time.perf_counter()
        cur.execute(f"INSERT INTO events SELECT {sel} FROM tmpl")
        lat.append(time.perf_counter() - t)
        time.sleep(0.2)


th = threading.Thread(target=appender)
th.start()
busy = run_queries(con.cursor(), names, frm, to, 3)
stop.set()
th.join()
con.execute("DROP TABLE tmpl")
print("1-year queries, a 500-row append every 0.2 s:", {k: f"{v:.3f}s" for k, v in busy.items()})
print(f"appends {len(lat)}: p50 {statistics.median(lat)*1000:.1f} ms, max {max(lat)*1000:.1f} ms")

con.execute("CHECKPOINT")
before = size()
t = time.perf_counter()
n = con.execute("DELETE FROM events WHERE day < DATE '2023-11-13'").fetchone()[0]
con.execute("CHECKPOINT")
print(f"retention: deleted {n} rows (the first month) in {time.perf_counter()-t:.2f}s; file {before:.0f} -> {size():.0f} MB")

t = time.perf_counter()
n = con.execute("DELETE FROM events WHERE actor_id = ?", [actor]).fetchone()[0]
con.execute("CHECKPOINT")
print(f"erasure: deleted {n} rows of one actor in {time.perf_counter()-t:.2f}s; file {size():.0f} MB")

t = time.perf_counter()
con.execute("VACUUM")
con.execute("CHECKPOINT")
print(f"VACUUM in {time.perf_counter()-t:.2f}s; file {size():.0f} MB")
con.close()

# Space comes back only by rewriting: copy into a fresh file.
t = time.perf_counter()
fresh = duckdb.connect("fresh.duckdb")
fresh.execute(f"ATTACH '{path}' AS o (READ_ONLY); CREATE TABLE events AS SELECT * FROM o.events; CHECKPOINT")
fresh.close()
print(f"rewrite into a fresh file in {time.perf_counter()-t:.2f}s; file {os.path.getsize('fresh.duckdb')/1e6:.0f} MB")
for p in (path, "fresh.duckdb"):
    os.remove(p)
