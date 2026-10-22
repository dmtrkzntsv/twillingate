# Native engine measurement: results (scratch)

## Task 1: today's DuckDB baseline

Host: 4 CPUs, 30 GB. Engine at 2 threads, cache off, project 1, 12 months
(2025-10 to 2026-09) per volume, ranges ending 2026-09-30. Command:

    MEASURE_DIR=~/.cache/twbench-native/{1m,100k} MEASURE_MONTH_EVENTS={1000000,100000} \
      MEASURE_RUNS=1:30,1:365,2:30,2:365 GOTMPDIR=~/.cache/gotmp \
      go test -tags measure -run TestMeasureDashboards -v -timeout 2h ./internal/app

Wall is the best of 3 passes. "Seq" = one widget at a time (engine parallel 1),
"2 at a time" = engine parallel 2, "All" = all widgets at once as a browser
sends them. Slowest widget and peak RSS are from the Seq mode (the all-at-once
slowest is queueing, not the widget); child peak RSS is the engine child's.

| Dashboard | Range | Volume | Seq wall | 2 at a time | All at once | Slowest widget (Seq) | Child peak RSS (Seq / max of modes) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 30 d | 100k | 550 ms | 281 ms | 482 ms | visitors 40 ms | 101 / 119 MB |
| Views (20) | 365 d | 100k | 1.481 s | 1.061 s | 1.408 s | top-pages 214 ms | 194 / 218 MB |
| Product (10) | 30 d | 100k | 306 ms | 225 ms | 292 ms | attribute-values 134 ms | 142 / 186 MB |
| Product (10) | 365 d | 100k | 608 ms | 441 ms | 604 ms | attribute-values 193 ms | 194 / 200 MB |
| Views (20) | 30 d | 1M | 1.111 s | 833 ms | 1.100 s | top-pages 111 ms (visitors 111 ms) | 171 / 192 MB |
| Views (20) | 365 d | 1M | 10.902 s | 8.115 s | 10.870 s | top-pages 1.708 s | 853 / 853 MB |
| Product (10) | 30 d | 1M | 1.601 s | 1.454 s | 1.595 s | attribute-values 1.209 s | 729 / 738 MB |
| Product (10) | 365 d | 1M | 5.277 s | 5.000 s | 5.197 s | attribute-values 1.804 s | 812 / 965 MB |

Other timings: daily pass (derive sessions, identities, aliases) 1.864 s at
100k, 11.797 s at 1M. Generation 0.4 to 0.55 s a month at 100k, about 2.6 s a
month at 1M (the whole 1M run was 209 s, 100k 34 s). Files: 100k month about
4.1 MB, 1M month about 39.8 MB; `du` of each dir 53 MB and 501 MB.

Noise: the host's load average was 3.0 to 3.9 throughout (another process was
running), so these are pessimistic for a 4-CPU box and the 100k numbers are
more affected in relative terms. The 100k run's all-at-once passes are not
slower than seq by much; read them as "about the same".

## Row groups (2026-09, project 1; DuckDB `parquet_metadata`)

| Volume | Rows in month | Row groups | Rows per group |
| --- | --- | --- | --- |
| 100k | 99,047 | 1 | 99,047 |
| 1M | 990,987 | 9 | 122,880 x 8, then 7,947 |

Gathered with the DuckDB Python module (2.0.0) from a venv at
`~/.cache/twbench-native/venv` (no duckdb CLI on the host):
`SELECT row_group_id, max(row_group_num_rows) FROM parquet_metadata('...') GROUP BY 1`.

## Generated data (kept for later tasks)

`~/.cache/twbench-native/100k/raw/1/` and `~/.cache/twbench-native/1m/raw/1/`:
`2025-10.parquet` to `2026-09.parquet`, plus `aliases.parquet`,
`identities.parquet` and `sessions/` (derived). Full logs: `100k.log`,
`1m.log` beside them.
