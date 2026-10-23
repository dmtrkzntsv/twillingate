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

Product widgets users-by-app-version and app-versions return 0 rows in this
generated data at both volumes (see `100k.log` and `1m.log`), so their timings
are of empty results.

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

## Task 5: native scans and writes (matrix)

Started 2026-10-22T06:06:20Z. Every cell is a fresh `native-bench query` process with GOMAXPROCS=2; each runs 3 times: "first" is run 1, "best" the minimum of runs 2 to 3 (each metric on its own). CPU is user+sys of the query alone; peak RSS is the process's VmHWM. Files passed are the months overlapping the range (1 for 30 d, 12 for 365 d). Layouts: `duckdb` = the original files; `<lib>-1d` / `<lib>-4d` = rewritten by that library with row groups of PerMonth/30 / PerMonth*4/30 rows. Rows = result rows; every cell of a volume x query x range must hash to the same rows.

### Volume 100k (100000 events a month)

Rewrites (12 month files per layout, each by its own process; max = the slowest / largest file):

| Layout | Rows per group | Total size | Row groups (12 files) | Max wall per file | Max writer peak RSS |
| --- | --- | --- | --- | --- | --- |
| duckdb | DuckDB's own | 47.3 MB | 12 | - | - |
| arrow-1d | 3333 | 57.1 MB | 360 | 780 ms | 147 MB |
| arrow-4d | 13333 | 55.2 MB | 96 | 629 ms | 167 MB |
| pqgo-1d | 3333 | 56.0 MB | 360 | 491 ms | 63 MB |
| pqgo-4d | 13333 | 52.7 MB | 96 | 451 ms | 77 MB |

Queries:

| Layout | Source | Query | Range | First wall | Best wall | Best CPU | Peak RSS | Rows |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| duckdb | arrow | countries | 30 d | 52 ms | 45 ms | 57 ms | 56 MB | 20 |
| duckdb | arrow | countries | 365 d | 372 ms | 384 ms | 480 ms | 84 MB | 20 |
| duckdb | arrow | dau | 30 d | 49 ms | 44 ms | 56 ms | 52 MB | 30 |
| duckdb | arrow | dau | 365 d | 399 ms | 367 ms | 441 ms | 85 MB | 365 |
| duckdb | arrow | attrvalues | 30 d | 128 ms | 129 ms | 147 ms | 97 MB | 20367 |
| duckdb | arrow | attrvalues | 365 d | 922 ms | 902 ms | 1062 ms | 137 MB | 20367 |
| duckdb | pqgo | countries | 30 d | 43 ms | 35 ms | 42 ms | 39 MB | 20 |
| duckdb | pqgo | countries | 365 d | 331 ms | 325 ms | 375 ms | 63 MB | 20 |
| duckdb | pqgo | dau | 30 d | 36 ms | 37 ms | 46 ms | 39 MB | 30 |
| duckdb | pqgo | dau | 365 d | 372 ms | 338 ms | 387 ms | 73 MB | 365 |
| duckdb | pqgo | attrvalues | 30 d | 116 ms | 111 ms | 129 ms | 47 MB | 20367 |
| duckdb | pqgo | attrvalues | 365 d | 726 ms | 734 ms | 834 ms | 78 MB | 20367 |
| arrow-1d | arrow | countries | 30 d | 27 ms | 26 ms | 36 ms | 35 MB | 20 |
| arrow-1d | arrow | countries | 365 d | 226 ms | 239 ms | 297 ms | 54 MB | 20 |
| arrow-1d | arrow | dau | 30 d | 35 ms | 33 ms | 47 ms | 38 MB | 30 |
| arrow-1d | arrow | dau | 365 d | 314 ms | 319 ms | 382 ms | 73 MB | 365 |
| arrow-1d | arrow | attrvalues | 30 d | 127 ms | 127 ms | 155 ms | 56 MB | 20367 |
| arrow-1d | arrow | attrvalues | 365 d | 1104 ms | 1055 ms | 1267 ms | 67 MB | 20367 |
| arrow-1d | pqgo | countries | 30 d | 21 ms | 18 ms | 23 ms | 31 MB | 20 |
| arrow-1d | pqgo | countries | 365 d | 159 ms | 154 ms | 176 ms | 50 MB | 20 |
| arrow-1d | pqgo | dau | 30 d | 27 ms | 27 ms | 34 ms | 36 MB | 30 |
| arrow-1d | pqgo | dau | 365 d | 230 ms | 228 ms | 257 ms | 68 MB | 365 |
| arrow-1d | pqgo | attrvalues | 30 d | 111 ms | 107 ms | 124 ms | 48 MB | 20367 |
| arrow-1d | pqgo | attrvalues | 365 d | 809 ms | 823 ms | 889 ms | 58 MB | 20367 |
| arrow-4d | arrow | countries | 30 d | 27 ms | 27 ms | 37 ms | 36 MB | 20 |
| arrow-4d | arrow | countries | 365 d | 235 ms | 236 ms | 294 ms | 59 MB | 20 |
| arrow-4d | arrow | dau | 30 d | 36 ms | 34 ms | 44 ms | 41 MB | 30 |
| arrow-4d | arrow | dau | 365 d | 318 ms | 305 ms | 361 ms | 74 MB | 365 |
| arrow-4d | arrow | attrvalues | 30 d | 120 ms | 114 ms | 134 ms | 60 MB | 20367 |
| arrow-4d | arrow | attrvalues | 365 d | 887 ms | 871 ms | 1035 ms | 75 MB | 20367 |
| arrow-4d | pqgo | countries | 30 d | 23 ms | 23 ms | 30 ms | 33 MB | 20 |
| arrow-4d | pqgo | countries | 365 d | 178 ms | 158 ms | 184 ms | 51 MB | 20 |
| arrow-4d | pqgo | dau | 30 d | 26 ms | 25 ms | 34 ms | 35 MB | 30 |
| arrow-4d | pqgo | dau | 365 d | 230 ms | 232 ms | 263 ms | 66 MB | 365 |
| arrow-4d | pqgo | attrvalues | 30 d | 104 ms | 103 ms | 126 ms | 47 MB | 20367 |
| arrow-4d | pqgo | attrvalues | 365 d | 699 ms | 660 ms | 728 ms | 61 MB | 20367 |
| pqgo-1d | arrow | countries | 30 d | 25 ms | 31 ms | 38 ms | 36 MB | 20 |
| pqgo-1d | arrow | countries | 365 d | 282 ms | 234 ms | 293 ms | 56 MB | 20 |
| pqgo-1d | arrow | dau | 30 d | 35 ms | 33 ms | 45 ms | 39 MB | 30 |
| pqgo-1d | arrow | dau | 365 d | 317 ms | 319 ms | 384 ms | 73 MB | 365 |
| pqgo-1d | arrow | attrvalues | 30 d | 134 ms | 136 ms | 166 ms | 56 MB | 20367 |
| pqgo-1d | arrow | attrvalues | 365 d | 1152 ms | 1058 ms | 1279 ms | 67 MB | 20367 |
| pqgo-1d | pqgo | countries | 30 d | 18 ms | 18 ms | 23 ms | 31 MB | 20 |
| pqgo-1d | pqgo | countries | 365 d | 165 ms | 157 ms | 177 ms | 50 MB | 20 |
| pqgo-1d | pqgo | dau | 30 d | 25 ms | 25 ms | 32 ms | 34 MB | 30 |
| pqgo-1d | pqgo | dau | 365 d | 231 ms | 228 ms | 259 ms | 66 MB | 365 |
| pqgo-1d | pqgo | attrvalues | 30 d | 108 ms | 107 ms | 124 ms | 48 MB | 20367 |
| pqgo-1d | pqgo | attrvalues | 365 d | 815 ms | 811 ms | 879 ms | 60 MB | 20367 |
| pqgo-4d | arrow | countries | 30 d | 27 ms | 27 ms | 35 ms | 36 MB | 20 |
| pqgo-4d | arrow | countries | 365 d | 245 ms | 230 ms | 288 ms | 58 MB | 20 |
| pqgo-4d | arrow | dau | 30 d | 35 ms | 35 ms | 47 ms | 41 MB | 30 |
| pqgo-4d | arrow | dau | 365 d | 319 ms | 302 ms | 363 ms | 73 MB | 365 |
| pqgo-4d | arrow | attrvalues | 30 d | 121 ms | 119 ms | 140 ms | 60 MB | 20367 |
| pqgo-4d | arrow | attrvalues | 365 d | 894 ms | 867 ms | 1036 ms | 75 MB | 20367 |
| pqgo-4d | pqgo | countries | 30 d | 21 ms | 20 ms | 26 ms | 32 MB | 20 |
| pqgo-4d | pqgo | countries | 365 d | 186 ms | 160 ms | 188 ms | 52 MB | 20 |
| pqgo-4d | pqgo | dau | 30 d | 26 ms | 26 ms | 34 ms | 35 MB | 30 |
| pqgo-4d | pqgo | dau | 365 d | 229 ms | 223 ms | 255 ms | 66 MB | 365 |
| pqgo-4d | pqgo | attrvalues | 30 d | 104 ms | 98 ms | 118 ms | 46 MB | 20367 |
| pqgo-4d | pqgo | attrvalues | 365 d | 678 ms | 663 ms | 737 ms | 61 MB | 20367 |

Failed cells: 0 of 60. Load average (1 min) at cell start: 1.32 to 2.32.

The three queries one after another (sum of walls; "Peak RSS" = peak over both ranges, the largest of the three queries at 30 d and 365 d):

| Layout | Source | 30 d first | 30 d best | 365 d first | 365 d best | Peak RSS |
| --- | --- | --- | --- | --- | --- | --- |
| duckdb | arrow | 229 ms | 218 ms | 1693 ms | 1653 ms | 137 MB |
| duckdb | pqgo | 195 ms | 183 ms | 1429 ms | 1398 ms | 78 MB |
| arrow-1d | arrow | 189 ms | 186 ms | 1645 ms | 1613 ms | 73 MB |
| arrow-1d | pqgo | 158 ms | 152 ms | 1198 ms | 1205 ms | 68 MB |
| arrow-4d | arrow | 183 ms | 174 ms | 1440 ms | 1412 ms | 75 MB |
| arrow-4d | pqgo | 154 ms | 152 ms | 1107 ms | 1049 ms | 66 MB |
| pqgo-1d | arrow | 194 ms | 200 ms | 1751 ms | 1611 ms | 73 MB |
| pqgo-1d | pqgo | 152 ms | 150 ms | 1211 ms | 1197 ms | 66 MB |
| pqgo-4d | arrow | 183 ms | 181 ms | 1458 ms | 1398 ms | 75 MB |
| pqgo-4d | pqgo | 152 ms | 143 ms | 1093 ms | 1045 ms | 66 MB |

### Volume 1m (1000000 events a month)

Rewrites (12 month files per layout, each by its own process; max = the slowest / largest file):

| Layout | Rows per group | Total size | Row groups (12 files) | Max wall per file | Max writer peak RSS |
| --- | --- | --- | --- | --- | --- |
| duckdb | DuckDB's own | 455.1 MB | 108 | - | - |
| arrow-1d | 33333 | 533.6 MB | 360 | 5220 ms | 271 MB |
| arrow-4d | 133333 | 515.2 MB | 96 | 5904 ms | 597 MB |
| pqgo-1d | 33333 | 508.7 MB | 360 | 8165 ms | 208 MB |
| pqgo-4d | 133333 | 499.1 MB | 96 | 10747 ms | 322 MB |

Queries:

| Layout | Source | Query | Range | First wall | Best wall | Best CPU | Peak RSS | Rows |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| duckdb | arrow | countries | 30 d | 438 ms | 301 ms | 290 ms | 93 MB | 20 |
| duckdb | arrow | countries | 365 d | 3506 ms | 3346 ms | 2895 ms | 295 MB | 20 |
| duckdb | arrow | dau | 30 d | 951 ms | 550 ms | 455 ms | 100 MB | 30 |
| duckdb | arrow | dau | 365 d | 5496 ms | 4860 ms | 4510 ms | 541 MB | 365 |
| duckdb | arrow | attrvalues | 30 d | 1729 ms | 1270 ms | 1219 ms | 207 MB | 20367 |
| duckdb | arrow | attrvalues | 365 d | 14446 ms | 13593 ms | 12502 ms | 262 MB | 20367 |
| duckdb | pqgo | countries | 30 d | 221 ms | 228 ms | 245 ms | 65 MB | 20 |
| duckdb | pqgo | countries | 365 d | 2405 ms | 2080 ms | 2218 ms | 250 MB | 20 |
| duckdb | pqgo | dau | 30 d | 301 ms | 293 ms | 364 ms | 90 MB | 30 |
| duckdb | pqgo | dau | 365 d | 3350 ms | 3613 ms | 3877 ms | 431 MB | 365 |
| duckdb | pqgo | attrvalues | 30 d | 856 ms | 834 ms | 918 ms | 137 MB | 20367 |
| duckdb | pqgo | attrvalues | 365 d | 8121 ms | 7763 ms | 8202 ms | 194 MB | 20367 |
| arrow-1d | arrow | countries | 30 d | 183 ms | 175 ms | 223 ms | 78 MB | 20 |
| arrow-1d | arrow | countries | 365 d | 1893 ms | 1887 ms | 2164 ms | 280 MB | 20 |
| arrow-1d | arrow | dau | 30 d | 242 ms | 248 ms | 314 ms | 93 MB | 30 |
| arrow-1d | arrow | dau | 365 d | 2765 ms | 2764 ms | 3027 ms | 483 MB | 365 |
| arrow-1d | arrow | attrvalues | 30 d | 876 ms | 876 ms | 995 ms | 176 MB | 20367 |
| arrow-1d | arrow | attrvalues | 365 d | 9449 ms | 9060 ms | 9996 ms | 216 MB | 20367 |
| arrow-1d | pqgo | countries | 30 d | 131 ms | 133 ms | 158 ms | 58 MB | 20 |
| arrow-1d | pqgo | countries | 365 d | 1448 ms | 1479 ms | 1600 ms | 215 MB | 20 |
| arrow-1d | pqgo | dau | 30 d | 199 ms | 194 ms | 237 ms | 75 MB | 30 |
| arrow-1d | pqgo | dau | 365 d | 2341 ms | 2326 ms | 2513 ms | 401 MB | 365 |
| arrow-1d | pqgo | attrvalues | 30 d | 776 ms | 763 ms | 839 ms | 130 MB | 20367 |
| arrow-1d | pqgo | attrvalues | 365 d | 8197 ms | 12730 ms | 10395 ms | 177 MB | 20367 |
| arrow-4d | arrow | countries | 30 d | 326 ms | 356 ms | 285 ms | 108 MB | 20 |
| arrow-4d | arrow | countries | 365 d | 3538 ms | 2999 ms | 3026 ms | 309 MB | 20 |
| arrow-4d | arrow | dau | 30 d | 503 ms | 418 ms | 384 ms | 111 MB | 30 |
| arrow-4d | arrow | dau | 365 d | 4614 ms | 5083 ms | 4244 ms | 521 MB | 365 |
| arrow-4d | arrow | attrvalues | 30 d | 1278 ms | 1156 ms | 1183 ms | 196 MB | 20367 |
| arrow-4d | arrow | attrvalues | 365 d | 13140 ms | 12820 ms | 12186 ms | 308 MB | 20367 |
| arrow-4d | pqgo | countries | 30 d | 259 ms | 268 ms | 241 ms | 74 MB | 20 |
| arrow-4d | pqgo | countries | 365 d | 2279 ms | 2187 ms | 2381 ms | 253 MB | 20 |
| arrow-4d | pqgo | dau | 30 d | 327 ms | 332 ms | 403 ms | 87 MB | 30 |
| arrow-4d | pqgo | dau | 365 d | 3641 ms | 3655 ms | 3876 ms | 413 MB | 365 |
| arrow-4d | pqgo | attrvalues | 30 d | 1045 ms | 1030 ms | 1097 ms | 142 MB | 20367 |
| arrow-4d | pqgo | attrvalues | 365 d | 8414 ms | 7995 ms | 8418 ms | 194 MB | 20367 |
| pqgo-1d | arrow | countries | 30 d | 203 ms | 186 ms | 234 ms | 79 MB | 20 |
| pqgo-1d | arrow | countries | 365 d | 2042 ms | 2048 ms | 2309 ms | 285 MB | 20 |
| pqgo-1d | arrow | dau | 30 d | 294 ms | 292 ms | 369 ms | 95 MB | 30 |
| pqgo-1d | arrow | dau | 365 d | 2977 ms | 2859 ms | 3121 ms | 450 MB | 365 |
| pqgo-1d | arrow | attrvalues | 30 d | 1210 ms | 1412 ms | 1345 ms | 171 MB | 20367 |
| pqgo-1d | arrow | attrvalues | 365 d | 16375 ms | 15820 ms | 13917 ms | 211 MB | 20367 |
| pqgo-1d | pqgo | countries | 30 d | 233 ms | 234 ms | 207 ms | 62 MB | 20 |
| pqgo-1d | pqgo | countries | 365 d | 2435 ms | 2303 ms | 2043 ms | 244 MB | 20 |
| pqgo-1d | pqgo | dau | 30 d | 383 ms | 340 ms | 313 ms | 77 MB | 30 |
| pqgo-1d | pqgo | dau | 365 d | 3820 ms | 3321 ms | 3104 ms | 408 MB | 365 |
| pqgo-1d | pqgo | attrvalues | 30 d | 1649 ms | 1510 ms | 1221 ms | 128 MB | 20367 |
| pqgo-1d | pqgo | attrvalues | 365 d | 18972 ms | 14475 ms | 11924 ms | 179 MB | 20367 |
| pqgo-4d | arrow | countries | 30 d | 331 ms | 375 ms | 315 ms | 97 MB | 20 |
| pqgo-4d | arrow | countries | 365 d | 4042 ms | 3866 ms | 3223 ms | 313 MB | 20 |
| pqgo-4d | arrow | dau | 30 d | 561 ms | 552 ms | 418 ms | 100 MB | 30 |
| pqgo-4d | arrow | dau | 365 d | 5451 ms | 5523 ms | 4439 ms | 492 MB | 365 |
| pqgo-4d | arrow | attrvalues | 30 d | 1712 ms | 1859 ms | 1408 ms | 200 MB | 20367 |
| pqgo-4d | arrow | attrvalues | 365 d | 16832 ms | 16445 ms | 13852 ms | 259 MB | 20367 |
| pqgo-4d | pqgo | countries | 30 d | 339 ms | 298 ms | 240 ms | 71 MB | 20 |
| pqgo-4d | pqgo | countries | 365 d | 3272 ms | 2948 ms | 2418 ms | 257 MB | 20 |
| pqgo-4d | pqgo | dau | 30 d | 407 ms | 366 ms | 335 ms | 83 MB | 30 |
| pqgo-4d | pqgo | dau | 365 d | 4107 ms | 4137 ms | 3448 ms | 421 MB | 365 |
| pqgo-4d | pqgo | attrvalues | 30 d | 1575 ms | 1557 ms | 1236 ms | 131 MB | 20367 |
| pqgo-4d | pqgo | attrvalues | 365 d | 14652 ms | 14109 ms | 11837 ms | 203 MB | 20367 |

Failed cells: 0 of 60. Load average (1 min) at cell start: 2.37 to 8.69.

**Caveat: host noise. Do not compare layouts at 1M.** Another process kept the
host busy during this volume: the 1-minute load average rose to 8.69 on 4 CPUs
(it was still 6.3 after the run). The cells ran with the layout as the outermost
loop (duckdb, arrow-1d, arrow-4d, pqgo-1d, pqgo-4d), so the layout is confounded
with elapsed time and with the load, which did not simply rise (per-cell loads,
`matrix-1m.json`): duckdb layout, run first, 5.8 to 8.7 (the highest); arrow-1d
2.4 to 4.2 (2.4 to 2.6 when read with parquet-go, the quietest cells); arrow-4d
4.6 to 7.2; pqgo-1d 4.0 to 6.1; pqgo-4d 6.2 to 6.6. Load also differs within a
layout, between the two sources. The layouts measured later (pqgo-1d, pqgo-4d,
and partly arrow-4d) look worse partly or wholly because of that. Some cells
show wall above CPU, or a best run slower than the first (arrow-1d / pqgo /
attrvalues / 365 d: first 8197 ms, best 12730 ms). Read the 1M numbers as orders
of magnitude. Within one layout, source against source is a fairer comparison:
those cells ran next to each other. Even so, at pqgo-1d and pqgo-4d the two
sources come out mixed. A rerun on a quiet host (`native-bench matrix -volumes 1m`)
is needed before choosing a layout. The best 1M 30 d trio (1.09 s, arrow-1d /
pqgo) came from the quietest cells, so the miss of the 30-day target is robust.

Other limits of the matrix:

- Ranges are month-aligned, so day pruning inside a file is never exercised.
- No cold-cache numbers: the "first" runs follow earlier cells over the same
  files, so the OS cache was warm.
- The generator has no `group_id`, so attrvalues' group sets were never built
  natively (the DuckDB baseline neither).
- "Write" in the rewrite tables is a Parquet-to-Parquet rewrite with fsync in
  the wall, not the daily pass's NDJSON dump and month merge.

The three queries one after another (sum of walls; "Peak RSS" = peak over both ranges, the largest of the three queries at 30 d and 365 d):

| Layout | Source | 30 d first | 30 d best | 365 d first | 365 d best | Peak RSS |
| --- | --- | --- | --- | --- | --- | --- |
| duckdb | arrow | 3117 ms | 2121 ms | 23449 ms | 21799 ms | 541 MB |
| duckdb | pqgo | 1379 ms | 1355 ms | 13876 ms | 13456 ms | 431 MB |
| arrow-1d | arrow | 1301 ms | 1299 ms | 14106 ms | 13711 ms | 483 MB |
| arrow-1d | pqgo | 1106 ms | 1090 ms | 11986 ms | 16534 ms | 401 MB |
| arrow-4d | arrow | 2107 ms | 1930 ms | 21292 ms | 20902 ms | 521 MB |
| arrow-4d | pqgo | 1631 ms | 1630 ms | 14334 ms | 13837 ms | 413 MB |
| pqgo-1d | arrow | 1707 ms | 1891 ms | 21394 ms | 20727 ms | 450 MB |
| pqgo-1d | pqgo | 2264 ms | 2084 ms | 25226 ms | 20100 ms | 408 MB |
| pqgo-4d | arrow | 2604 ms | 2786 ms | 26324 ms | 25834 ms | 492 MB |
| pqgo-4d | pqgo | 2320 ms | 2221 ms | 22031 ms | 21193 ms | 421 MB |

### Binary size

`go build -trimpath -ldflags="-s -w"` (GOOS/GOARCH of this host), a main that opens a file and prints its row count:

| Main | Bytes | MB | Over the empty main |
| --- | --- | --- | --- |
| empty `func main(){}` | 1200290 | 1.1 MB | 0.0 MB |
| arrow-go reader | 22851849 | 21.8 MB | 20.6 MB |
| parquet-go reader | 7176457 | 6.8 MB | 5.7 MB |
| native-bench (both, plus the queries) | 39534857 | 37.7 MB | 36.6 MB |

Load average (1 min) over every cell: 1.32 to 8.69. Matrix wall: 18m14s.

## Step 1b: optimised scans on a quiet host (matrix1b)

Started 2026-10-22T07:36:31Z. Every run is a fresh `native-bench query` process with GOMAXPROCS=2 and `-workers 2`. Before every run and every rewrite the matrix waits until the 1-minute load average is at most 1.0 (polling every 10s, giving up after 10m0s and flagging the run); the load each started at is recorded. The loop order is range, query, run index, then layout and source, so load drift spreads across layouts. Each cell runs 3 times: "first" is run 1, "best" the minimum of runs 2 to 3 (each metric on its own). Files passed are the months overlapping the range (`30d-mid` spans two). Layouts: `duckdb` = the original files; `pqgo-1d` / `pqgo-4d` / `arrow-1d` = step 1's rewrites (PerMonth/30, PerMonth*4/30 rows per group); `pqgo-8h` = new, PerMonth/90 rows per group. Sources: `pqgo` on every layout, `arrow` on `arrow-1d` and `pqgo-1d`. Ranges: 1d = 2026-09-30; 7d = 2026-09-08..14; 30d-mid = 2026-08-16..09-14; 30d = 2026-09-01..30; 365d = 2025-10-01..2026-09-30. Exact queries must hash-equal across layouts and sources; `attrvalues-approx` is compared value by value with `attrvalues` of the same layout, source and range.

### Step 1b, volume 100k (100000 events a month)

Layouts (12 month files each; rewrites of step 1's layouts were made then and are reused here):

| Layout | Rows per group | Total size | Row groups (12 files) | Max wall per file | Max writer peak RSS | Load at the rewrites |
| --- | --- | --- | --- | --- | --- | --- |
| duckdb | DuckDB's own | 47.3 MB | 12 | - | - | - |
| pqgo-1d | 3333 | 56.0 MB | 360 | 491 ms | 63 MB | step 1 |
| pqgo-4d | 13333 | 52.7 MB | 96 | 451 ms | 77 MB | step 1 |
| pqgo-8h | 1111 | 64.4 MB | 1080 | 616 ms | 63 MB | 0.67 to 0.78 (0 unquiet) |
| arrow-1d | 3333 | 57.1 MB | 360 | 780 ms | 147 MB | step 1 |

Per layout, `pqgo` source: best wall / best CPU / peak RSS per range and query.

`duckdb`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 32 ms / 39 ms / 40 MB | 32 ms / 37 ms / 38 MB | 56 ms / 71 ms / 43 MB | 45 ms / 56 ms / 44 MB |
| 7d | 32 ms / 38 ms / 39 MB | 33 ms / 42 ms / 39 MB | 57 ms / 70 ms / 43 MB | 47 ms / 57 ms / 44 MB |
| 30d-mid | 41 ms / 71 ms / 51 MB | 40 ms / 69 ms / 52 MB | 95 ms / 153 ms / 71 MB | 75 ms / 124 ms / 65 MB |
| 30d | 36 ms / 45 ms / 40 MB | 35 ms / 44 ms / 40 MB | 90 ms / 109 ms / 56 MB | 64 ms / 74 ms / 45 MB |
| 365d | 195 ms / 362 ms / 77 MB | 189 ms / 347 ms / 78 MB | 599 ms / 1073 ms / 149 MB | 327 ms / 590 ms / 105 MB |

`pqgo-1d`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 8.4 ms / 13 ms / 28 MB | 10 ms / 16 ms / 30 MB | 15 ms / 23 ms / 32 MB | 13 ms / 21 ms / 32 MB |
| 7d | 11 ms / 18 ms / 30 MB | 14 ms / 23 ms / 33 MB | 33 ms / 52 ms / 39 MB | 29 ms / 44 ms / 41 MB |
| 30d-mid | 21 ms / 35 ms / 35 MB | 28 ms / 46 ms / 37 MB | 79 ms / 126 ms / 55 MB | 63 ms / 98 ms / 49 MB |
| 30d | 16 ms / 27 ms / 34 MB | 21 ms / 34 ms / 37 MB | 70 ms / 110 ms / 53 MB | 57 ms / 88 ms / 50 MB |
| 365d | 122 ms / 207 ms / 48 MB | 145 ms / 242 ms / 56 MB | 523 ms / 954 ms / 103 MB | 303 ms / 516 ms / 65 MB |

`pqgo-4d`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 8.0 ms / 12 ms / 30 MB | 11 ms / 17 ms / 32 MB | 17 ms / 28 ms / 37 MB | 15 ms / 24 ms / 35 MB |
| 7d | 11 ms / 19 ms / 32 MB | 18 ms / 29 ms / 36 MB | 38 ms / 63 ms / 44 MB | 33 ms / 54 ms / 44 MB |
| 30d-mid | 19 ms / 33 ms / 35 MB | 25 ms / 42 ms / 40 MB | 76 ms / 121 ms / 53 MB | 65 ms / 101 ms / 50 MB |
| 30d | 15 ms / 25 ms / 34 MB | 18 ms / 31 ms / 36 MB | 67 ms / 101 ms / 52 MB | 48 ms / 71 ms / 46 MB |
| 365d | 95 ms / 169 ms / 48 MB | 120 ms / 207 ms / 55 MB | 479 ms / 890 ms / 105 MB | 244 ms / 414 ms / 66 MB |

`pqgo-8h`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 18 ms / 29 ms / 33 MB | 19 ms / 31 ms / 33 MB | 23 ms / 36 ms / 36 MB | 26 ms / 37 ms / 35 MB |
| 7d | 20 ms / 32 ms / 34 MB | 23 ms / 36 ms / 35 MB | 36 ms / 57 ms / 43 MB | 38 ms / 59 ms / 44 MB |
| 30d-mid | 41 ms / 66 ms / 42 MB | 44 ms / 72 ms / 44 MB | 99 ms / 153 ms / 57 MB | 81 ms / 128 ms / 58 MB |
| 30d | 27 ms / 43 ms / 39 MB | 34 ms / 55 ms / 41 MB | 85 ms / 136 ms / 60 MB | 70 ms / 113 ms / 54 MB |
| 365d | 246 ms / 407 ms / 53 MB | 261 ms / 424 ms / 65 MB | 646 ms / 1153 ms / 107 MB | 437 ms / 732 ms / 71 MB |

`arrow-1d`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 7.3 ms / 11 ms / 28 MB | 9.9 ms / 16 ms / 30 MB | 14 ms / 22 ms / 32 MB | 15 ms / 23 ms / 33 MB |
| 7d | 9.0 ms / 14 ms / 29 MB | 12 ms / 20 ms / 32 MB | 29 ms / 45 ms / 39 MB | 30 ms / 47 ms / 41 MB |
| 30d-mid | 21 ms / 35 ms / 35 MB | 24 ms / 40 ms / 37 MB | 77 ms / 120 ms / 52 MB | 58 ms / 91 ms / 47 MB |
| 30d | 16 ms / 26 ms / 33 MB | 21 ms / 35 ms / 38 MB | 68 ms / 105 ms / 52 MB | 55 ms / 84 ms / 50 MB |
| 365d | 114 ms / 195 ms / 46 MB | 145 ms / 246 ms / 58 MB | 507 ms / 933 ms / 98 MB | 291 ms / 500 ms / 64 MB |

Best wall side by side (`pqgo` source; first run in brackets):

| Range | Query | duckdb | pqgo-1d | pqgo-4d | pqgo-8h | arrow-1d |
| --- | --- | --- | --- | --- | --- | --- |
| 1d | countries | 32 ms (34 ms) | 8.4 ms (8.2 ms) | 8.0 ms (14 ms) | 18 ms (20 ms) | 7.3 ms (7.6 ms) |
| 1d | dau | 32 ms (33 ms) | 10 ms (11 ms) | 11 ms (11 ms) | 19 ms (19 ms) | 9.9 ms (11 ms) |
| 1d | attrvalues | 56 ms (47 ms) | 15 ms (14 ms) | 17 ms (16 ms) | 23 ms (25 ms) | 14 ms (28 ms) |
| 1d | attrvalues-approx | 45 ms (40 ms) | 13 ms (13 ms) | 15 ms (15 ms) | 26 ms (22 ms) | 15 ms (14 ms) |
| 7d | countries | 32 ms (33 ms) | 11 ms (11 ms) | 11 ms (12 ms) | 20 ms (22 ms) | 9.0 ms (14 ms) |
| 7d | dau | 33 ms (31 ms) | 14 ms (14 ms) | 18 ms (17 ms) | 23 ms (23 ms) | 12 ms (12 ms) |
| 7d | attrvalues | 57 ms (62 ms) | 33 ms (30 ms) | 38 ms (36 ms) | 36 ms (38 ms) | 29 ms (29 ms) |
| 7d | attrvalues-approx | 47 ms (53 ms) | 29 ms (30 ms) | 33 ms (33 ms) | 38 ms (35 ms) | 30 ms (30 ms) |
| 30d-mid | countries | 41 ms (42 ms) | 21 ms (23 ms) | 19 ms (21 ms) | 41 ms (44 ms) | 21 ms (20 ms) |
| 30d-mid | dau | 40 ms (51 ms) | 28 ms (31 ms) | 25 ms (25 ms) | 44 ms (46 ms) | 24 ms (25 ms) |
| 30d-mid | attrvalues | 95 ms (103 ms) | 79 ms (81 ms) | 76 ms (79 ms) | 99 ms (95 ms) | 77 ms (78 ms) |
| 30d-mid | attrvalues-approx | 75 ms (72 ms) | 63 ms (64 ms) | 65 ms (56 ms) | 81 ms (86 ms) | 58 ms (61 ms) |
| 30d | countries | 36 ms (36 ms) | 16 ms (16 ms) | 15 ms (15 ms) | 27 ms (26 ms) | 16 ms (16 ms) |
| 30d | dau | 35 ms (36 ms) | 21 ms (23 ms) | 18 ms (19 ms) | 34 ms (32 ms) | 21 ms (23 ms) |
| 30d | attrvalues | 90 ms (88 ms) | 70 ms (73 ms) | 67 ms (74 ms) | 85 ms (86 ms) | 68 ms (73 ms) |
| 30d | attrvalues-approx | 64 ms (65 ms) | 57 ms (59 ms) | 48 ms (50 ms) | 70 ms (72 ms) | 55 ms (82 ms) |
| 365d | countries | 195 ms (192 ms) | 122 ms (123 ms) | 95 ms (96 ms) | 246 ms (251 ms) | 114 ms (118 ms) |
| 365d | dau | 189 ms (194 ms) | 145 ms (153 ms) | 120 ms (116 ms) | 261 ms (261 ms) | 145 ms (138 ms) |
| 365d | attrvalues | 599 ms (598 ms) | 523 ms (525 ms) | 479 ms (492 ms) | 646 ms (641 ms) | 507 ms (531 ms) |
| 365d | attrvalues-approx | 327 ms (330 ms) | 303 ms (300 ms) | 244 ms (256 ms) | 437 ms (457 ms) | 291 ms (286 ms) |

Dashboard trio per range (countries + dau + attrvalues-approx, sum of best walls; first runs in brackets; peak = the largest of the three), `pqgo` source:

| Range | duckdb | pqgo-1d | pqgo-4d | pqgo-8h | arrow-1d |
| --- | --- | --- | --- | --- | --- |
| 1d | 109 ms (108 ms), 44 MB | 32 ms (33 ms), 32 MB | 34 ms (40 ms), 35 MB | 63 ms (61 ms), 35 MB | 32 ms (32 ms), 33 MB |
| 7d | 112 ms (117 ms), 44 MB | 54 ms (55 ms), 41 MB | 62 ms (62 ms), 44 MB | 81 ms (79 ms), 44 MB | 51 ms (56 ms), 41 MB |
| 30d-mid | 156 ms (165 ms), 65 MB | 112 ms (118 ms), 49 MB | 109 ms (102 ms), 50 MB | 166 ms (176 ms), 58 MB | 104 ms (107 ms), 47 MB |
| 30d | 135 ms (137 ms), 45 MB | 94 ms (98 ms), 50 MB | 81 ms (84 ms), 46 MB | 131 ms (130 ms), 54 MB | 92 ms (121 ms), 50 MB |
| 365d | 710 ms (716 ms), 105 MB | 570 ms (576 ms), 65 MB | 459 ms (468 ms), 66 MB | 944 ms (969 ms), 71 MB | 551 ms (542 ms), 64 MB |

Memory: peak RSS per range, `pqgo` source with 2 workers, the largest over the four queries (and over the trio):

| Range | duckdb | pqgo-1d | pqgo-4d | pqgo-8h | arrow-1d |
| --- | --- | --- | --- | --- | --- |
| 1d | 44 MB (44 MB) | 32 MB (32 MB) | 37 MB (35 MB) | 36 MB (35 MB) | 33 MB (33 MB) |
| 7d | 44 MB (44 MB) | 41 MB (41 MB) | 44 MB (44 MB) | 44 MB (44 MB) | 41 MB (41 MB) |
| 30d-mid | 71 MB (65 MB) | 55 MB (49 MB) | 53 MB (50 MB) | 58 MB (58 MB) | 52 MB (47 MB) |
| 30d | 56 MB (45 MB) | 53 MB (50 MB) | 52 MB (46 MB) | 60 MB (54 MB) | 52 MB (50 MB) |
| 365d | 149 MB (105 MB) | 103 MB (65 MB) | 105 MB (66 MB) | 107 MB (71 MB) | 98 MB (64 MB) |

Library re-check: best wall / best CPU / peak RSS, the two sources on the two 1-day layouts:

| Range | Query | pqgo-1d, pqgo | pqgo-1d, arrow | arrow-1d, pqgo | arrow-1d, arrow |
| --- | --- | --- | --- | --- | --- |
| 1d | countries | 8.4 ms / 13 ms / 28 MB | 10.0 ms / 15 ms / 31 MB | 7.3 ms / 11 ms / 28 MB | 8.7 ms / 14 ms / 30 MB |
| 1d | dau | 10 ms / 16 ms / 30 MB | 12 ms / 19 ms / 33 MB | 9.9 ms / 16 ms / 30 MB | 11 ms / 18 ms / 33 MB |
| 1d | attrvalues | 15 ms / 23 ms / 32 MB | 21 ms / 35 ms / 40 MB | 14 ms / 22 ms / 32 MB | 17 ms / 28 ms / 40 MB |
| 1d | attrvalues-approx | 13 ms / 21 ms / 32 MB | 20 ms / 35 ms / 40 MB | 15 ms / 23 ms / 33 MB | 19 ms / 32 ms / 39 MB |
| 7d | countries | 11 ms / 18 ms / 30 MB | 13 ms / 23 ms / 34 MB | 9.0 ms / 14 ms / 29 MB | 13 ms / 20 ms / 34 MB |
| 7d | dau | 14 ms / 23 ms / 33 MB | 18 ms / 31 ms / 36 MB | 12 ms / 20 ms / 32 MB | 17 ms / 29 ms / 35 MB |
| 7d | attrvalues | 33 ms / 52 ms / 39 MB | 37 ms / 62 ms / 45 MB | 29 ms / 45 ms / 39 MB | 37 ms / 59 ms / 46 MB |
| 7d | attrvalues-approx | 29 ms / 44 ms / 41 MB | 39 ms / 62 ms / 46 MB | 30 ms / 47 ms / 41 MB | 37 ms / 60 ms / 47 MB |
| 30d-mid | countries | 21 ms / 35 ms / 35 MB | 29 ms / 51 ms / 39 MB | 21 ms / 35 ms / 35 MB | 28 ms / 50 ms / 38 MB |
| 30d-mid | dau | 28 ms / 46 ms / 37 MB | 35 ms / 62 ms / 41 MB | 24 ms / 40 ms / 37 MB | 34 ms / 60 ms / 42 MB |
| 30d-mid | attrvalues | 79 ms / 126 ms / 55 MB | 99 ms / 162 ms / 62 MB | 77 ms / 120 ms / 52 MB | 95 ms / 154 ms / 56 MB |
| 30d-mid | attrvalues-approx | 63 ms / 98 ms / 49 MB | 82 ms / 137 ms / 60 MB | 58 ms / 91 ms / 47 MB | 80 ms / 133 ms / 55 MB |
| 30d | countries | 16 ms / 27 ms / 34 MB | 23 ms / 41 ms / 39 MB | 16 ms / 26 ms / 33 MB | 23 ms / 39 ms / 38 MB |
| 30d | dau | 21 ms / 34 ms / 37 MB | 29 ms / 51 ms / 41 MB | 21 ms / 35 ms / 38 MB | 29 ms / 50 ms / 40 MB |
| 30d | attrvalues | 70 ms / 110 ms / 53 MB | 89 ms / 146 ms / 61 MB | 68 ms / 105 ms / 52 MB | 88 ms / 138 ms / 59 MB |
| 30d | attrvalues-approx | 57 ms / 88 ms / 50 MB | 75 ms / 125 ms / 60 MB | 55 ms / 84 ms / 50 MB | 72 ms / 116 ms / 57 MB |
| 365d | countries | 122 ms / 207 ms / 48 MB | 185 ms / 332 ms / 48 MB | 114 ms / 195 ms / 46 MB | 170 ms / 304 ms / 49 MB |
| 365d | dau | 145 ms / 242 ms / 56 MB | 218 ms / 386 ms / 62 MB | 145 ms / 246 ms / 58 MB | 199 ms / 351 ms / 57 MB |
| 365d | attrvalues | 523 ms / 954 ms / 103 MB | 684 ms / 1260 ms / 114 MB | 507 ms / 933 ms / 98 MB | 676 ms / 1246 ms / 110 MB |
| 365d | attrvalues-approx | 303 ms / 516 ms / 65 MB | 467 ms / 841 ms / 82 MB | 291 ms / 500 ms / 64 MB | 450 ms / 805 ms / 76 MB |

Against step 1 (serial scans, maps; `pqgo-1d`, `pqgo` source, best wall; step 1's approx column is its exact attrvalues):

| Range | Query | Step 1 best | Step 1b best | Speedup | Step 1 peak RSS | Step 1b peak RSS |
| --- | --- | --- | --- | --- | --- | --- |
| 30d | countries | 18 ms | 16 ms | 1.1x | 31 MB | 34 MB |
| 30d | dau | 25 ms | 21 ms | 1.2x | 34 MB | 37 MB |
| 30d | attrvalues | 107 ms | 70 ms | 1.5x | 48 MB | 53 MB |
| 30d | attrvalues-approx | 107 ms | 57 ms | 1.9x | 48 MB | 50 MB |
| 365d | countries | 157 ms | 122 ms | 1.3x | 50 MB | 48 MB |
| 365d | dau | 228 ms | 145 ms | 1.6x | 66 MB | 56 MB |
| 365d | attrvalues | 811 ms | 523 ms | 1.6x | 60 MB | 103 MB |
| 365d | attrvalues-approx | 811 ms | 303 ms | 2.7x | 60 MB | 65 MB |

`attrvalues-approx` against `attrvalues` (users and groups values; tolerance 2%, or 1 under 50; the bound is statistical, about 2.5 sigma, so rare misses are expected; a cell fails only when more than 1% of the values above 1,024 miss):

| Range | Cells | Values per cell | Max rel. error (exact >= 50) | Values outside (max per cell) | Users outside (max per cell) | Values > 1,024 | Of those outside (max) | Failed |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1d | 7 | 3352 | 0.00% | 0 | 0 | 0 | 0 | 0 |
| 7d | 7 | 17248 | 0.83% | 0 | 0 | 14 | 0 | 0 |
| 30d-mid | 7 | 38118 | 1.13% | 0 | 0 | 27 | 0 | 0 |
| 30d | 7 | 40734 | 1.31% | 0 | 0 | 27 | 0 | 0 |
| 365d | 7 | 40734 | 1.31% | 0 | 0 | 27 | 0 | 0 |

Failed cells: 0 of 140 (420 runs). Load average (1 min) at run start: 0.81 to 0.98. Runs that started above 1.0 (the gate gave up): 0 cells.

### Step 1b, volume 1m (1000000 events a month)

Layouts (12 month files each; rewrites of step 1's layouts were made then and are reused here):

| Layout | Rows per group | Total size | Row groups (12 files) | Max wall per file | Max writer peak RSS | Load at the rewrites |
| --- | --- | --- | --- | --- | --- | --- |
| duckdb | DuckDB's own | 455.1 MB | 108 | - | - | - |
| pqgo-1d | 33333 | 508.7 MB | 360 | 8165 ms | 208 MB | step 1 |
| pqgo-4d | 133333 | 499.1 MB | 96 | 10747 ms | 322 MB | step 1 |
| pqgo-8h | 11111 | 521.6 MB | 1080 | 4117 ms | 182 MB | 0.89 to 0.98 (0 unquiet) |
| arrow-1d | 33333 | 533.6 MB | 360 | 5220 ms | 271 MB | step 1 |

Per layout, `pqgo` source: best wall / best CPU / peak RSS per range and query.

`duckdb`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 53 ms / 93 ms / 54 MB | 101 ms / 175 ms / 71 MB | 122 ms / 216 ms / 84 MB | 95 ms / 164 ms / 86 MB |
| 7d | 57 ms / 95 ms / 59 MB | 136 ms / 248 ms / 80 MB | 236 ms / 407 ms / 121 MB | 138 ms / 245 ms / 93 MB |
| 30d-mid | 145 ms / 258 ms / 80 MB | 248 ms / 457 ms / 100 MB | 645 ms / 1125 ms / 173 MB | 273 ms / 487 ms / 107 MB |
| 30d | 116 ms / 209 ms / 73 MB | 190 ms / 336 ms / 93 MB | 598 ms / 994 ms / 173 MB | 225 ms / 383 ms / 100 MB |
| 365d | 1035 ms / 1974 ms / 147 MB | 1741 ms / 3285 ms / 224 MB | 5622 ms / 10530 ms / 382 MB | 1815 ms / 3439 ms / 179 MB |

`pqgo-1d`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 16 ms / 26 ms / 35 MB | 30 ms / 50 ms / 44 MB | 59 ms / 91 ms / 61 MB | 42 ms / 66 ms / 52 MB |
| 7d | 37 ms / 62 ms / 45 MB | 65 ms / 104 ms / 60 MB | 166 ms / 258 ms / 85 MB | 90 ms / 138 ms / 65 MB |
| 30d-mid | 100 ms / 172 ms / 64 MB | 137 ms / 239 ms / 83 MB | 544 ms / 897 ms / 140 MB | 194 ms / 315 ms / 83 MB |
| 30d | 93 ms / 161 ms / 61 MB | 122 ms / 207 ms / 75 MB | 539 ms / 876 ms / 149 MB | 187 ms / 300 ms / 76 MB |
| 365d | 776 ms / 1414 ms / 129 MB | 1112 ms / 1977 ms / 186 MB | 5243 ms / 9851 ms / 395 MB | 1385 ms / 2484 ms / 139 MB |

`pqgo-4d`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 44 ms / 68 ms / 52 MB | 62 ms / 99 ms / 58 MB | 90 ms / 139 ms / 71 MB | 59 ms / 96 ms / 71 MB |
| 7d | 71 ms / 121 ms / 62 MB | 114 ms / 180 ms / 83 MB | 217 ms / 376 ms / 112 MB | 109 ms / 184 ms / 84 MB |
| 30d-mid | 141 ms / 238 ms / 84 MB | 184 ms / 309 ms / 93 MB | 590 ms / 988 ms / 164 MB | 213 ms / 357 ms / 99 MB |
| 30d | 102 ms / 183 ms / 75 MB | 135 ms / 215 ms / 85 MB | 559 ms / 905 ms / 163 MB | 178 ms / 278 ms / 102 MB |
| 365d | 892 ms / 1598 ms / 158 MB | 1108 ms / 1972 ms / 206 MB | 5017 ms / 9404 ms / 377 MB | 1258 ms / 2282 ms / 178 MB |

`pqgo-8h`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 23 ms / 36 ms / 37 MB | 28 ms / 46 ms / 42 MB | 53 ms / 78 ms / 52 MB | 46 ms / 68 ms / 50 MB |
| 7d | 38 ms / 63 ms / 45 MB | 53 ms / 87 ms / 55 MB | 146 ms / 230 ms / 84 MB | 91 ms / 149 ms / 67 MB |
| 30d-mid | 117 ms / 203 ms / 65 MB | 155 ms / 264 ms / 81 MB | 565 ms / 924 ms / 162 MB | 220 ms / 366 ms / 77 MB |
| 30d | 104 ms / 181 ms / 64 MB | 142 ms / 245 ms / 82 MB | 539 ms / 884 ms / 140 MB | 224 ms / 362 ms / 77 MB |
| 365d | 897 ms / 1595 ms / 136 MB | 1250 ms / 2169 ms / 196 MB | 5456 ms / 10234 ms / 361 MB | 1759 ms / 3120 ms / 134 MB |

`arrow-1d`:

| Range | countries | dau | attrvalues | attrvalues-approx |
| --- | --- | --- | --- | --- |
| 1d | 15 ms / 25 ms / 36 MB | 31 ms / 46 ms / 45 MB | 56 ms / 89 ms / 57 MB | 46 ms / 72 ms / 51 MB |
| 7d | 37 ms / 61 ms / 44 MB | 60 ms / 96 ms / 57 MB | 159 ms / 252 ms / 83 MB | 87 ms / 134 ms / 65 MB |
| 30d-mid | 96 ms / 168 ms / 62 MB | 137 ms / 235 ms / 78 MB | 533 ms / 897 ms / 157 MB | 191 ms / 322 ms / 81 MB |
| 30d | 95 ms / 163 ms / 64 MB | 130 ms / 221 ms / 73 MB | 539 ms / 883 ms / 153 MB | 189 ms / 306 ms / 81 MB |
| 365d | 772 ms / 1407 ms / 117 MB | 1091 ms / 1923 ms / 181 MB | 5155 ms / 9806 ms / 396 MB | 1368 ms / 2479 ms / 146 MB |

Best wall side by side (`pqgo` source; first run in brackets):

| Range | Query | duckdb | pqgo-1d | pqgo-4d | pqgo-8h | arrow-1d |
| --- | --- | --- | --- | --- | --- | --- |
| 1d | countries | 53 ms (55 ms) | 16 ms (20 ms) | 44 ms (44 ms) | 23 ms (23 ms) | 15 ms (20 ms) |
| 1d | dau | 101 ms (98 ms) | 30 ms (33 ms) | 62 ms (68 ms) | 28 ms (34 ms) | 31 ms (34 ms) |
| 1d | attrvalues | 122 ms (121 ms) | 59 ms (60 ms) | 90 ms (103 ms) | 53 ms (54 ms) | 56 ms (58 ms) |
| 1d | attrvalues-approx | 95 ms (99 ms) | 42 ms (41 ms) | 59 ms (54 ms) | 46 ms (48 ms) | 46 ms (45 ms) |
| 7d | countries | 57 ms (59 ms) | 37 ms (40 ms) | 71 ms (75 ms) | 38 ms (39 ms) | 37 ms (37 ms) |
| 7d | dau | 136 ms (129 ms) | 65 ms (65 ms) | 114 ms (111 ms) | 53 ms (53 ms) | 60 ms (62 ms) |
| 7d | attrvalues | 236 ms (232 ms) | 166 ms (185 ms) | 217 ms (249 ms) | 146 ms (145 ms) | 159 ms (166 ms) |
| 7d | attrvalues-approx | 138 ms (142 ms) | 90 ms (102 ms) | 109 ms (113 ms) | 91 ms (102 ms) | 87 ms (89 ms) |
| 30d-mid | countries | 145 ms (138 ms) | 100 ms (103 ms) | 141 ms (134 ms) | 117 ms (119 ms) | 96 ms (104 ms) |
| 30d-mid | dau | 248 ms (258 ms) | 137 ms (138 ms) | 184 ms (188 ms) | 155 ms (149 ms) | 137 ms (142 ms) |
| 30d-mid | attrvalues | 645 ms (652 ms) | 544 ms (540 ms) | 590 ms (631 ms) | 565 ms (558 ms) | 533 ms (537 ms) |
| 30d-mid | attrvalues-approx | 273 ms (275 ms) | 194 ms (211 ms) | 213 ms (213 ms) | 220 ms (222 ms) | 191 ms (192 ms) |
| 30d | countries | 116 ms (113 ms) | 93 ms (106 ms) | 102 ms (110 ms) | 104 ms (107 ms) | 95 ms (93 ms) |
| 30d | dau | 190 ms (192 ms) | 122 ms (126 ms) | 135 ms (130 ms) | 142 ms (137 ms) | 130 ms (134 ms) |
| 30d | attrvalues | 598 ms (616 ms) | 539 ms (529 ms) | 559 ms (606 ms) | 539 ms (567 ms) | 539 ms (547 ms) |
| 30d | attrvalues-approx | 225 ms (225 ms) | 187 ms (182 ms) | 178 ms (191 ms) | 224 ms (240 ms) | 189 ms (185 ms) |
| 365d | countries | 1035 ms (1071 ms) | 776 ms (775 ms) | 892 ms (868 ms) | 897 ms (927 ms) | 772 ms (765 ms) |
| 365d | dau | 1741 ms (1763 ms) | 1112 ms (1122 ms) | 1108 ms (1184 ms) | 1250 ms (1264 ms) | 1091 ms (1141 ms) |
| 365d | attrvalues | 5622 ms (5610 ms) | 5243 ms (5128 ms) | 5017 ms (4991 ms) | 5456 ms (5446 ms) | 5155 ms (5153 ms) |
| 365d | attrvalues-approx | 1815 ms (1841 ms) | 1385 ms (1383 ms) | 1258 ms (1257 ms) | 1759 ms (1764 ms) | 1368 ms (1373 ms) |

Dashboard trio per range (countries + dau + attrvalues-approx, sum of best walls; first runs in brackets; peak = the largest of the three), `pqgo` source:

| Range | duckdb | pqgo-1d | pqgo-4d | pqgo-8h | arrow-1d |
| --- | --- | --- | --- | --- | --- |
| 1d | 248 ms (252 ms), 86 MB | 88 ms (94 ms), 52 MB | 164 ms (165 ms), 71 MB | 97 ms (105 ms), 50 MB | 92 ms (99 ms), 51 MB |
| 7d | 332 ms (331 ms), 93 MB | 191 ms (207 ms), 65 MB | 294 ms (299 ms), 84 MB | 182 ms (194 ms), 67 MB | 184 ms (188 ms), 65 MB |
| 30d-mid | 666 ms (672 ms), 107 MB | 431 ms (451 ms), 83 MB | 539 ms (536 ms), 99 MB | 492 ms (490 ms), 81 MB | 424 ms (438 ms), 81 MB |
| 30d | 531 ms (530 ms), 100 MB | 402 ms (415 ms), 76 MB | 416 ms (430 ms), 102 MB | 470 ms (484 ms), 82 MB | 415 ms (412 ms), 81 MB |
| 365d | 4591 ms (4675 ms), 224 MB | 3274 ms (3280 ms), 186 MB | 3258 ms (3310 ms), 206 MB | 3906 ms (3956 ms), 196 MB | 3231 ms (3278 ms), 181 MB |

Memory: peak RSS per range, `pqgo` source with 2 workers, the largest over the four queries (and over the trio):

| Range | duckdb | pqgo-1d | pqgo-4d | pqgo-8h | arrow-1d |
| --- | --- | --- | --- | --- | --- |
| 1d | 86 MB (86 MB) | 61 MB (52 MB) | 71 MB (71 MB) | 52 MB (50 MB) | 57 MB (51 MB) |
| 7d | 121 MB (93 MB) | 85 MB (65 MB) | 112 MB (84 MB) | 84 MB (67 MB) | 83 MB (65 MB) |
| 30d-mid | 173 MB (107 MB) | 140 MB (83 MB) | 164 MB (99 MB) | 162 MB (81 MB) | 157 MB (81 MB) |
| 30d | 173 MB (100 MB) | 149 MB (76 MB) | 163 MB (102 MB) | 140 MB (82 MB) | 153 MB (81 MB) |
| 365d | 382 MB (224 MB) | 395 MB (186 MB) | 377 MB (206 MB) | 361 MB (196 MB) | 396 MB (181 MB) |

Library re-check: best wall / best CPU / peak RSS, the two sources on the two 1-day layouts:

| Range | Query | pqgo-1d, pqgo | pqgo-1d, arrow | arrow-1d, pqgo | arrow-1d, arrow |
| --- | --- | --- | --- | --- | --- |
| 1d | countries | 16 ms / 26 ms / 35 MB | 24 ms / 38 ms / 46 MB | 15 ms / 25 ms / 36 MB | 21 ms / 36 ms / 45 MB |
| 1d | dau | 30 ms / 50 ms / 44 MB | 42 ms / 74 ms / 54 MB | 31 ms / 46 ms / 45 MB | 40 ms / 67 ms / 61 MB |
| 1d | attrvalues | 59 ms / 91 ms / 61 MB | 78 ms / 137 ms / 94 MB | 56 ms / 89 ms / 57 MB | 77 ms / 128 ms / 96 MB |
| 1d | attrvalues-approx | 42 ms / 66 ms / 52 MB | 65 ms / 107 ms / 85 MB | 46 ms / 72 ms / 51 MB | 69 ms / 112 ms / 90 MB |
| 7d | countries | 37 ms / 62 ms / 45 MB | 52 ms / 91 ms / 53 MB | 37 ms / 61 ms / 44 MB | 54 ms / 88 ms / 59 MB |
| 7d | dau | 65 ms / 104 ms / 60 MB | 76 ms / 131 ms / 65 MB | 60 ms / 96 ms / 57 MB | 73 ms / 129 ms / 62 MB |
| 7d | attrvalues | 166 ms / 258 ms / 85 MB | 205 ms / 331 ms / 120 MB | 159 ms / 252 ms / 83 MB | 199 ms / 322 ms / 126 MB |
| 7d | attrvalues-approx | 90 ms / 138 ms / 65 MB | 135 ms / 224 ms / 130 MB | 87 ms / 134 ms / 65 MB | 130 ms / 221 ms / 129 MB |
| 30d-mid | countries | 100 ms / 172 ms / 64 MB | 140 ms / 248 ms / 75 MB | 96 ms / 168 ms / 62 MB | 135 ms / 243 ms / 77 MB |
| 30d-mid | dau | 137 ms / 239 ms / 83 MB | 180 ms / 322 ms / 90 MB | 137 ms / 235 ms / 78 MB | 174 ms / 316 ms / 87 MB |
| 30d-mid | attrvalues | 544 ms / 897 ms / 140 MB | 641 ms / 1077 ms / 164 MB | 533 ms / 897 ms / 157 MB | 638 ms / 1074 ms / 193 MB |
| 30d-mid | attrvalues-approx | 194 ms / 315 ms / 83 MB | 289 ms / 512 ms / 128 MB | 191 ms / 322 ms / 81 MB | 287 ms / 509 ms / 132 MB |
| 30d | countries | 93 ms / 161 ms / 61 MB | 124 ms / 221 ms / 74 MB | 95 ms / 163 ms / 64 MB | 125 ms / 225 ms / 78 MB |
| 30d | dau | 122 ms / 207 ms / 75 MB | 159 ms / 289 ms / 86 MB | 130 ms / 221 ms / 73 MB | 161 ms / 287 ms / 87 MB |
| 30d | attrvalues | 539 ms / 876 ms / 149 MB | 595 ms / 990 ms / 183 MB | 539 ms / 883 ms / 153 MB | 603 ms / 991 ms / 180 MB |
| 30d | attrvalues-approx | 187 ms / 300 ms / 76 MB | 262 ms / 443 ms / 121 MB | 189 ms / 306 ms / 81 MB | 277 ms / 475 ms / 138 MB |
| 365d | countries | 776 ms / 1414 ms / 129 MB | 1131 ms / 2112 ms / 159 MB | 772 ms / 1407 ms / 117 MB | 1112 ms / 2083 ms / 146 MB |
| 365d | dau | 1112 ms / 1977 ms / 186 MB | 1429 ms / 2582 ms / 199 MB | 1091 ms / 1923 ms / 181 MB | 1401 ms / 2551 ms / 200 MB |
| 365d | attrvalues | 5243 ms / 9851 ms / 395 MB | 5966 ms / 11272 ms / 381 MB | 5155 ms / 9806 ms / 396 MB | 6036 ms / 11450 ms / 404 MB |
| 365d | attrvalues-approx | 1385 ms / 2484 ms / 139 MB | 2212 ms / 4168 ms / 201 MB | 1368 ms / 2479 ms / 146 MB | 2267 ms / 4279 ms / 217 MB |

Writes re-timed behind the gate (12 month files each, a fresh directory; step 1's unguarded numbers beside them):

| Layout | Total wall | Max wall per file | Max writer peak RSS | Size | Load at the rewrites | Step 1 total wall | Step 1 max per file |
| --- | --- | --- | --- | --- | --- | --- | --- |
| pqgo-1d | 48099 ms | 4058 ms | 203 MB | 508.7 MB | 0.97 to 0.99 (0 unquiet) | 64555 ms | 8165 ms |
| arrow-1d | 60295 ms | 5074 ms | 268 MB | 533.6 MB | 0.89 to 1.00 (0 unquiet) | 60788 ms | 5220 ms |

Against step 1 (serial scans, maps; `pqgo-1d`, `pqgo` source, best wall; step 1's approx column is its exact attrvalues):

| Range | Query | Step 1 best | Step 1b best | Speedup | Step 1 peak RSS | Step 1b peak RSS |
| --- | --- | --- | --- | --- | --- | --- |
| 30d | countries | 234 ms | 93 ms | 2.5x | 62 MB | 61 MB |
| 30d | dau | 340 ms | 122 ms | 2.8x | 77 MB | 75 MB |
| 30d | attrvalues | 1510 ms | 539 ms | 2.8x | 128 MB | 149 MB |
| 30d | attrvalues-approx | 1510 ms | 187 ms | 8.1x | 128 MB | 76 MB |
| 365d | countries | 2303 ms | 776 ms | 3.0x | 244 MB | 129 MB |
| 365d | dau | 3321 ms | 1112 ms | 3.0x | 408 MB | 186 MB |
| 365d | attrvalues | 14475 ms | 5243 ms | 2.8x | 179 MB | 395 MB |
| 365d | attrvalues-approx | 14475 ms | 1385 ms | 10.5x | 179 MB | 139 MB |

`attrvalues-approx` against `attrvalues` (users and groups values; tolerance 2%, or 1 under 50; the bound is statistical, about 2.5 sigma, so rare misses are expected; a cell fails only when more than 1% of the values above 1,024 miss):

| Range | Cells | Values per cell | Max rel. error (exact >= 50) | Values outside (max per cell) | Users outside (max per cell) | Values > 1,024 | Of those outside (max) | Failed |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1d | 7 | 20464 | 1.38% | 0 | 0 | 18 | 0 | 0 |
| 7d | 7 | 40506 | 1.33% | 0 | 0 | 67 | 0 | 0 |
| 30d-mid | 7 | 40734 | 1.73% | 0 | 0 | 67 | 0 | 0 |
| 30d | 7 | 40734 | 1.70% | 0 | 0 | 367 | 0 | 0 |
| 365d | 7 | 40734 | 1.70% | 0 | 0 | 367 | 0 | 0 |

Failed cells: 0 of 140 (420 runs). Load average (1 min) at run start: 0.86 to 1.00. Runs that started above 1.0 (the gate gave up): 0 cells.

Step 1b: load average (1 min) at the start of every run: 0.81 to 1.00. Matrix wall: 24m4s.

Notes on step 1b (written by hand after the run):

- The gate held: every run started at a 1-minute load of 0.81 to 1.00, none
  gave up, so no run is flagged (the rewrites' loads are in the layouts
  tables). The load hovered just under the
  threshold (other sessions share the host), so the gate waited before most
  runs; the whole matrix, both volumes, took 24 minutes.
- The comparison with step 1 is roughly comparable at 100k (step 1 ran at
  load 1.3 to 2.3) but flatters step 1b at 1M:
  step 1's 1M cells ran at load 2.4 to 8.7, and its pqgo-1d cells with the
  pqgo source at 5.0 to 6.1. Read the 1M speedups as upper bounds. The
  re-timed 1M writes say the same: pqgo-1d's slowest file took 4.1 s behind
  the gate against 8.2 s in step 1, while arrow-1d's barely moved (5.1 s
  against 5.2 s).
- Exact `attrvalues` keeps every (pair, actor) key until the merge, so its
  peak RSS grew at 365d at 1M (395 MB against step 1's 179 MB with maps);
  `attrvalues-approx` is 139 MB there, and the trio's 365d peak is set by
  `dau` (186 MB).
- Approx error: the generator draws the same actors every month and has no
  `group_id`, so the groups half of the compared values is 0 on both sides
  (trivially exact), and 30d and 365d have the same distinct users per value
  (hence identical error rows). No value fell outside the tolerance at
  either volume; the largest error on an exact count of 50 or more was 1.73%
  (1M, 30d-mid). The bound is statistical (about 2.5 sigma), so rare misses
  remain possible on other data.
- Rows for the approx comparison come from run 1 of each `attrvalues` and
  `attrvalues-approx` cell (`-rows`), written after the measurement; they are
  kept under `<root>/<volume>/rows1b/`, and every cell under
  `<root>/matrix1b-<volume>.json`.

## Step 3f: built-in dashboards

Run 2026-10-23 from 07:36Z (92 cells, 1h04m); the 6 cells the gate gave up on (load 1.6 to 5.3) and one whose warm runs were slower than its first (native Product 365 d at 1M) were re-run from 08:40Z, all behind the gate, and their first numbers dropped. Every cell is a fresh process (`native-bench dashboards`, which runs the main module's `TestMeasureSystemDashboard`, `internal/app/measure_system_test.go`) with GOMAXPROCS=2: one dashboard over one range ending 2026-09-30, every widget requested at once as the console sends them, once ("first") and then 3 times more ("best" = the fastest of those); wall is from the first request to the last answer. Native = the dashboard's query widgets through `reporting.Service.WidgetData` with `native.New`'s defaults (MaxScans 2, 2 workers each). DuckDB = the old SQL kept for parity (`internal/reporting/testdata/system-sql`, and `vitals-by-device-browser.sql` for the two vitals widgets split from it) through `Service.Preview` (uncached), so through `archive.Reader` and the engine child at the defaults (2 threads at GOMAXPROCS=2, one query at a time, 512 MB); its first run includes spawning the child and building the views. Neither side has a result cache; native keeps file footers and identities between runs in the process, so its warm runs reuse them. Peak = VmHWM: native is the Go process; DuckDB is the Go process plus the engine child ("Go + child"). Before every cell the bench waits until the 1-minute load average is at most 1.0 (polling every 10s, giving up after 5m0s and flagging the cell). Data: `archivetest.Generated` at 100k and 1M events a month, 12 months (2025-10 to 2026-09), with group ids (2,000 groups) and web vitals (half the measures), then the daily pass (sessions, identities, aliases); new files under `~/.cache/twbench-native/3f/`, since step 1's had neither group ids nor vitals.

### Step 3f, volume 100k (100000 events a month)

| Dashboard (widgets native / SQL) | Range | Native first | Native best | DuckDB first | DuckDB best | Native peak | DuckDB peak (Go + child) | Slowest native widget (best run) | Load at start (native / DuckDB) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20 / 20) | 7 d | 297 ms | 278 ms | 494 ms | 376 ms | 87 MB | 145 MB (42 MB + 104 MB) | browsers 277 ms | 0.96 / 0.96 |
| Views (20 / 20) | 30 d | 382 ms | 377 ms | 531 ms | 446 ms | 93 MB | 159 MB (42 MB + 117 MB) | consent-states 360 ms | 0.90 / 0.90 |
| Views (20 / 20) | 90 d | 1.09 s | 1.03 s | 809 ms | 603 ms | 139 MB | 168 MB (43 MB + 125 MB) | display-resolutions 1008 ms | 0.91 / 0.97 |
| Views (20 / 20) | 365 d | 3.67 s | 3.71 s | 1.45 s | 1.31 s | 160 MB | 254 MB (42 MB + 212 MB) | top-pages 3712 ms | 0.87 / 0.96 |
| Product (10 / 10) | 7 d | 166 ms | 150 ms | 292 ms | 211 ms | 89 MB | 168 MB (42 MB + 126 MB) | users-by-app-version 147 ms | 0.96 / 0.97 |
| Product (10 / 10) | 30 d | 280 ms | 228 ms | 418 ms | 308 ms | 110 MB | 225 MB (42 MB + 183 MB) | events-by-name 211 ms | 0.90 / 0.90 |
| Product (10 / 10) | 90 d | 640 ms | 605 ms | 465 ms | 364 ms | 155 MB | 224 MB (42 MB + 182 MB) | peak-dau 604 ms | 0.97 / 0.96 |
| Product (10 / 10) | 365 d | 2.10 s | 2.08 s | 737 ms | 633 ms | 209 MB | 210 MB (42 MB + 167 MB) | attribute-values 2075 ms | 0.89 / 0.99 |
| Users (6 / 6) | 7 d | 70 ms | 44 ms | 247 ms | 155 ms | 76 MB | 157 MB (41 MB + 116 MB) | daily-active-users 43 ms | 0.97 / 0.97 |
| Users (6 / 6) | 30 d | 80 ms | 65 ms | 272 ms | 165 ms | 83 MB | 149 MB (41 MB + 107 MB) | daily-active-users 57 ms | 0.90 / 0.90 |
| Users (6 / 6) | 90 d | 178 ms | 154 ms | 297 ms | 201 ms | 108 MB | 162 MB (41 MB + 121 MB) | actions 154 ms | 0.96 / 0.96 |
| Groups (6 / 6) | 7 d | 47 ms | 20 ms | 222 ms | 137 ms | 70 MB | 150 MB (41 MB + 109 MB) | actions-per-group 19 ms | 0.97 / 0.97 |
| Groups (6 / 6) | 30 d | 54 ms | 31 ms | 232 ms | 144 ms | 77 MB | 152 MB (41 MB + 111 MB) | new-groups 20 ms | 0.90 / 0.99 |
| Groups (6 / 6) | 90 d | 114 ms | 82 ms | 260 ms | 169 ms | 91 MB | 163 MB (41 MB + 122 MB) | active-groups 71 ms | 0.96 / 0.96 |
| Retention (5 / 5) | 7 d | 182 ms | 1.6 ms | 378 ms | 287 ms | 81 MB | 146 MB (42 MB + 105 MB) | install-milestones 1 ms | 0.89 / 0.89 |
| Retention (5 / 5) | 30 d | 178 ms | 1.9 ms | 422 ms | 280 ms | 91 MB | 150 MB (42 MB + 108 MB) | cohorts 0 ms | 0.99 / 0.99 |
| Retention (5 / 5) | 90 d | 186 ms | 1.9 ms | 381 ms | 294 ms | 80 MB | 145 MB (41 MB + 104 MB) | cohorts 0 ms | 0.96 / 0.96 |
| Web Vitals (11 / 10) | 7 d | 41 ms | 38 ms | 283 ms | 188 ms | 64 MB | 150 MB (41 MB + 109 MB) | by-device 31 ms | 0.89 / 0.89 |
| Web Vitals (11 / 10) | 30 d | 49 ms | 44 ms | 275 ms | 189 ms | 65 MB | 150 MB (42 MB + 109 MB) | by-device 43 ms | 0.99 / 0.99 |
| Web Vitals (11 / 10) | 90 d | 122 ms | 117 ms | 301 ms | 214 ms | 77 MB | 156 MB (42 MB + 114 MB) | p75-by-day 110 ms | 0.87 / 0.87 |
| Measures (2 / 2) | 7 d | 11 ms | 5.7 ms | 120 ms | 43 ms | 62 MB | 132 MB (41 MB + 92 MB) | metrics 5 ms | 0.89 / 0.89 |
| Measures (2 / 2) | 30 d | 12 ms | 8.8 ms | 115 ms | 44 ms | 58 MB | 139 MB (41 MB + 97 MB) | p75-by-day 8 ms | 0.91 / 0.91 |
| Measures (2 / 2) | 90 d | 31 ms | 24 ms | 118 ms | 48 ms | 74 MB | 138 MB (41 MB + 98 MB) | p75-by-day 23 ms | 0.87 / 0.87 |

### Step 3f, volume 1m (1000000 events a month)

| Dashboard (widgets native / SQL) | Range | Native first | Native best | DuckDB first | DuckDB best | Native peak | DuckDB peak (Go + child) | Slowest native widget (best run) | Load at start (native / DuckDB) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20 / 20) | 7 d | 911 ms | 874 ms | 732 ms | 604 ms | 135 MB | 162 MB (42 MB + 120 MB) | visitors-by-kind 874 ms | 0.91 / 1.00 |
| Views (20 / 20) | 30 d | 2.20 s | 2.15 s | 1.24 s | 1.09 s | 171 MB | 208 MB (42 MB + 166 MB) | devices 2116 ms | 1.00 / 0.94 |
| Views (20 / 20) | 90 d | 6.70 s | 6.11 s | 2.88 s | 2.78 s | 238 MB | 387 MB (43 MB + 344 MB) | countries 6111 ms | 0.93 / 0.88 |
| Views (20 / 20) | 365 d | 24.47 s | 23.94 s | 14.57 s | 11.49 s | 651 MB | 810 MB (44 MB + 767 MB) | devices 23939 ms | 0.91 / 0.92 |
| Product (10 / 10) | 7 d | 712 ms | 698 ms | 619 ms | 522 ms | 169 MB | 328 MB (42 MB + 286 MB) | peak-dau 685 ms | 1.00 / 0.90 |
| Product (10 / 10) | 30 d | 1.64 s | 1.51 s | 1.89 s | 1.71 s | 236 MB | 790 MB (42 MB + 748 MB) | avg-dau 1514 ms | 0.94 / 0.96 |
| Product (10 / 10) | 90 d | 4.33 s | 4.38 s | 2.55 s | 2.39 s | 315 MB | 657 MB (43 MB + 614 MB) | attribute-values 4377 ms | 0.92 / 0.88 |
| Product (10 / 10) | 365 d | 17.74 s | 16.88 s | 5.72 s | 5.64 s | 693 MB | 886 MB (43 MB + 843 MB) | avg-dau 16879 ms | 0.97 / 0.97 |
| Users (6 / 6) | 7 d | 333 ms | 193 ms | 352 ms | 260 ms | 152 MB | 173 MB (41 MB + 132 MB) | actions 192 ms | 0.90 / 0.98 |
| Users (6 / 6) | 30 d | 645 ms | 511 ms | 521 ms | 396 ms | 216 MB | 218 MB (42 MB + 176 MB) | daily-active-users 511 ms | 0.96 / 0.96 |
| Users (6 / 6) | 90 d | 1.62 s | 1.69 s | 849 ms | 749 ms | 281 MB | 265 MB (42 MB + 222 MB) | new-users 1665 ms | 0.96 / 0.92 |
| Groups (6 / 6) | 7 d | 239 ms | 113 ms | 266 ms | 179 ms | 131 MB | 155 MB (42 MB + 113 MB) | new-groups 101 ms | 0.98 / 0.98 |
| Groups (6 / 6) | 30 d | 349 ms | 246 ms | 326 ms | 243 ms | 143 MB | 164 MB (41 MB + 123 MB) | actions 245 ms | 1.00 / 1.00 |
| Groups (6 / 6) | 90 d | 830 ms | 728 ms | 538 ms | 409 ms | 162 MB | 189 MB (42 MB + 148 MB) | new-groups 727 ms | 1.00 / 1.00 |
| Retention (5 / 5) | 7 d | 1.94 s | 2.1 ms | 1.32 s | 1.23 s | 116 MB | 183 MB (43 MB + 141 MB) | cohorts 0 ms | 0.98 / 0.95 |
| Retention (5 / 5) | 30 d | 2.06 s | 1.6 ms | 1.31 s | 1.21 s | 118 MB | 186 MB (43 MB + 143 MB) | install-milestones 1 ms | 1.00 / 1.00 |
| Retention (5 / 5) | 90 d | 1.96 s | 2.1 ms | 1.39 s | 1.21 s | 116 MB | 186 MB (42 MB + 143 MB) | install-milestones 2 ms | 1.00 / 1.00 |
| Web Vitals (11 / 10) | 7 d | 82 ms | 80 ms | 342 ms | 247 ms | 76 MB | 154 MB (41 MB + 113 MB) | by-page 72 ms | 0.91 / 0.91 |
| Web Vitals (11 / 10) | 30 d | 153 ms | 111 ms | 406 ms | 334 ms | 81 MB | 155 MB (42 MB + 114 MB) | by-browser 110 ms | 0.94 / 0.94 |
| Web Vitals (11 / 10) | 90 d | 334 ms | 317 ms | 438 ms | 340 ms | 104 MB | 160 MB (42 MB + 118 MB) | lcp 300 ms | 1.00 / 0.91 |
| Measures (2 / 2) | 7 d | 21 ms | 16 ms | 138 ms | 51 ms | 72 MB | 136 MB (41 MB + 95 MB) | metrics 12 ms | 1.00 / 1.00 |
| Measures (2 / 2) | 30 d | 29 ms | 29 ms | 162 ms | 71 ms | 83 MB | 141 MB (41 MB + 100 MB) | p75-by-day 29 ms | 0.93 / 0.93 |
| Measures (2 / 2) | 90 d | 89 ms | 67 ms | 153 ms | 77 ms | 93 MB | 149 MB (41 MB + 108 MB) | metrics 66 ms | 0.91 / 0.91 |

Load average (1 min) at cell start: 0.87 to 1.00; cells that started above 1.0 (the gate gave up): 0 of 92.

Analytics tools at 1M, 30 d (2026-09-01 to 09-30), one process (`go test -tags measure -run TestMeasureTools ./internal/api`, `internal/api/measure_tools_test.go`), GOMAXPROCS=2, started at load 0.89; each tool alone, twice on each side; old SQL = the parity harness's constants through `filesTable` (archive.Reader, engine child at 2 threads, started by a throwaway query before the first timing):

| Tool | Native first | Native second | Old SQL first | Old SQL second |
| --- | --- | --- | --- | --- |
| views_overview | 145.8 ms | 130.5 ms | 85.3 ms | 67.7 ms |
| product_events | 237.7 ms | 260.7 ms | 152.6 ms | 139.1 ms |
| event_names | 21.7 ms | 22.2 ms | 24.1 ms | 24.3 ms |
| product_attributes | 1026.6 ms | 970.5 ms | 3535.8 ms | 3527.6 ms |
| measures | 21.5 ms | 20.8 ms | 46.0 ms | 33.8 ms |
| retention (user) | 1775.9 ms | 185.1 ms | 2154.5 ms | 2208.4 ms |
| identities (user) | 436.3 ms | 363.2 ms | 168.5 ms | 160.5 ms |
| identities (group) | 224.2 ms | 221.9 ms | 103.3 ms | 99.5 ms |
| views_breakdown app_versions | 63.5 ms | 59.8 ms | 35.8 ms | 37.7 ms |
| views_breakdown browsers | 123.5 ms | 125.2 ms | 80.6 ms | 80.6 ms |
| views_breakdown consent | 128.9 ms | 127.9 ms | 58.1 ms | 66.3 ms |
| views_breakdown countries | 132.1 ms | 129.5 ms | 62.8 ms | 64.8 ms |
| views_breakdown devices | 123.0 ms | 118.1 ms | 78.1 ms | 77.6 ms |
| views_breakdown displays | 145.1 ms | 152.6 ms | 90.4 ms | 88.6 ms |
| views_breakdown hosts | 115.2 ms | 117.8 ms | 70.7 ms | 69.3 ms |
| views_breakdown kinds | 117.4 ms | 113.4 ms | 61.6 ms | 64.5 ms |
| views_breakdown locales | 123.5 ms | 133.2 ms | 79.0 ms | 76.1 ms |
| views_breakdown os | 129.3 ms | 128.4 ms | 91.5 ms | 80.5 ms |
| views_breakdown paths | 170.9 ms | 165.2 ms | 115.7 ms | 109.9 ms |
| views_breakdown platforms | 119.9 ms | 112.1 ms | 70.5 ms | 65.0 ms |
| views_breakdown referrers | 137.8 ms | 138.3 ms | 87.2 ms | 79.5 ms |
| views_breakdown utm | 60.7 ms | 66.5 ms | 44.5 ms | 41.1 ms |

Notes on step 3f (written by hand after the run):

- The files are DuckDB-written month files, as this branch's daily pass
  writes them: one row group a month at 100k, 9 groups of 122,880 rows at
  1M. Step 1b chose 1-day groups (`pqgo-1d`), which come with the Go
  writer (step 4, not on this branch), so no scan here prunes days inside
  a month: a 7-day range reads the whole month's groups of its family.
  Re-measure after step 4.
- Each widget is its own scan and at most two scan at once (MaxScans 2),
  so a dashboard costs roughly the sum of its scans over two. The
  per-widget times in the cells include that queueing; the slowest-widget
  column is the last to finish, not the most expensive scan. One native
  breakdown at 1M and 30 d takes 110 to 170 ms (tools table) against 60
  to 115 ms for DuckDB's one query; Views has 20 such scans.
- Retention's first run reads the identities, which the native engine
  then caches in the process, so its warm runs (about 2 ms) reuse them;
  its first run is the honest uncached figure (1.9 to 2.1 s at 1M).
  Product's attribute-values and the DAU widgets dominate its long ranges.
- Peak memory grows with the range on the native side: 651 MB (Views) and
  693 MB (Product) at 1M and 365 d. It is below the DuckDB Go + child sum
  in every cell but one (Users, 90 d, 1M: 281 against 265 MB).
- Idle RSS before the first request was 37 to 38 MB in both modes; that is
  the measure test binary (which carries both engines and the tests), not
  the server.
- The generated data: 2,000 groups on a quarter of the product events and
  on the views of signed-in visitors; half the measures are vitals ($lcp,
  $inp, $cls, $fcp, $ttfb with a device and a browser). Users and groups
  repeat every month, as in step 1, so distinct counts over 30 d and 365 d
  are close.
- Every cell is in `~/.cache/twbench-native/3f/<volume>/cells/` and
  `~/.cache/twbench-native/3f/dashboards.json`; the first pass's table is
  `RESULTS-3f-run1.md` beside them.

## Step 4: the daily pass in Go

Started 2026-10-23T12:48:25Z. Every run is a fresh process (`native-bench pass`, which runs the main module's `TestMeasurePass`, `internal/archive/measure_pass_test.go`) at GOMAXPROCS=2, once ("first") and 3 times more ("best" = the fastest of those). The month is step 3f's 2026-09 file, written as one NDJSON log a day as ingest writes them. Cases: **pass** is the merge day's pass (the last day's log dumped, the month merged from it and the 29 other days' files, then its sessions, identities and aliases derived); **dump** is the 30 logs dumped one after another; **leftover** is the merge and the derive with a crashed merge's month file (the DuckDB-written source) next to the Go day files; **wide** logs spread each of the 8 product attributes over 250 names (2,000 keys). Go runs `dumpLog`, `mergeFiles` and `Archive.derive` in the process; DuckDB runs the old statements in an engine child of 2 threads and 512 MB (the server's defaults at GOMAXPROCS=2), spawned before the clock. Peak = VmHWM, reset at each phase (clear_refs), so a phase's peak includes what the one before left resident; for DuckDB the Go process plus the child. The cell's peak is the largest over its runs and phases. GC = collections and the GC's CPU time in the best run, over the phases. Before every cell the bench waits until the 1-minute load average is at most 1.0.

### Step 4 pass, volume 100k (100000 events a month)

| Case | Side | GOMEMLIMIT | First | Best | Best by phase (dump / merge / derive) | Peak | Peak by phase | Baseline | GCs / GC CPU | Month file | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pass | go | none | 1.22 s | 1.22 s | dump 109 ms, merge 864 ms, derive 246 ms | 56 MB | dump 42 MB, merge 55 MB, derive 56 MB | 25 MB | 41 / 73 ms | 5 MB, 49 cols, 90 groups | 0.86 |
| pass | go | 128MiB | 1.25 s | 1.23 s | dump 103 ms, merge 881 ms, derive 242 ms | 55 MB | dump 42 MB, merge 54 MB, derive 55 MB | 25 MB | 41 / 78 ms | 5 MB, 49 cols, 90 groups | 0.95 |
| pass | duckdb | none | 829 ms | 812 ms | dump 116 ms, merge 514 ms, derive 182 ms | 364 MB | dump 119 MB, merge 364 MB, derive 364 MB | 30 MB | 4 / 1.8 ms | 4 MB, 49 cols, 1 groups | 0.95 |
| pass | duckdb | 128MiB | 785 ms | 785 ms | dump 113 ms, merge 499 ms, derive 174 ms | 366 MB | dump 119 MB, merge 366 MB, derive 365 MB | 30 MB | 4 / 1.8 ms | 4 MB, 49 cols, 1 groups | 0.95 |
| dump | go | none | 2.82 s | 2.81 s | dump 2.81 s | 50 MB | dump 50 MB | 25 MB | 167 / 319 ms | 0 MB, 0 cols, 0 groups | 0.86 |
| dump | go | 128MiB | 2.86 s | 2.82 s | dump 2.82 s | 50 MB | dump 50 MB | 26 MB | 162 / 323 ms | 0 MB, 0 cols, 0 groups | 0.95 |
| dump | duckdb | none | 2.97 s | 2.91 s | dump 2.91 s | 245 MB | dump 245 MB | 30 MB | 132 / 58 ms | 0 MB, 0 cols, 0 groups | 0.86 |
| dump | duckdb | 128MiB | 2.98 s | 2.89 s | dump 2.89 s | 258 MB | dump 258 MB | 30 MB | 133 / 57 ms | 0 MB, 0 cols, 0 groups | 0.96 |
| leftover | go | none | 1.08 s | 1.07 s | merge 847 ms, derive 227 ms | 82 MB | merge 78 MB, derive 82 MB | 25 MB | 26 / 46 ms | 5 MB, 49 cols, 90 groups | 0.86 |
| leftover | go | 128MiB | 1.08 s | 1.07 s | merge 842 ms, derive 225 ms | 80 MB | merge 79 MB, derive 80 MB | 25 MB | 26 / 50 ms | 5 MB, 49 cols, 90 groups | 0.87 |
| leftover | duckdb | none | 705 ms | 705 ms | merge 530 ms, derive 175 ms | 360 MB | merge 352 MB, derive 360 MB | 30 MB | 0 / 0.0 ms | 4 MB, 49 cols, 1 groups | 0.88 |
| leftover | duckdb | 128MiB | 724 ms | 706 ms | merge 528 ms, derive 178 ms | 389 MB | merge 352 MB, derive 389 MB | 30 MB | 0 / 0.0 ms | 4 MB, 49 cols, 1 groups | 1.00 |
| pass (wide) | go | none | 15.38 s | 15.36 s | dump 434 ms, merge 14.20 s, derive 723 ms | 349 MB | dump 47 MB, merge 178 MB, derive 349 MB | 25 MB | 73 / 517 ms | 38 MB, 2041 cols, 90 groups | 1.00 |
| pass (wide) | go | 128MiB | 15.90 s | 15.89 s | dump 459 ms, merge 14.17 s, derive 1.26 s | 217 MB | dump 47 MB, merge 148 MB, derive 217 MB | 25 MB | 117 / 1.66 s | 38 MB, 2041 cols, 90 groups | 0.89 |
| pass (wide) | duckdb | none | failed: measure_pass_test.go:185: Out of Memory Error: failed to pin block of size 256.0 KiB (488.1 MiB/488.2 MiB used) | | | | | | | | 0.95 |
| pass (wide) | duckdb | 128MiB | failed: measure_pass_test.go:185: Out of Memory Error: failed to pin block of size 256.0 KiB (488.1 MiB/488.2 MiB used) | | | | | | | | 0.95 |

### Step 4 pass, volume 1m (1000000 events a month)

| Case | Side | GOMEMLIMIT | First | Best | Best by phase (dump / merge / derive) | Peak | Peak by phase | Baseline | GCs / GC CPU | Month file | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pass | go | none | 9.62 s | 9.62 s | dump 873 ms, merge 6.77 s, derive 1.98 s | 146 MB | dump 121 MB, merge 146 MB, derive 91 MB | 25 MB | 123 / 529 ms | 43 MB, 49 cols, 90 groups | 0.95 |
| pass | go | 128MiB | 9.66 s | 9.61 s | dump 870 ms, merge 6.76 s, derive 1.98 s | 143 MB | dump 118 MB, merge 143 MB, derive 88 MB | 26 MB | 126 / 528 ms | 43 MB, 49 cols, 90 groups | 0.88 |
| pass | duckdb | none | 8.15 s | 7.67 s | dump 834 ms, merge 5.69 s, derive 1.15 s | 918 MB | dump 205 MB, merge 918 MB, derive 850 MB | 30 MB | 42 / 17 ms | 38 MB, 49 cols, 9 groups | 1.00 |
| pass | duckdb | 128MiB | 7.83 s | 7.83 s | dump 866 ms, merge 5.76 s, derive 1.20 s | 967 MB | dump 209 MB, merge 967 MB, derive 912 MB | 30 MB | 42 / 18 ms | 38 MB, 49 cols, 9 groups | 0.96 |
| dump | go | none | 25.78 s | 25.78 s | dump 25.78 s | 138 MB | dump 138 MB | 25 MB | 331 / 3.87 s | 0 MB, 0 cols, 0 groups | 0.90 |
| dump | go | 128MiB | 25.83 s | 25.83 s | dump 25.83 s | 135 MB | dump 135 MB | 25 MB | 339 / 3.87 s | 0 MB, 0 cols, 0 groups | 0.86 |
| dump | duckdb | none | 24.78 s | 24.78 s | dump 24.78 s | 411 MB | dump 411 MB | 30 MB | 1300 / 562 ms | 0 MB, 0 cols, 0 groups | 0.97 |
| dump | duckdb | 128MiB | 25.74 s | 24.60 s | dump 24.60 s | 435 MB | dump 435 MB | 31 MB | 1299 / 555 ms | 0 MB, 0 cols, 0 groups | 1.35 (unquiet) |
| leftover | go | none | 8.99 s | 8.94 s | merge 7.02 s, derive 1.91 s | 176 MB | merge 176 MB, derive 142 MB | 25 MB | 94 / 360 ms | 44 MB, 49 cols, 90 groups | 0.98 |
| leftover | go | 128MiB | 9.07 s | 9.00 s | merge 7.05 s, derive 1.94 s | 149 MB | merge 149 MB, derive 142 MB | 26 MB | 175 / 472 ms | 44 MB, 49 cols, 90 groups | 0.96 |
| leftover | duckdb | none | 7.59 s | 7.52 s | merge 6.35 s, derive 1.18 s | 904 MB | merge 904 MB, derive 838 MB | 31 MB | 0 / 0.0 ms | 38 MB, 49 cols, 9 groups | 0.88 |
| leftover | duckdb | 128MiB | 7.75 s | 7.41 s | merge 6.24 s, derive 1.18 s | 888 MB | merge 888 MB, derive 802 MB | 30 MB | 0 / 0.0 ms | 38 MB, 49 cols, 9 groups | 0.86 |
| pass (wide) | go | none | 91.23 s | 90.90 s | dump 3.23 s, merge 85.37 s, derive 2.30 s | 398 MB | dump 130 MB, merge 223 MB, derive 398 MB | 25 MB | 90 / 981 ms | 104 MB, 2041 cols, 90 groups | 0.86 |
| pass (wide) | go | 128MiB | 95.95 s | 95.00 s | dump 3.26 s, merge 85.87 s, derive 5.88 s | 227 MB | dump 125 MB, merge 151 MB, derive 227 MB | 26 MB | 309 / 8.16 s | 104 MB, 2041 cols, 90 groups | 0.94 |
| pass (wide) | duckdb | none | failed: measure_pass_test.go:185: Out of Memory Error: failed to pin block of size 256.0 KiB (488.0 MiB/488.2 MiB used) | | | | | | | | 0.93 |
| pass (wide) | duckdb | 128MiB | failed: measure_pass_test.go:185: Out of Memory Error: failed to pin block of size 256.0 KiB (488.3 MiB/488.2 MiB used) | | | | | | | | 0.86 |

Load average (1 min) at cell start: 0.86 to 1.35; cells that started above 1.0 (the gate gave up): 1 of 32.

Notes on the step 4 pass (written by hand after the run):

- Extra runs at GOMEMLIMIT=256MiB (1M, Go, one run each, started at load
  0.97): the pass 9.61 s, peak 129 MB, GC CPU 0.5 s; the wide pass 92.4 s,
  peak 280 MB, GC CPU 1.6 s. The unit and compose files now ship 256MiB.
- The wide merge first peaked at 457 MB: the merge held all 30 inputs'
  footers decoded (parquet-go, about 1 KB per column per row group, about
  6 MB a wide day file, 178 MB live in a heap profile). It now holds at most
  four (`maxFooters`); the table's 223 MB is that version. The wide month's
  own footer (2,041 columns, 90 groups) is about 150 MB decoded, which the
  derive reads: its peak is the derive's.
- DuckDB's wide failures are its own memory limit (`Out of Memory Error`,
  488 MiB used) in the first statement each case runs: the dump of the
  last day's log (and, run alone, the merge of the 30 Go-written days).
- A wide pass spends about 80% of its CPU in the by-column writer, encoding
  NULLs: 2,041 columns of every row of every group.
- The baseline (25 MB) is the measure test binary at rest, not the server.
- Every cell is in `~/.cache/twbench-native/4/pass/<volume>/cells/` and
  `~/.cache/twbench-native/4/pass/pass.json`.

## Step 4: built-in dashboards on files the Go pass wrote

Started 2026-10-23T13:45:28Z. The same cells as step 3f (`native-bench dashboards`, `TestMeasureSystemDashboard`, one fresh process a cell, GOMAXPROCS=2, every widget at once, a first run then 3 more, "best" the fastest), native only, on step 3f's events rebuilt through the daily pass in Go: each month written as one log a day, then dumped, merged and derived a month at a time (`MEASURE_FROM`), so a month file has a row group per family per day (about 90) where step 3f's DuckDB-written ones had one (100k) or 9 of 122,880 rows (1M). "3f" columns are step 3f's cells; "change" is step 4's best over step 3f's native best. Before every cell the bench waits until the 1-minute load average is at most 1.0.

### Step 4 dashboards, volume 100k (100000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 297 ms / 278 ms | 352 ms / 316 ms | 1.14x | 376 ms | 87 MB | 91 MB | visitors 313 ms | 0.97 |
| Views (20) | 30 d | 382 ms / 377 ms | 497 ms / 461 ms | 1.23x | 446 ms | 93 MB | 92 MB | consent-share 461 ms | 0.97 |
| Views (20) | 90 d | 1.09 s / 1.03 s | 1.44 s / 1.40 s | 1.36x | 603 ms | 139 MB | 107 MB | referrers 1383 ms | 0.89 |
| Views (20) | 365 d | 3.67 s / 3.71 s | 5.00 s / 5.02 s | 1.35x | 1.31 s | 160 MB | 139 MB | referrers 5005 ms | 0.96 |
| Product (10) | 7 d | 166 ms / 150 ms | 259 ms / 192 ms | 1.28x | 211 ms | 89 MB | 103 MB | daily-active-users 192 ms | 0.88 |
| Product (10) | 30 d | 280 ms / 228 ms | 432 ms / 310 ms | 1.36x | 308 ms | 110 MB | 120 MB | peak-dau 306 ms | 0.97 |
| Product (10) | 90 d | 640 ms / 605 ms | 1.09 s / 891 ms | 1.47x | 364 ms | 155 MB | 155 MB | daily-active-users 876 ms | 0.94 |
| Product (10) | 365 d | 2.10 s / 2.08 s | 3.33 s / 3.23 s | 1.56x | 633 ms | 209 MB | 169 MB | peak-dau 3215 ms | 0.87 |
| Users (6) | 7 d | 70 ms / 44 ms | 130 ms / 106 ms | 2.41x | 155 ms | 76 MB | 98 MB | daily-active-users 94 ms | 0.88 |
| Users (6) | 30 d | 80 ms / 65 ms | 193 ms / 149 ms | 2.29x | 165 ms | 83 MB | 109 MB | actions 148 ms | 0.97 |
| Users (6) | 90 d | 178 ms / 154 ms | 462 ms / 447 ms | 2.90x | 201 ms | 108 MB | 123 MB | new-users 434 ms | 0.94 |
| Groups (6) | 7 d | 47 ms / 20 ms | 121 ms / 97 ms | 4.93x | 137 ms | 70 MB | 99 MB | actions-per-group 89 ms | 0.88 |
| Groups (6) | 30 d | 54 ms / 31 ms | 179 ms / 140 ms | 4.48x | 144 ms | 77 MB | 98 MB | daily-active-groups 140 ms | 0.89 |
| Groups (6) | 90 d | 114 ms / 82 ms | 441 ms / 400 ms | 4.85x | 169 ms | 91 MB | 105 MB | daily-active-groups 376 ms | 0.91 |
| Retention (5) | 7 d | 182 ms / 1.6 ms | 395 ms / 1.2 ms | 0.72x | 287 ms | 81 MB | 74 MB | signed-in-retention 1 ms | 0.88 |
| Retention (5) | 30 d | 178 ms / 1.9 ms | 405 ms / 1.3 ms | 0.70x | 280 ms | 91 MB | 72 MB | cohorts 1 ms | 0.89 |
| Retention (5) | 90 d | 186 ms / 1.9 ms | 388 ms / 1.5 ms | 0.80x | 294 ms | 80 MB | 73 MB | install-milestones 1 ms | 0.91 |
| Web Vitals (11) | 7 d | 41 ms / 38 ms | 174 ms / 162 ms | 4.22x | 188 ms | 64 MB | 87 MB | by-device 155 ms | 0.88 |
| Web Vitals (11) | 30 d | 49 ms / 44 ms | 192 ms / 170 ms | 3.86x | 189 ms | 65 MB | 85 MB | cls 170 ms | 0.89 |
| Web Vitals (11) | 90 d | 122 ms / 117 ms | 553 ms / 504 ms | 4.30x | 214 ms | 77 MB | 91 MB | ttfb 503 ms | 0.91 |
| Measures (2) | 7 d | 11 ms / 5.7 ms | 48 ms / 33 ms | 5.81x | 43 ms | 62 MB | 69 MB | metrics 28 ms | 0.88 |
| Measures (2) | 30 d | 12 ms / 8.8 ms | 58 ms / 32 ms | 3.61x | 44 ms | 58 MB | 76 MB | metrics 31 ms | 0.89 |
| Measures (2) | 90 d | 31 ms / 24 ms | 116 ms / 99 ms | 4.17x | 48 ms | 74 MB | 82 MB | p75-by-day 98 ms | 0.96 |

### Step 4 dashboards, volume 1m (1000000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 911 ms / 874 ms | 742 ms / 711 ms | 0.81x | 604 ms | 135 MB | 102 MB | visitors-by-country 710 ms | 0.93 |
| Views (20) | 30 d | 2.20 s / 2.15 s | 2.15 s / 2.09 s | 0.98x | 1.09 s | 171 MB | 141 MB | consent-share 2094 ms | 0.94 |
| Views (20) | 90 d | 6.70 s / 6.11 s | 6.11 s / 6.09 s | 1.00x | 2.78 s | 238 MB | 225 MB | referrers 6086 ms | 0.98 |
| Views (20) | 365 d | 24.47 s / 23.94 s | 24.09 s / 23.71 s | 0.99x | 11.49 s | 651 MB | 534 MB | visitors-by-kind 23707 ms | 0.89 |
| Product (10) | 7 d | 712 ms / 698 ms | 560 ms / 472 ms | 0.68x | 522 ms | 169 MB | 164 MB | users-by-app-version 459 ms | 0.94 |
| Product (10) | 30 d | 1.64 s / 1.51 s | 1.51 s / 1.45 s | 0.95x | 1.71 s | 236 MB | 200 MB | events 1446 ms | 0.87 |
| Product (10) | 90 d | 4.33 s / 4.38 s | 4.13 s / 4.00 s | 0.91x | 2.39 s | 315 MB | 266 MB | avg-dau 3997 ms | 0.98 |
| Product (10) | 365 d | 17.74 s / 16.88 s | 15.44 s / 15.76 s | 0.93x | 5.64 s | 693 MB | 591 MB | avg-dau 15737 ms | 0.90 |
| Users (6) | 7 d | 333 ms / 193 ms | 336 ms / 235 ms | 1.22x | 260 ms | 152 MB | 150 MB | top-users 235 ms | 0.94 |
| Users (6) | 30 d | 645 ms / 511 ms | 848 ms / 648 ms | 1.27x | 396 ms | 216 MB | 214 MB | new-users 648 ms | 0.99 |
| Users (6) | 90 d | 1.62 s / 1.69 s | 1.95 s / 1.86 s | 1.10x | 749 ms | 281 MB | 278 MB | new-users 1860 ms | 0.90 |
| Groups (6) | 7 d | 239 ms / 113 ms | 267 ms / 161 ms | 1.42x | 179 ms | 131 MB | 128 MB | new-groups 132 ms | 0.94 |
| Groups (6) | 30 d | 349 ms / 246 ms | 508 ms / 390 ms | 1.58x | 243 ms | 143 MB | 153 MB | daily-active-groups 389 ms | 0.99 |
| Groups (6) | 90 d | 830 ms / 728 ms | 1.35 s / 1.17 s | 1.61x | 409 ms | 162 MB | 177 MB | new-groups 1163 ms | 0.99 |
| Retention (5) | 7 d | 1.94 s / 2.1 ms | 1.76 s / 2.1 ms | 1.00x | 1.23 s | 116 MB | 90 MB | cohorts 0 ms | 0.94 |
| Retention (5) | 30 d | 2.06 s / 1.6 ms | 1.76 s / 1.7 ms | 1.10x | 1.21 s | 118 MB | 90 MB | cohorts 0 ms | 0.99 |
| Retention (5) | 90 d | 1.96 s / 2.1 ms | 1.79 s / 1.4 ms | 0.65x | 1.21 s | 116 MB | 90 MB | cohorts 1 ms | 0.97 |
| Web Vitals (11) | 7 d | 82 ms / 80 ms | 180 ms / 181 ms | 2.27x | 247 ms | 76 MB | 88 MB | p75-by-day 180 ms | 0.94 |
| Web Vitals (11) | 30 d | 153 ms / 111 ms | 285 ms / 259 ms | 2.34x | 334 ms | 81 MB | 86 MB | by-page 258 ms | 0.99 |
| Web Vitals (11) | 90 d | 334 ms / 317 ms | 792 ms / 775 ms | 2.45x | 340 ms | 104 MB | 99 MB | lcp 760 ms | 0.97 |
| Measures (2) | 7 d | 21 ms / 16 ms | 40 ms / 30 ms | 1.91x | 51 ms | 72 MB | 84 MB | metrics 30 ms | 0.94 |
| Measures (2) | 30 d | 29 ms / 29 ms | 59 ms / 50 ms | 1.69x | 71 ms | 83 MB | 79 MB | p75-by-day 49 ms | 0.99 |
| Measures (2) | 90 d | 89 ms / 67 ms | 162 ms / 140 ms | 2.09x | 77 ms | 93 MB | 85 MB | p75-by-day 139 ms | 0.89 |

Load average (1 min) at cell start: 0.87 to 0.99; cells that started above 1.0 (the gate gave up): 0 of 46.

Notes on the step 4 dashboards (written by hand after the run):

- The rebuild (`MEASURE_FROM`, `rebuildThroughPass` in
  `internal/app/measure_system_test.go`) wrote each 3f month as 30 or 31
  logs and ran `Archive.Maintain` as of the month's end plus two days: at
  100k 4.0 to 4.3 s a month, peak 90 to 131 MB; at 1M 33 to 36 s a month,
  peak 149 to 167 MB (the month's 31 dumps, its merge, its sessions, and
  identities and aliases over every month so far).
- The file layout does not close the Views and Product gap at 1M: only the
  7-day loads gain (0.81x, 0.68x); from 30 days they are within 0 to 9%.
  At 100k, and for the small dashboards at 1M, the 90 groups a month cost
  more than step 3f's one or nine: see the spec's "Measurements (step 4)".
- Every cell is in `~/.cache/twbench-native/4/dash/<volume>/cells/` and
  `~/.cache/twbench-native/4/dash/dashboards.json`.

## Step 4b: a day cuts a group only past T rows

Run 2026-10-23 from 08:25 local (78 cells, 0 above the gate; load 0.85 to 1.00). The step 3f events rebuilt through the Go pass once per T (`ARCHIVE_DAY_CUT_ROWS=T native-bench dashboards -engines native -ranges 7,30,90,365 -small 30 -from <3f root> -before <3f dashboards.json>`, `internal/archive/measure_daycut.go`), native only: Views and Product at 7, 30, 90 and 365 days, the rest at 30. Best of 3 warm runs (Retention: the first load); "per day" is step 4's group per family and day; DuckDB is step 3f's. Chosen: T = 32,768. The per-T tables with first runs and slowest widgets follow; every cell is in `~/.cache/twbench-native/4/dash-T<T>/`.


**100k**

| Dashboard | Range | 3f | per day | T=16384 | T=32768 | T=65536 | DuckDB (3f) | Peak per day / T=16384 / T=32768 / T=65536 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views | 7 d | 278 ms | 316 ms | 82 ms | 81 ms | 140 ms | 376 ms | 91 / 57 / 59 / 64 MB |
| Views | 30 d | 377 ms | 461 ms | 230 ms | 234 ms | 218 ms | 446 ms | 92 / 64 / 69 / 68 MB |
| Views | 90 d | 1.03 s | 1.40 s | 638 ms | 595 ms | 617 ms | 603 ms | 107 / 77 / 79 / 86 MB |
| Views | 1 y | 3.71 s | 5.02 s | 2.26 s | 2.12 s | 2.09 s | 1.31 s | 139 / 110 / 118 / 127 MB |
| Product | 7 d | 150 ms | 192 ms | 91 ms | 112 ms | 121 ms | 211 ms | 103 / 75 / 82 / 77 MB |
| Product | 30 d | 228 ms | 310 ms | 190 ms | 186 ms | 164 ms | 308 ms | 120 / 106 / 102 / 99 MB |
| Product | 90 d | 605 ms | 891 ms | 481 ms | 485 ms | 454 ms | 364 ms | 155 / 130 / 139 / 138 MB |
| Product | 1 y | 2.08 s | 3.23 s | 1.68 s | 1.68 s | 1.59 s | 633 ms | 169 / 166 / 182 / 186 MB |
| Users | 30 d | 65 ms | 149 ms | 73 ms | 70 ms | 59 ms | 165 ms | 109 / 80 / 78 / 82 MB |
| Groups | 30 d | 31 ms | 140 ms | 46 ms | 41 ms | 38 ms | 144 ms | 98 / 69 / 68 / 73 MB |
| Retention (first load) | 30 d | 178 ms | 405 ms | 182 ms | 188 ms | 178 ms | 422 ms | 72 / 60 / 64 / 66 MB |
| Web Vitals | 30 d | 44 ms | 170 ms | 27 ms | 23 ms | 20 ms | 189 ms | 85 / 52 / 49 / 50 MB |
| Measures | 30 d | 8.8 ms | 32 ms | 5.0 ms | 4.8 ms | 4.1 ms | 44 ms | 76 / 47 / 47 / 48 MB |

**1M**

| Dashboard | Range | 3f | per day | T=16384 | T=32768 | T=65536 | DuckDB (3f) | Peak per day / T=16384 / T=32768 / T=65536 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views | 7 d | 874 ms | 711 ms | 581 ms | 540 ms | 639 ms | 604 ms | 102 / 92 / 96 / 94 MB |
| Views | 30 d | 2.15 s | 2.09 s | 1.95 s | 1.93 s | 1.97 s | 1.09 s | 141 / 136 / 131 / 129 MB |
| Views | 90 d | 6.11 s | 6.09 s | 5.47 s | 5.66 s | 5.68 s | 2.78 s | 225 / 210 / 188 / 213 MB |
| Views | 1 y | 23.94 s | 23.71 s | 21.87 s | 21.89 s | 22.70 s | 11.49 s | 534 / 556 / 522 / 517 MB |
| Product | 7 d | 698 ms | 472 ms | 442 ms | 449 ms | 487 ms | 522 ms | 164 / 142 / 148 / 152 MB |
| Product | 30 d | 1.51 s | 1.45 s | 1.37 s | 1.36 s | 1.42 s | 1.71 s | 200 / 194 / 188 / 205 MB |
| Product | 90 d | 4.38 s | 4.00 s | 3.81 s | 3.82 s | 3.96 s | 2.39 s | 266 / 262 / 255 / 271 MB |
| Product | 1 y | 16.88 s | 15.76 s | 14.75 s | 14.77 s | 15.73 s | 5.64 s | 591 / 629 / 670 / 575 MB |
| Users | 30 d | 511 ms | 648 ms | 602 ms | 568 ms | 540 ms | 396 ms | 214 / 200 / 198 / 200 MB |
| Groups | 30 d | 246 ms | 390 ms | 334 ms | 308 ms | 289 ms | 243 ms | 153 / 129 / 130 / 128 MB |
| Retention (first load) | 30 d | 2.06 s | 1.76 s | 1.67 s | 1.78 s | 1.92 s | 1.31 s | 90 / 84 / 84 / 91 MB |
| Web Vitals | 30 d | 111 ms | 259 ms | 176 ms | 140 ms | 123 ms | 334 ms | 86 / 71 / 70 / 68 MB |
| Measures | 30 d | 29 ms | 50 ms | 35 ms | 29 ms | 22 ms | 71 ms | 79 / 66 / 61 / 59 MB |

The 1M pass with T = 32,768 (`TestMeasurePass`, case pass, 4 runs a limit, load 0.86 to 0.98): 26 row groups (90 before), 9.8 s (dump 0.87 s, merge 6.7 s, derive 2.25 s), peak 130 to 136 MB with no limit, 128 to 146 MB at 256MiB, 131 to 139 MB at 128MiB.

### T = 16384

Started 2026-10-23T15:23:04Z. The same cells as step 3f (`native-bench dashboards`, `TestMeasureSystemDashboard`, one fresh process a cell, GOMAXPROCS=2, every widget at once, a first run then 3 more, "best" the fastest), native only, on step 3f's events rebuilt through the daily pass in Go: each month written as one log a day, then dumped, merged and derived a month at a time (`MEASURE_FROM`), so a month file has a row group per family per day (about 90) where step 3f's DuckDB-written ones had one (100k) or 9 of 122,880 rows (1M). "3f" columns are step 3f's cells; "change" is step 4's best over step 3f's native best. Before every cell the bench waits until the 1-minute load average is at most 1.0.

#### Step 4b T=16384 dashboards, volume 100k (100000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 297 ms / 278 ms | 112 ms / 82 ms | 0.30x | 376 ms | 87 MB | 57 MB | top-pages 76 ms | 0.94 |
| Views (20) | 30 d | 382 ms / 377 ms | 248 ms / 230 ms | 0.61x | 446 ms | 93 MB | 64 MB | countries 229 ms | 0.94 |
| Views (20) | 90 d | 1.09 s / 1.03 s | 656 ms / 638 ms | 0.62x | 603 ms | 139 MB | 77 MB | visitors 637 ms | 0.95 |
| Views (20) | 365 d | 3.67 s / 3.71 s | 2.27 s / 2.26 s | 0.61x | 1.31 s | 160 MB | 110 MB | devices 2263 ms | 0.93 |
| Product (10) | 7 d | 166 ms / 150 ms | 115 ms / 91 ms | 0.61x | 211 ms | 89 MB | 75 MB | app-versions 83 ms | 0.94 |
| Product (10) | 30 d | 280 ms / 228 ms | 221 ms / 190 ms | 0.84x | 308 ms | 110 MB | 106 MB | total-events 190 ms | 0.95 |
| Product (10) | 90 d | 640 ms / 605 ms | 512 ms / 481 ms | 0.80x | 364 ms | 155 MB | 130 MB | events 473 ms | 0.95 |
| Product (10) | 365 d | 2.10 s / 2.08 s | 1.72 s / 1.68 s | 0.81x | 633 ms | 209 MB | 166 MB | peak-dau 1666 ms | 1.00 |
| Users (6) | 30 d | 80 ms / 65 ms | 96 ms / 73 ms | 1.12x | 165 ms | 83 MB | 80 MB | actions 72 ms | 0.95 |
| Groups (6) | 30 d | 54 ms / 31 ms | 71 ms / 46 ms | 1.46x | 144 ms | 77 MB | 69 MB | top-groups 45 ms | 0.95 |
| Retention (5) | 30 d | 178 ms / 1.9 ms | 182 ms / 1.4 ms | 0.75x | 280 ms | 91 MB | 60 MB | cohorts 0 ms | 0.95 |
| Web Vitals (11) | 30 d | 49 ms / 44 ms | 27 ms / 27 ms | 0.62x | 189 ms | 65 MB | 52 MB | by-page 23 ms | 0.95 |
| Measures (2) | 30 d | 12 ms / 8.8 ms | 9.1 ms / 5.0 ms | 0.57x | 44 ms | 58 MB | 47 MB | metrics 4 ms | 0.95 |

#### Step 4b T=16384 dashboards, volume 1m (1000000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 911 ms / 874 ms | 628 ms / 581 ms | 0.67x | 604 ms | 135 MB | 92 MB | top-pages 581 ms | 0.85 |
| Views (20) | 30 d | 2.20 s / 2.15 s | 2.00 s / 1.95 s | 0.91x | 1.09 s | 171 MB | 136 MB | consent-states 1953 ms | 0.95 |
| Views (20) | 90 d | 6.70 s / 6.11 s | 5.56 s / 5.47 s | 0.89x | 2.78 s | 238 MB | 210 MB | visitors-by-kind 5452 ms | 0.97 |
| Views (20) | 365 d | 24.47 s / 23.94 s | 22.06 s / 21.87 s | 0.91x | 11.49 s | 651 MB | 556 MB | consent-states 21850 ms | 0.87 |
| Product (10) | 7 d | 712 ms / 698 ms | 480 ms / 442 ms | 0.63x | 522 ms | 169 MB | 142 MB | total-events 426 ms | 0.95 |
| Product (10) | 30 d | 1.64 s / 1.51 s | 1.39 s / 1.37 s | 0.90x | 1.71 s | 236 MB | 194 MB | attribute-values 1366 ms | 0.94 |
| Product (10) | 90 d | 4.33 s / 4.38 s | 3.92 s / 3.81 s | 0.87x | 2.39 s | 315 MB | 262 MB | attribute-values 3808 ms | 0.98 |
| Product (10) | 365 d | 17.74 s / 16.88 s | 15.05 s / 14.75 s | 0.87x | 5.64 s | 693 MB | 629 MB | users-by-app-version 14750 ms | 0.99 |
| Users (6) | 30 d | 645 ms / 511 ms | 754 ms / 602 ms | 1.18x | 396 ms | 216 MB | 200 MB | active-users 602 ms | 0.87 |
| Groups (6) | 30 d | 349 ms / 246 ms | 442 ms / 334 ms | 1.36x | 243 ms | 143 MB | 129 MB | actions-per-group 319 ms | 0.87 |
| Retention (5) | 30 d | 2.06 s / 1.6 ms | 1.67 s / 1.6 ms | 1.04x | 1.21 s | 118 MB | 84 MB | install-milestones 1 ms | 0.88 |
| Web Vitals (11) | 30 d | 153 ms / 111 ms | 198 ms / 176 ms | 1.59x | 334 ms | 81 MB | 71 MB | lcp 167 ms | 0.88 |
| Measures (2) | 30 d | 29 ms / 29 ms | 41 ms / 35 ms | 1.20x | 71 ms | 83 MB | 66 MB | p75-by-day 35 ms | 0.97 |

Load average (1 min) at cell start: 0.85 to 1.00; cells that started above 1.0 (the gate gave up): 0 of 26.

### T = 32768

Started 2026-10-23T15:44:30Z. The same cells as step 3f (`native-bench dashboards`, `TestMeasureSystemDashboard`, one fresh process a cell, GOMAXPROCS=2, every widget at once, a first run then 3 more, "best" the fastest), native only, on step 3f's events rebuilt through the daily pass in Go: each month written as one log a day, then dumped, merged and derived a month at a time (`MEASURE_FROM`), so a month file has a row group per family per day (about 90) where step 3f's DuckDB-written ones had one (100k) or 9 of 122,880 rows (1M). "3f" columns are step 3f's cells; "change" is step 4's best over step 3f's native best. Before every cell the bench waits until the 1-minute load average is at most 1.0.

#### Step 4b T=32768 dashboards, volume 100k (100000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 297 ms / 278 ms | 82 ms / 81 ms | 0.29x | 376 ms | 87 MB | 59 MB | visitors-and-views 81 ms | 0.99 |
| Views (20) | 30 d | 382 ms / 377 ms | 242 ms / 234 ms | 0.62x | 446 ms | 93 MB | 69 MB | visitors 232 ms | 0.99 |
| Views (20) | 90 d | 1.09 s / 1.03 s | 628 ms / 595 ms | 0.58x | 603 ms | 139 MB | 79 MB | consent-states 583 ms | 0.98 |
| Views (20) | 365 d | 3.67 s / 3.71 s | 2.15 s / 2.12 s | 0.57x | 1.31 s | 160 MB | 118 MB | referrers 2123 ms | 0.97 |
| Product (10) | 7 d | 166 ms / 150 ms | 140 ms / 112 ms | 0.75x | 211 ms | 89 MB | 82 MB | events 112 ms | 0.99 |
| Product (10) | 30 d | 280 ms / 228 ms | 227 ms / 186 ms | 0.82x | 308 ms | 110 MB | 102 MB | events-by-name 185 ms | 0.99 |
| Product (10) | 90 d | 640 ms / 605 ms | 517 ms / 485 ms | 0.80x | 364 ms | 155 MB | 139 MB | events 481 ms | 0.97 |
| Product (10) | 365 d | 2.10 s / 2.08 s | 1.72 s / 1.68 s | 0.81x | 633 ms | 209 MB | 182 MB | attribute-values 1682 ms | 0.97 |
| Users (6) | 30 d | 80 ms / 65 ms | 90 ms / 70 ms | 1.07x | 165 ms | 83 MB | 78 MB | new-users 63 ms | 0.98 |
| Groups (6) | 30 d | 54 ms / 31 ms | 73 ms / 41 ms | 1.31x | 144 ms | 77 MB | 68 MB | daily-active-groups 40 ms | 0.98 |
| Retention (5) | 30 d | 178 ms / 1.9 ms | 188 ms / 1.4 ms | 0.73x | 280 ms | 91 MB | 64 MB | install-milestones 1 ms | 0.98 |
| Web Vitals (11) | 30 d | 49 ms / 44 ms | 30 ms / 23 ms | 0.51x | 189 ms | 65 MB | 49 MB | fcp 14 ms | 0.98 |
| Measures (2) | 30 d | 12 ms / 8.8 ms | 9.0 ms / 4.8 ms | 0.55x | 44 ms | 58 MB | 47 MB | p75-by-day 4 ms | 0.98 |

#### Step 4b T=32768 dashboards, volume 1m (1000000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 911 ms / 874 ms | 585 ms / 540 ms | 0.62x | 604 ms | 135 MB | 96 MB | consent-share 540 ms | 0.94 |
| Views (20) | 30 d | 2.20 s / 2.15 s | 1.97 s / 1.93 s | 0.90x | 1.09 s | 171 MB | 131 MB | visitors-and-views 1911 ms | 0.93 |
| Views (20) | 90 d | 6.70 s / 6.11 s | 5.70 s / 5.66 s | 0.93x | 2.78 s | 238 MB | 188 MB | display-resolutions 5656 ms | 1.00 |
| Views (20) | 365 d | 24.47 s / 23.94 s | 22.23 s / 21.89 s | 0.91x | 11.49 s | 651 MB | 522 MB | top-pages 21869 ms | 0.98 |
| Product (10) | 7 d | 712 ms / 698 ms | 495 ms / 449 ms | 0.64x | 522 ms | 169 MB | 148 MB | users-by-app-version 448 ms | 0.93 |
| Product (10) | 30 d | 1.64 s / 1.51 s | 1.44 s / 1.36 s | 0.90x | 1.71 s | 236 MB | 188 MB | avg-dau 1361 ms | 0.93 |
| Product (10) | 90 d | 4.33 s / 4.38 s | 3.96 s / 3.82 s | 0.87x | 2.39 s | 315 MB | 255 MB | daily-active-users 3801 ms | 0.92 |
| Product (10) | 365 d | 17.74 s / 16.88 s | 15.21 s / 14.77 s | 0.88x | 5.64 s | 693 MB | 670 MB | daily-active-users 14755 ms | 0.90 |
| Users (6) | 30 d | 645 ms / 511 ms | 684 ms / 568 ms | 1.11x | 396 ms | 216 MB | 198 MB | top-users 567 ms | 0.86 |
| Groups (6) | 30 d | 349 ms / 246 ms | 405 ms / 308 ms | 1.25x | 243 ms | 143 MB | 130 MB | daily-active-groups 306 ms | 0.94 |
| Retention (5) | 30 d | 2.06 s / 1.6 ms | 1.78 s / 1.6 ms | 1.01x | 1.21 s | 118 MB | 84 MB | install-milestones 1 ms | 0.94 |
| Web Vitals (11) | 30 d | 153 ms / 111 ms | 179 ms / 140 ms | 1.26x | 334 ms | 81 MB | 70 MB | lcp 124 ms | 1.00 |
| Measures (2) | 30 d | 29 ms / 29 ms | 39 ms / 29 ms | 0.99x | 71 ms | 83 MB | 61 MB | p75-by-day 29 ms | 1.00 |

Load average (1 min) at cell start: 0.86 to 1.00; cells that started above 1.0 (the gate gave up): 0 of 26.

### T = 65536

Started 2026-10-23T16:08:52Z. The same cells as step 3f (`native-bench dashboards`, `TestMeasureSystemDashboard`, one fresh process a cell, GOMAXPROCS=2, every widget at once, a first run then 3 more, "best" the fastest), native only, on step 3f's events rebuilt through the daily pass in Go: each month written as one log a day, then dumped, merged and derived a month at a time (`MEASURE_FROM`), so a month file has a row group per family per day (about 90) where step 3f's DuckDB-written ones had one (100k) or 9 of 122,880 rows (1M). "3f" columns are step 3f's cells; "change" is step 4's best over step 3f's native best. Before every cell the bench waits until the 1-minute load average is at most 1.0.

#### Step 4b T=65536 dashboards, volume 100k (100000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 297 ms / 278 ms | 158 ms / 140 ms | 0.50x | 376 ms | 87 MB | 64 MB | consent-share 139 ms | 1.00 |
| Views (20) | 30 d | 382 ms / 377 ms | 229 ms / 218 ms | 0.58x | 446 ms | 93 MB | 68 MB | display-resolutions 205 ms | 1.00 |
| Views (20) | 90 d | 1.09 s / 1.03 s | 621 ms / 617 ms | 0.60x | 603 ms | 139 MB | 86 MB | browsers 611 ms | 0.92 |
| Views (20) | 365 d | 3.67 s / 3.71 s | 2.09 s / 2.09 s | 0.56x | 1.31 s | 160 MB | 127 MB | operating-systems 2090 ms | 1.00 |
| Product (10) | 7 d | 166 ms / 150 ms | 133 ms / 121 ms | 0.81x | 211 ms | 89 MB | 77 MB | events 114 ms | 1.00 |
| Product (10) | 30 d | 280 ms / 228 ms | 202 ms / 164 ms | 0.72x | 308 ms | 110 MB | 99 MB | attribute-values 164 ms | 1.00 |
| Product (10) | 90 d | 640 ms / 605 ms | 466 ms / 454 ms | 0.75x | 364 ms | 155 MB | 138 MB | avg-dau 446 ms | 1.00 |
| Product (10) | 365 d | 2.10 s / 2.08 s | 1.62 s / 1.59 s | 0.77x | 633 ms | 209 MB | 186 MB | events 1589 ms | 0.95 |
| Users (6) | 30 d | 80 ms / 65 ms | 91 ms / 59 ms | 0.90x | 165 ms | 83 MB | 82 MB | actions-per-user 49 ms | 0.92 |
| Groups (6) | 30 d | 54 ms / 31 ms | 62 ms / 38 ms | 1.21x | 144 ms | 77 MB | 73 MB | daily-active-groups 37 ms | 0.92 |
| Retention (5) | 30 d | 178 ms / 1.9 ms | 178 ms / 2.0 ms | 1.08x | 280 ms | 91 MB | 66 MB | cohorts 0 ms | 0.92 |
| Web Vitals (11) | 30 d | 49 ms / 44 ms | 28 ms / 20 ms | 0.45x | 189 ms | 65 MB | 50 MB | cls-by-day 19 ms | 0.92 |
| Measures (2) | 30 d | 12 ms / 8.8 ms | 8.8 ms / 4.1 ms | 0.47x | 44 ms | 58 MB | 48 MB | p75-by-day 4 ms | 0.92 |

#### Step 4b T=65536 dashboards, volume 1m (1000000 events a month)

| Dashboard | Range | 3f native first / best | Step 4 native first / best | Change (best) | 3f DuckDB best | 3f native peak | Step 4 native peak | Slowest widget (best run) | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | 7 d | 911 ms / 874 ms | 674 ms / 639 ms | 0.73x | 604 ms | 135 MB | 94 MB | visitors 638 ms | 0.97 |
| Views (20) | 30 d | 2.20 s / 2.15 s | 2.03 s / 1.97 s | 0.92x | 1.09 s | 171 MB | 129 MB | visitors 1956 ms | 0.89 |
| Views (20) | 90 d | 6.70 s / 6.11 s | 5.67 s / 5.68 s | 0.93x | 2.78 s | 238 MB | 213 MB | visitors-and-views 5656 ms | 0.95 |
| Views (20) | 365 d | 24.47 s / 23.94 s | 22.72 s / 22.70 s | 0.95x | 11.49 s | 651 MB | 517 MB | devices 22700 ms | 0.86 |
| Product (10) | 7 d | 712 ms / 698 ms | 522 ms / 487 ms | 0.70x | 522 ms | 169 MB | 152 MB | avg-dau 471 ms | 0.89 |
| Product (10) | 30 d | 1.64 s / 1.51 s | 1.52 s / 1.42 s | 0.94x | 1.71 s | 236 MB | 205 MB | events 1417 ms | 0.85 |
| Product (10) | 90 d | 4.33 s / 4.38 s | 3.95 s / 3.96 s | 0.90x | 2.39 s | 315 MB | 271 MB | daily-active-users 3942 ms | 1.00 |
| Product (10) | 365 d | 17.74 s / 16.88 s | 15.81 s / 15.73 s | 0.93x | 5.64 s | 693 MB | 575 MB | peak-dau 15730 ms | 0.94 |
| Users (6) | 30 d | 645 ms / 511 ms | 690 ms / 540 ms | 1.06x | 396 ms | 216 MB | 200 MB | actions-per-user 527 ms | 0.94 |
| Groups (6) | 30 d | 349 ms / 246 ms | 402 ms / 289 ms | 1.18x | 243 ms | 143 MB | 128 MB | daily-active-groups 265 ms | 0.94 |
| Retention (5) | 30 d | 2.06 s / 1.6 ms | 1.92 s / 1.5 ms | 0.94x | 1.21 s | 118 MB | 91 MB | signed-in-milestones 1 ms | 0.94 |
| Web Vitals (11) | 30 d | 153 ms / 111 ms | 131 ms / 123 ms | 1.11x | 334 ms | 81 MB | 68 MB | by-page 122 ms | 0.94 |
| Measures (2) | 30 d | 29 ms / 29 ms | 33 ms / 22 ms | 0.76x | 71 ms | 83 MB | 59 MB | metrics 22 ms | 0.95 |

Load average (1 min) at cell start: 0.85 to 1.00; cells that started above 1.0 (the gate gave up): 0 of 26.

## Step 4c: a profile of a dashboard load

Run 2026-10-23 from 17:28Z, native only, on step 4b's 1M files with T = 32,768 (`~/.cache/twbench-native/4/dash-T32768/1m`): Views (20 query widgets) and Product (10) over 30 days, one fresh process a cell (`TestMeasureSystemDashboard`, GOMAXPROCS=2, `native.New`'s defaults: MaxScans 2, 2 workers), every widget at once, a first load then 3 warm loads. Each cell runs with and without **an open log**: 2026-09-30's 32,304 events written as `archivetest.WriteLogs` writes a day's log, re-dated to 2026-10-01 and added as `2026-10-01.log` to a copy of `raw/` whose other files are links; that cell's range is 2026-09-02 to 2026-10-01 (29 days of Parquet and the log) and the engine's clock says it is 2026-10-01, so the log is today's. The cell without it is 2026-09-01 to 2026-09-30 as before. Before every cell the gate waited until the 1-minute load was at most 1.0 (polling every 10 s, up to 5 min); one profile cell started at 1.31 and was re-run, the rest started at 0.69 to 0.98.

Hooks (`internal/app/measure_system_test.go`, build tag `measure`): `MEASURE_PROFILE` writes a CPU profile and a block profile (rate 1) of the 3 warm loads and a heap profile after them; `MEASURE_OPEN_LOG` with `MEASURE_TODAY` adds the log; `MEASURE_MARK` stats a missing `/measure-mark-<n>` before each load so an `strace -f -y` of the process tells the loads apart; `MEASURE_CHUNKS` lists every Parquet column chunk's first byte, to name the trace's reads; `MEASURE_NO_POLL` skips the harness's look for engine children. Each cell's CPU (user + system, `getrusage`) is now recorded per warm load. Profiles, traces and cells: `~/.cache/twbench-native/profiles/`.

### Wall and CPU

Best of 3 warm loads; CPU is the process's over that load. "Poll" is the harness as every earlier step ran it; "no poll" skips its /proc scan.

| Dashboard | Open log | Wall, poll | CPU, poll | Wall, no poll | CPU, no poll | Peak | Load at start (poll / no poll) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | no | 1.91 s | 3.79 s | 1.61 s | 3.18 s | 117 to 124 MB | 0.72 / 0.88 |
| Views (20) | yes | 9.12 s | 18.07 s | 7.76 s | 15.25 s | 119 to 123 MB | 0.82 / 0.89 |
| Product (10) | no | 1.43 s | 2.80 s | 1.22 s | 2.29 s | 187 to 200 MB | 0.98 / 0.93 |
| Product (10) | yes | 4.96 s | 9.82 s | 4.17 s | 8.19 s | 166 to 179 MB | 0.98 / 0.93 |

- **CPU is saturated:** CPU is 1.97 to 2.0 times wall in every cell, so the two Ps are idle under 2% of a load.
- **The harness's child poll costs 14 to 16% of a native cell's wall.** `childPeaks` lists /proc and reads every process's stat every 20 ms; on a busy host that is 13 to 15% of the CPU samples, taken from the cell's own two Ps. Every native cell since step 3f carries it (Views 30 d at 1M: 1.93 s in step 4b, 1.61 s without it). A DuckDB cell polls too, but its queries run in the engine child, outside the Go process's GOMAXPROCS, so the native/DuckDB ratios of steps 3f to 4b overstate the gap (Views 30 d at 1M: about 1.5x DuckDB, not 1.8x).
- **The open log makes the load 4.8x (Views) and 3.4x (Product) slower.** No earlier benchmark had one; a range ending today always does.

### Where the CPU goes

CPU per warm load (the profile's samples over 3 loads, divided by 3), without the harness's poll; each sample goes to the first category any frame of its stack matches, in the table's order (`tools/split.py` beside the profiles).

| Category | Views, no log | Views, open log | Product, no log | Product, open log |
| --- | --- | --- | --- | --- |
| Footer decode (`openFile`) | 93 ms (2.9%) | 87 ms (0.6%) | 60 ms (2.5%) | 60 ms (0.7%) |
| JSON log decode (`readLog`) | — | 11,523 ms (75.0%) | — | 5,717 ms (68.5%) |
| `readChunk` and dictionaries | 1,137 ms (35.6%) | 1,167 ms (7.6%) | 933 ms (39.1%) | 877 ms (10.5%) |
| of which the dictionary map (`dictBuilder`) | 697 ms (21.8%) | 667 ms (4.3%) | 637 ms (26.7%) | 607 ms (7.3%) |
| of which zstd | 160 ms (5.0%) | 197 ms (1.3%) | 120 ms (5.0%) | 93 ms (1.1%) |
| `selectRows`, `groupsOf`, `addDistinct` | 1,360 ms (42.5%) | 1,457 ms (9.5%) | 837 ms (35.1%) | 867 ms (10.4%) |
| of which the per-day key sort (`sortedUniqueKeys`) | 1,047 ms (32.7%) | 1,150 ms (7.5%) | 233 ms (9.8%) | 303 ms (3.6%) |
| of which the id hashing (`addDistinct`, `dset`) | 203 ms (6.4%) | 200 ms (1.3%) | 433 ms (18.2%) | 403 ms (4.8%) |
| of which `groupsOf` and `selectRows` | 110 ms (3.4%) | 107 ms (0.7%) | 170 ms (7.1%) | 160 ms (1.9%) |
| Computed columns (`batchColumn`) | 100 ms (3.1%) | 97 ms (0.6%) | 0 | 0 |
| Merge (`agg.merge`, its key sort) | 310 ms (9.7%) | 260 ms (1.7%) | 137 ms (5.7%) | 90 ms (1.1%) |
| Attribute unpivot | — | — | 210 ms (8.8%) | 197 ms (2.4%) |
| Gap-day sessionize (beyond its reads) | — | 50 ms (0.3%) | — | — |
| GC (background) | 110 ms (3.4%) | 507 ms (3.3%) | 113 ms (4.7%) | 407 ms (4.9%) |
| Other | 87 ms (2.7%) | 213 ms (1.4%) | 97 ms (4.1%) | 130 ms (1.6%) |
| **Engine CPU per load** | **3,197 ms** | **15,360 ms** | **2,387 ms** | **8,343 ms** |

- **Waiting on `turn`** (the block profile's time in `turn`'s semaphore select, summed over the widgets, per load): Views 17.6 s of the 21.8 s its widgets' latencies add up to, 82 s of 101 s with the log; Product 6.4 s of 10.5 s, 21.7 s of 30.7 s. It is queueing, not lost work: the Ps are busy throughout (above), so the wall is the CPU over two.
- **The JSON decode** is `encoding/json` into `map[string]json.RawMessage` per line (83% of it), then the per-key builders: about 575 ms of CPU per decode of the 18 MB log (32 MB/s), the same in both dashboards. With Views' sessions widgets, 16% of it is the gap day's `deriveSessions` decoding the log again.
- **The dictionary map** is `dictBuilder.addDict` putting every entry of a chunk's first dictionary into a `map[string]int32` (and a Go string each) to remap it, though a first dictionary's indexes stand as they are; `dictBuilder.id` is also the largest allocator (2.1 GB of the 6.2 GB allocated by a Views cell).
- **The per-day key sort** is `slices.SortFunc(cmpDayKey)` over each batch's (cell, hash) keys for a per-day distinct (visitors per day, `daily_*`): Views' largest single cost without the log, done per query.

### Reads per warm load

From `strace -f -y` of a cell with one warm load (between the marks): footer decodes are `openat`s of a Parquet file (each `openFile` decodes its footer), chunk reads are `pread64`s at a column chunk's first byte, log decodes are `openat`s of the log.

| Dashboard | Open log | Footer decodes | Column chunk reads (distinct chunks) | Most read | Log decodes |
| --- | --- | --- | --- | --- | --- |
| Views | no | 65: 2026-09 51, 2026-08 6, sessions/2026-09 8 | 664 (266), 2.5 each | `day` 230 (28), `actor_id` 191 (21) | 0 |
| Views | yes | 50: 2026-09 38, 2026-08 6, sessions/2026-09 6 | 704 (267), 2.6 each | `day` 242 (28), `actor_id` 198 (21) | 20 |
| Product | no | 32: 2026-09 30, 2026-08 2 | 314 (142), 2.2 each | `day` 106 (19), `actor_id` 63 (10) | 0 |
| Product | yes | 25: 2026-09 23, 2026-08 2 | 336 (150), 2.2 each | `day` 108 (21), `actor_id` 64 (10) | 11 |

A load reads 18 to 35 MB of Parquet. Each widget's scan lists the file's groups (one footer decode) and each worker opens it again (one more each); the dimension columns are read about once a chunk (one widget each), `day` (every group not taken whole) and `actor_id` (every visitor count) 8 to 9 times, and the log once per events scan and once more per gap-day derivation.

### What a shared scan must share

One scan per (project, table class) answers the load's widgets only if it shares the open log's decode first: 69 to 75% of a load's CPU with a log in range, read 20 (Views) or 11 (Product) times, where one decode per load (shared by the events consumers and the gap-day derivation) would take the Views load from about 15.4 s to about 4.4 s of CPU. Without a log, sharing the chunk reads (2.5x fewer, with their dictionary maps) and the footers saves about 0.77 s of Views' 3.2 s and 0.57 s of Product's 2.4 s. What stays per query is the aggregation: the per-day key sort (1.05 s of Views' 3.2 s), the id hashing, `groupsOf` and the merge, 1.7 s of Views' load, which neither the shared scan nor the per-batch memo touches. The per-batch memo targets the computed columns (3% of Views' load, none of Product's) and the per-entry hashes inside the id hashing (6% of Views', 18% of Product's, set inserts included), and pays only once a scan has several consumers. A cheaper per-day distinct (no comparison sort per batch), a first dictionary taken without the map, and a log decoded once per growth rather than per load are the costs beside it.

## Step 4d: the profile's cheap fixes (Task 2)

The cells of step 4c (`~/.cache/twbench-native/4/dash-T32768/1m`, Views (20 query widgets) and Product (10) over 30 days, with and without the open log of 2026-10-01, one fresh process a cell, GOMAXPROCS=2, `native.New`'s defaults, a first load then 3 warm loads, "best" the fastest), behind the same gate (1-minute load at most 1.0, polled every 10 s, up to 5 min), after each part. Cells and the script: `~/.cache/twbench-native/task2/`.

### 2a: the harness polls only DuckDB cells

`TestMeasureSystemDashboard` now polls /proc for engine children (every 20 ms) only in a DuckDB cell, whose queries run in those children, off the Go process's GOMAXPROCS. A native cell looks once, at the end, to check it spawned none. Every native number of steps 3f to 4c carried the poll; these are the fair "before" numbers.

| Dashboard | Open log | Step 4b best (poll) | 2a first / best | 2a CPU (best warm) | Change (best) | 3f DuckDB best | Peak | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | no | 1.93 s | 1.62 s / 1.61 s | 3.19 s | 0.84x | 1.09 s | 128 MB | 0.74 |
| Views (20) | yes | 9.12 s (step 4c) | 7.72 s / 7.68 s | 15.27 s | 0.84x | — | 115 MB | 0.84 |
| Product (10) | no | 1.36 s | 1.22 s / 1.19 s | 2.35 s | 0.87x | 1.71 s | 186 MB | 0.98 |
| Product (10) | yes | 4.96 s (step 4c) | 4.68 s / 4.20 s | 8.27 s | 0.85x | — | 177 MB | 0.97 |

Without the poll, native Views at 30 days and 1M is 1.48x DuckDB's step 3f best (not 1.8x), Product 0.70x. The numbers match step 4c's "no poll" column (1.61 s, 7.76 s, 1.22 s, 4.17 s).

### 2b: an open log decoded once while it grows

The engine keeps each open log's decoded rows (`internal/native/logcache.go`): raw rows before rules, of every family and day, with the union of the columns queries asked for, keyed by path and checked against the file's device, inode and size. A grown log decodes only its new complete lines (the seen-id set kept, so the first line per id spans appends); the torn tail past the last newline is decoded per query and never kept; a query asking for a column the entry lacks decodes the whole log again with the union (picked over keeping line offsets, since `encoding/json` into a map is 83% of a decode whichever key is wanted). Concurrent queries share one decode per log; entries are LRU within `MaxBytes`, and a log whose entry alone is larger is read per query by `readLog` as before. Every reader of a log goes through it: the events scans, the gap-day sessions derivation, the identities' live part, retention, presence, attributes and attribution (whose per-call cache it replaces).

| Dashboard | Open log | 2a first / best | 2b first / best | 2b CPU (best warm) | Change (best) | 2a peak | 2b peak | Log decodes, first load | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | no | 1.62 s / 1.61 s | 1.64 s / 1.62 s | 3.20 s | 1.01x | 128 MB | 121 MB | — | 0.78 |
| Views (20) | yes | 7.72 s / 7.68 s | 7.22 s / 1.73 s | 3.44 s | 0.23x | 115 MB | 169 MB | 11 (was 20 a load) | 0.87 |
| Product (10) | no | 1.22 s / 1.19 s | 1.30 s / 1.16 s | 2.32 s | 0.98x | 186 MB | 190 MB | — | 0.89 |
| Product (10) | yes | 4.68 s / 4.20 s | 3.14 s / 1.20 s | 2.38 s | 0.29x | 177 MB | 219 MB | 4 (was 11 a load) | 0.98 |

- **With the log in range, a warm load now costs about what one without it does:** Views 1.73 s against 1.62 s, Product 1.20 s against 1.16 s. Warm loads decode nothing (the log does not grow between them in this bench); the first load still decodes it once per new column set (11 times for Views, 4 for Product), each waiting on the one before.
- **The cached entry of the 33k-line log** (32,304 lines, 18 MB; `TestMeasureLogCache`, tag `measure`): 11.3 MB counted and 11.4 MB on the heap with the 23 columns a Views load asks for, 7.0 MB counted (6.3 MB heap) with Product's 7 (`attr_*` among them), 15.8 MB (16.2 MB heap) with every column. Decoding it cold takes 531 ms (`readLog`: 542 ms), a 10% growth (3,230 lines) 59 ms, and a warm query's rows 0.5 to 1.7 ms.
- **The peak rose** with the log in range: 169 MB against 115 (Views), 219 against 177 (Product). The entry stays live (11 MB) and a re-decode for a new column holds the old entry and the new one at once on the first load, which the GC's headroom doubles.

### 2b, fix round 1: every column on the first miss

The first miss now decodes every column the log has: one decode, never again for a column (a key first seen in appended lines becomes a column, null for the rows before). The byte bound is unchanged.

| Dashboard | Open log | 2a first / best | 2b (on demand) first / best | 2b (every column) first / best | CPU (best warm) | Peak 2a / on demand / every column | Load at start |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Views (20) | no | 1.62 s / 1.61 s | 1.64 s / 1.62 s | 1.67 s / 1.63 s | 3.22 s | 128 / 121 / 123 MB | 0.96 |
| Views (20) | yes | 7.72 s / 7.68 s | 7.22 s / 1.73 s | 2.29 s / 1.73 s | 3.42 s | 115 / 169 / 177 MB | 0.89 |
| Product (10) | no | 1.22 s / 1.19 s | 1.30 s / 1.16 s | 1.19 s / 1.17 s | 2.33 s | 186 / 190 / 195 MB | 0.98 |
| Product (10) | yes | 4.68 s / 4.20 s | 3.14 s / 1.20 s | 1.91 s / 1.20 s | 2.35 s | 177 / 219 / 238 MB | 0.90 |

- **The first load with the log** drops from 7.22 to 2.29 s (Views) and 3.14 to 1.91 s (Product): one decode instead of 11 and 4. The warm loads are unchanged.
- **The peak did not come back down:** with the log in range it is 62 MB (Views) and 61 MB (Product) above 2a's. The entry is 15.8 MB live, every query's rows of it are copied out (a family's rows, `Column.take`), and the GC's headroom doubles both. Without the log the peaks are within the run-to-run spread.
