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
