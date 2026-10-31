package native

import (
	"cmp"
	"math"
	"slices"
)

// merged is the workers' partials put together.
type merged struct {
	a       *agg
	groups  int
	count   [][]int64 // per mask
	bounces [][]int64 // per mask
	nums    []numPart
	msrs    []msrPart
	latest  []latestPart
	hists   [][]histEntry // per measure column with hist: sorted by cell, then bucket
	sets    [][]dset
	days    [][]daySet // per distinct column with day keys, per day: all of them
	runs    [][]run    // and their counts per (group, day)
	// parts are the groups of each partition a share is taken over, by
	// partition (nil: one, every group); partOf is each group's.
	parts       [][]int32
	partOf      []int32
	shareTotals map[shareKey]float64
}

// shareKey is a cached share total: of a mask's rows (count) or a
// measure column's weights, over a partition (-1: every group), on a day
// (-1: all).
type shareKey struct {
	weight    bool
	idx, part int
	day       int
}

// partitionBy splits the groups into partitions by their first
// dimension's value, which a breakdown's share is taken over.
func (m *merged) partitionBy() {
	ids := map[dimVal]int32{}
	m.partOf = make([]int32, m.groups)
	for g := range m.groups {
		v := m.a.groups.vals[g][0]
		id, ok := ids[v]
		if !ok {
			id = int32(len(m.parts))
			ids[v] = id
			m.parts = append(m.parts, nil)
		}
		m.parts[id] = append(m.parts[id], int32(g))
		m.partOf[g] = id
	}
}

// merge puts the workers' partials together, releasing them.
func (a *agg) merge() *merged {
	m := &merged{a: a, groups: 1}
	if a.groups != nil {
		m.groups = a.groups.len()
	}
	cells := m.groups * a.nd
	m.count = make([][]int64, len(a.masks))
	if a.bounces {
		m.bounces = make([][]int64, len(a.masks))
	}
	for k := range a.masks {
		m.count[k] = make([]int64, cells)
		if a.bounces {
			m.bounces[k] = make([]int64, cells)
		}
	}
	m.latest = make([]latestPart, len(a.latests))
	for j := range m.latest {
		m.latest[j].grow(cells)
		m.latest[j].num = a.latests[j].numeric()
	}
	m.nums = make([]numPart, len(a.nums))
	for j, n := range a.nums {
		np := &m.nums[j]
		np.n = make([]int64, cells)
		if n.float {
			np.fsum, np.fmin, np.fmax = make([]float64, cells), make([]float64, cells), make([]float64, cells)
		} else {
			np.isum, np.imin, np.imax = make([]int64, cells), make([]int64, cells), make([]int64, cells)
		}
	}
	m.msrs = make([]msrPart, len(a.msrs))
	m.hists = make([][]histEntry, len(a.msrs))
	hists := make([]map[histKey]float64, len(a.msrs))
	for j, c := range a.msrs {
		mp := &m.msrs[j]
		mp.wn, mp.sn, mp.w, mp.s = make([]int64, cells), make([]int64, cells), make([]float64, cells), make([]float64, cells)
		if c.hist {
			hists[j] = map[histKey]float64{}
		}
	}
	m.sets = make([][]dset, len(a.distinct))
	for j, d := range a.distinct {
		if d.set {
			m.sets[j] = make([]dset, m.groups)
		}
	}
	for w := range a.parts {
		p := &a.parts[w]
		// A worker's arrays may be longer than the groups (they grow
		// ahead); the cells past them hold nothing.
		for k, count := range p.count {
			for x, c := range count[:min(len(count), cells)] {
				m.count[k][x] += c
			}
		}
		for k, bounces := range p.bounces {
			for x, c := range bounces[:min(len(bounces), cells)] {
				m.bounces[k][x] += c
			}
		}
		for j := range p.nums {
			mergeNum(&m.nums[j], &p.nums[j], a.nums[j].float, cells)
		}
		for j := range p.latest {
			mergeLatest(&m.latest[j], &p.latest[j], cells)
		}
		for j := range p.msrs {
			mergeMsr(&m.msrs[j], &p.msrs[j], hists[j], cells)
		}
		for j := range p.sets {
			for g := range p.sets[j][:min(len(p.sets[j]), m.groups)] {
				m.sets[j][g].merge(&p.sets[j][g])
			}
			p.sets[j] = nil
		}
		p.count, p.bounces, p.nums, p.msrs, p.latest = nil, nil, nil, nil, nil
	}
	for j, h := range hists {
		if h == nil {
			continue
		}
		es := make([]histEntry, 0, len(h))
		for k, w := range h {
			es = append(es, histEntry{histKey: k, w: w})
		}
		slices.SortFunc(es, func(a, b histEntry) int { return cmpHistKey(a.histKey, b.histKey) })
		m.hists[j] = es
		hists[j] = nil
	}
	m.days = make([][]daySet, len(a.distinct))
	m.runs = make([][]run, len(a.distinct))
	for j, d := range a.distinct {
		if !d.day {
			continue
		}
		m.days[j] = make([]daySet, a.ndays)
		sets := make([]*daySet, 0, len(a.parts))
		for day := range a.ndays {
			sets = sets[:0]
			for w := range a.parts {
				if a.parts[w].days != nil {
					sets = append(sets, &a.parts[w].days[j][day])
				}
			}
			m.days[j][day] = mergeSets(sets)
		}
		for w := range a.parts {
			if a.parts[w].days != nil {
				a.parts[w].days[j] = nil
			}
		}
		m.runs[j] = runsOf(m.days[j])
	}
	return m
}

// mergeMsr adds src's measure partials to dst's, its histogram to hist.
func mergeMsr(dst, src *msrPart, hist map[histKey]float64, cells int) {
	for x, n := range src.wn[:min(len(src.wn), cells)] {
		if n == 0 {
			continue
		}
		dst.wn[x] += n
		dst.w[x] += src.w[x]
		dst.sn[x] += src.sn[x]
		dst.s[x] += src.s[x]
	}
	for k, w := range src.hist {
		if k.cell < cells {
			hist[k] += w
		}
	}
}

func cmpHistKey(a, b histKey) int {
	if a.cell != b.cell {
		return cmp.Compare(a.cell, b.cell)
	}
	return cmp.Compare(a.bucket, b.bucket)
}

func mergeNum(dst, src *numPart, float bool, cells int) {
	for x, n := range src.n[:min(len(src.n), cells)] {
		if n == 0 {
			continue
		}
		first := dst.n[x] == 0
		dst.n[x] += n
		if float {
			foldBounds(&dst.fsum[x], &dst.fmin[x], &dst.fmax[x], src.fsum[x], src.fmin[x], src.fmax[x], first)
		} else {
			foldBounds(&dst.isum[x], &dst.imin[x], &dst.imax[x], src.isum[x], src.imin[x], src.imax[x], first)
		}
	}
}

// foldBounds adds sum to *dsum and widens [*dlo, *dhi] to take in [lo,
// hi]: a cell's partial sum and bounds folded into another's, which holds
// none yet when first.
func foldBounds[T int64 | float64](dsum, dlo, dhi *T, sum, lo, hi T, first bool) {
	*dsum += sum
	if first || lo < *dlo {
		*dlo = lo
	}
	if first || hi > *dhi {
		*dhi = hi
	}
}

// cellsOf calls f with each cell of group g: all its days when day < 0.
func (m *merged) cellsOf(g int32, day int, f func(x int)) {
	if int(g) >= m.groups {
		return
	}
	nd := m.a.nd
	if day >= 0 {
		f(int(g)*nd + day)
		return
	}
	for x := int(g) * nd; x < int(g+1)*nd; x++ {
		f(x)
	}
}

// rows is how many rows group g holds (on day, unless it is < 0).
func (m *merged) rows(g int32, day int) int64 { return m.maskRows(0, g, day) }

// metricRows is how many rows of group g on day pass mt's where: a
// ratio's, those of either side.
func (m *merged) metricRows(mt metricSpec, g int32, day int) int64 {
	if mt.Op == "ratio" {
		return m.metricRows(*mt.ratioNum, g, day) + m.metricRows(*mt.ratioDen, g, day)
	}
	return m.maskRows(mt.mask, g, day)
}

// maskRows is how many rows of mask group g holds (on day, unless it is
// < 0).
func (m *merged) maskRows(mask int, g int32, day int) int64 {
	var n int64
	m.cellsOf(g, day, func(x int) { n += m.count[mask][x] })
	return n
}

// compute is metric mt over the rows of the groups gs: on one day of the
// range (a trend's cell; gs one group then) or, when day < 0, over all;
// rounded as mt says.
func (m *merged) compute(mt metricSpec, gs []int32, day int) value {
	return m.computeRaw(mt, gs, day, false).round(mt.Round)
}

// computeOther is compute for a breakdown's Other row: its share is over
// every group.
func (m *merged) computeOther(mt metricSpec, gs []int32) value {
	return m.computeRaw(mt, gs, -1, true).round(mt.Round)
}

func (m *merged) computeRaw(mt metricSpec, gs []int32, day int, whole bool) value {
	switch mt.Op {
	case "ratio":
		// num / den as a DOUBLE, both over the same groups and day (an
		// Other row's both folded first): NULL when den is 0 or NULL, or
		// num NULL; a counted num over no rows is 0 when den is above 0,
		// as the widgets' COALESCE(num, 0) reads it.
		num := m.computeRaw(*mt.ratioNum, gs, day, whole)
		den := m.computeRaw(*mt.ratioDen, gs, day, whole)
		if num.null && countedOps[mt.ratioNum.Op] && !den.null && den.num() > 0 {
			num = intValue(0)
		}
		if num.null || den.null || den.num() == 0 {
			return value{null: true}
		}
		return value{f: num.num() / den.num(), approx: num.approx || den.approx}
	case "share":
		part := -1
		if m.partOf != nil && !whole && len(gs) > 0 {
			part = int(m.partOf[gs[0]])
		}
		total := m.shareTotal(mt, part, day)
		if total == 0 {
			return value{null: true}
		}
		return value{f: m.shareOf(mt, gs, day) / total}
	case "count":
		var n int64
		for _, g := range gs {
			n += m.maskRows(mt.mask, g, day)
		}
		return intValue(n)
	case "sum", "avg", "min", "max":
		return m.evalNum(mt, gs, day)
	case "latest":
		return m.latestOf(mt, gs, day)
	case "percentile":
		return m.percentile(mt, gs, day)
	case "mean", "weight":
		// SUM(value / sample_rate) / SUM(1 / sample_rate), and the latter
		// alone: NULL over no row (no value, for the mean).
		mp := &m.msrs[mt.msr]
		var wn, sn int64
		var w, s float64
		for _, g := range gs {
			m.cellsOf(g, day, func(x int) {
				wn += mp.wn[x]
				w += mp.w[x]
				sn += mp.sn[x]
				s += mp.s[x]
			})
		}
		switch {
		case wn == 0 || (mt.Op == "mean" && sn == 0):
			return value{null: true}
		case mt.Op == "weight":
			return value{f: w}
		}
		return value{f: s / w}
	case "bounces", "bounce_rate":
		// Sessions of one view, and their share of the sessions: DuckDB's
		// count(*) FILTER (WHERE views = 1)::DOUBLE / count(*), empty over
		// none.
		var b, n int64
		for _, g := range gs {
			n += m.maskRows(mt.mask, g, day)
			m.cellsOf(g, day, func(x int) { b += m.bounces[mt.mask][x] })
		}
		switch {
		case mt.Op == "bounces":
			return intValue(b)
		case n == 0:
			return value{null: true}
		}
		return value{f: float64(b) / float64(n)}
	case "actors", "users", "groups", opValues:
		if day >= 0 {
			return intValue(m.dayCount(mt.distinct, gs[0], day))
		}
		if len(gs) == 1 {
			if int(gs[0]) >= m.groups {
				return intValue(0)
			}
			n, approx := m.sets[mt.distinct][gs[0]].count()
			return value{isInt: true, i: n, approx: approx}
		}
		var u dset
		for _, g := range gs {
			u.merge(&m.sets[mt.distinct][g])
		}
		n, approx := u.count()
		return value{isInt: true, i: n, approx: approx}
	}
	// daily_* and days: the per-day distinct counts of the days with rows.
	var days []int64
	switch {
	case day >= 0:
		days = []int64{m.dayCount(mt.distinct, gs[0], day)}
	case len(gs) == 1:
		days = m.groupDays(mt.distinct, gs[0])
	default:
		days = m.unionDays(mt.distinct, gs)
	}
	if mt.Op == "days" {
		if day >= 0 {
			return intValue(int64(boolInt(m.hasDay(mt.distinct, gs[0], day))))
		}
		return intValue(int64(len(days)))
	}
	if len(days) == 0 {
		return value{null: true}
	}
	var sum, top int64
	for i, n := range days {
		sum += n
		if i == 0 || n > top {
			top = n
		}
	}
	switch mt.Op {
	case "daily_sum":
		return intValue(sum)
	case "daily_max":
		return intValue(top)
	}
	return value{f: float64(sum) / float64(len(days))}
}

func (m *merged) evalNum(mt metricSpec, gs []int32, day int) value {
	np, float := &m.nums[mt.num], m.a.nums[mt.num].float
	var n, isum, imin, imax int64
	var fsum, fmin, fmax float64
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) {
			c := np.n[x]
			if c == 0 {
				return
			}
			first := n == 0
			n += c
			if float {
				foldBounds(&fsum, &fmin, &fmax, np.fsum[x], np.fmin[x], np.fmax[x], first)
			} else {
				foldBounds(&isum, &imin, &imax, np.isum[x], np.imin[x], np.imax[x], first)
			}
		})
	}
	if n == 0 {
		return value{null: true}
	}
	switch {
	case mt.Op == "avg" && float:
		return value{f: fsum / float64(n)}
	case mt.Op == "avg":
		return value{f: float64(isum) / float64(n)}
	case float && mt.Op == "sum":
		return value{f: fsum}
	case float && mt.Op == "min":
		return value{f: fmin}
	case float:
		return value{f: fmax}
	case mt.Op == "sum":
		return intValue(isum)
	case mt.Op == "min":
		return value{isInt: true, i: imin, form: m.a.nums[mt.num].form}
	}
	return value{isInt: true, i: imax, form: m.a.nums[mt.num].form}
}

// shareOf is what share mt is taken of over the groups gs (on day, unless
// it is < 0): the rows of its mask, or their weights.
func (m *merged) shareOf(mt metricSpec, gs []int32, day int) float64 {
	if mt.Of != "weight" {
		var n int64
		for _, g := range gs {
			n += m.maskRows(mt.mask, g, day)
		}
		return float64(n)
	}
	var w float64
	mp := &m.msrs[mt.msr]
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) { w += mp.w[x] })
	}
	return w
}

// shareTotal is shareOf over partition part's groups (every group when
// part < 0), cached.
func (m *merged) shareTotal(mt metricSpec, part, day int) float64 {
	k := shareKey{weight: mt.Of == "weight", idx: mt.mask, part: part, day: day}
	if k.weight {
		k.idx = mt.msr
	}
	if t, ok := m.shareTotals[k]; ok {
		return t
	}
	var gs []int32
	if part >= 0 {
		gs = m.parts[part]
	} else {
		gs = make([]int32, m.groups)
		for g := range gs {
			gs[g] = int32(g)
		}
	}
	t := m.shareOf(mt, gs, day)
	if m.shareTotals == nil {
		m.shareTotals = map[shareKey]float64{}
	}
	m.shareTotals[k] = t
	return t
}

// percentile is mt's quantile over the rows of the groups gs (on day,
// unless it is < 0), as the widgets' SQL reads it from the buckets: their
// weights summed per bucket, the running sum in ascending bucket order
// against the total, and the value of the first bucket whose running sum
// reaches q of the total; NULL over no row.
func (m *merged) percentile(mt metricSpec, gs []int32, day int) value {
	h := m.hists[mt.msr]
	var bs []histEntry
	cells := 0
	for _, g := range gs {
		m.cellsOf(g, day, func(x int) {
			i, _ := slices.BinarySearchFunc(h, x, func(e histEntry, c int) int { return cmp.Compare(e.cell, c) })
			if i < len(h) && h[i].cell == x {
				cells++
			}
			for ; i < len(h) && h[i].cell == x; i++ {
				bs = append(bs, h[i])
			}
		})
	}
	if len(bs) == 0 {
		return value{null: true}
	}
	if cells > 1 {
		// Several cells' histograms: one weight per bucket.
		slices.SortStableFunc(bs, func(a, b histEntry) int { return cmp.Compare(a.bucket, b.bucket) })
		n := 0
		for _, e := range bs {
			if n > 0 && bs[n-1].bucket == e.bucket {
				bs[n-1].w += e.w
				continue
			}
			bs[n] = e
			n++
		}
		bs = bs[:n]
	}
	var total float64
	for _, e := range bs {
		total += e.w
	}
	at := mt.Q * total
	var run float64
	for _, e := range bs {
		run += e.w
		if run >= at {
			return value{f: bucketValue(e.bucket)}
		}
	}
	return value{null: true}
}

// bucketValue is a bucket's representative value, as the widgets' SQL
// reads it: 0 for -1000, else 2 * 1.04^bucket / 2.04.
func bucketValue(b int64) float64 {
	if b == -1000 {
		return 0
	}
	return 2 * math.Pow(1.04, float64(b)) / 2.04
}

// dayCount is the distinct ids of group g on day.
func (m *merged) dayCount(j int, g int32, day int) int64 {
	cell := uint64(g)*uint64(m.a.ndays) + uint64(day)
	runs := m.runs[j]
	i, ok := slices.BinarySearchFunc(runs, cell, func(r run, c uint64) int { return cmp.Compare(r.cell, c) })
	if !ok {
		return 0
	}
	return runs[i].n
}

// hasDay says whether group g has rows on day, of the mask column j
// counts.
func (m *merged) hasDay(j int, g int32, day int) bool {
	cell := uint64(g)*uint64(m.a.ndays) + uint64(day)
	_, ok := slices.BinarySearchFunc(m.runs[j], cell, func(r run, c uint64) int { return cmp.Compare(r.cell, c) })
	return ok
}

// groupDays is group g's distinct ids per day with rows.
func (m *merged) groupDays(j int, g int32) []int64 {
	nd := uint64(m.a.ndays)
	lo, hi := uint64(g)*nd, uint64(g+1)*nd
	runs := m.runs[j]
	i, _ := slices.BinarySearchFunc(runs, lo, func(r run, c uint64) int { return cmp.Compare(r.cell, c) })
	var out []int64
	for ; i < len(runs) && runs[i].cell < hi; i++ {
		out = append(out, runs[i].n)
	}
	return out
}

// unionDays is the distinct ids per day with rows of the groups gs
// together.
func (m *merged) unionDays(j int, gs []int32) []int64 {
	in := make([]bool, m.groups)
	for _, g := range gs {
		if int(g) < m.groups {
			in[g] = true
		}
	}
	var out []int64
	for d := range m.days[j] {
		// The day's keys of the groups gs, without their group: a NULL
		// key marks rows, the others are the ids.
		var ids daySet
		rows := false
		m.days[j][d].each(func(g uint32, h uint64) {
			if !in[g] {
				return
			}
			rows = true
			if h != nullKey {
				ids.add(0, h)
			}
		})
		if rows {
			out = append(out, int64(ids.n))
		}
	}
	return out
}
