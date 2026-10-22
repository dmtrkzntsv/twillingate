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

Step 1b: load average (1 min) at the start of every run and rewrite: 0.81 to 1.00. Matrix wall: 24m4s.

Notes on step 1b (written by hand after the run):

- The gate held: every run and rewrite started at a 1-minute load of 0.81 to
  1.00, none gave up, so no run is flagged. The load hovered just under the
  threshold (other sessions share the host), so the gate waited before most
  runs; the whole matrix, both volumes, took 24 minutes.
- The comparison with step 1 is fair at 100k (step 1 ran at load 1.3 to 2.3)
  but flatters step 1b at 1M: step 1's 1M cells ran at load 2.4 to 8.7, and
  its pqgo-1d cells at 4.0 to 6.1. Read the 1M speedups as upper bounds. The
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
