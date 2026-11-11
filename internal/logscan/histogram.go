package logscan

import (
	"runtime"
	"sort"
	"sync"
	"time"
)

// Histogram 当前结果集的时间分布（JSON 给前端渲染时间直方图）。
// Buckets 每桶 7 位计数：[0..5]=TRACE..FATAL 六级、[6]=无级别（plain/未识别级别）行——
// 前端无级别段以中性灰渲染，全部文件无级别时整体退化为单色总量条（彩色只属于日志信号）
type Histogram struct {
	Start    int64      `json:"start"`    // 首桶起点 unixNano（整刻度对齐）
	Step     int64      `json:"step"`     // 桶宽 ns（整刻度：100ms ~ 1d 的可读刻度）
	Buckets  [][7]int32 `json:"buckets"`
	MaxTotal int32      `json:"maxTotal"` // 最大桶总量（前端纵向归一化用）
	NoTS     int64      `json:"noTs"`     // 无时间格式的行数（整文件无时间戳，无锚点可继承，不进桶）
	HasData  bool       `json:"hasData"`  // 是否存在时间分布（false=结果全无时间戳，前端隐藏直方图）
}

// niceStepNS 整刻度桶宽候选（单位 ns）：时间直方图的桶宽落在人类可读刻度上
// （Kibana 同款策略），轴标签与网格线才能对齐整点
var niceStepNS = []int64{
	100 * int64(time.Millisecond), 200 * int64(time.Millisecond), 500 * int64(time.Millisecond), // 亚秒档（microburst 场景）
	int64(time.Second), 2 * int64(time.Second), 5 * int64(time.Second),
	10 * int64(time.Second), 15 * int64(time.Second), 20 * int64(time.Second), 30 * int64(time.Second),
	int64(time.Minute), 2 * int64(time.Minute), 5 * int64(time.Minute),
	10 * int64(time.Minute), 15 * int64(time.Minute), 30 * int64(time.Minute),
	int64(time.Hour), 2 * int64(time.Hour), 3 * int64(time.Hour),
	6 * int64(time.Hour), 12 * int64(time.Hour),
	int64(24 * time.Hour),
}

// niceStep 为期望桶宽（ns）取第一个不小于它的整刻度；超过 1 天按整天数向上取整。
// 100ms 起步：日志时间戳精度普遍为 ms，更细无意义
func niceStep(rawNS int64) int64 {
	for _, u := range niceStepNS {
		if u >= rawNS {
			return u
		}
	}
	const day = int64(24 * time.Hour)
	d := (rawNS + day - 1) / day
	return d * day
}

// histFrame 由结果时间范围定桶骨架：整刻度桶宽 + 起点对齐桶宽倍数
// （对齐后首桶恰覆盖最小时间戳，无前置空桶；nice 取整只会让桶数略少于期望值）
func histFrame(minTS, maxTS int64, want int) (start, step int64, n int) {
	step = niceStep((maxTS - minTS) / int64(want))
	start = minTS / step * step
	if start > minTS { // 防御（unixNano 恒正，不会走到）
		start -= step
	}
	n = int((maxTS-start)/step) + 1
	return
}

// TimeHistogram 聚合当前结果集的时间分布。惰性计算（查询完成后由前端单独拉取）：
// 全览零物化 2 亿行的聚合是 O(总行数) 的数百毫秒级开销，并入查询主路径会把
// 0s 出结果的全览拖成秒级。三种结果形态统一取数：
//   - 零物化全览：从行索引按文件并行聚合（文件间无共享状态）
//   - 查询物化 / 全览物化：遍历结果数组，两遍 O(n)（先定桶骨架再分桶）
//
// 锚点继承与 filterCandidates/sortMerged 同一语义：无时间戳的行（堆栈/SQL 续行）
// 归属其主行所在桶——继承同文件最近前条非零时间戳，文件开头无前锚点的行继承
// 本文件首条时间戳，整文件无时间戳的行计入 NoTS 不进桶。
// 合并序结果同样用「序列向前继承」：sortMerged 的稳定排序保证续行（key=锚点）
// 紧贴主行不被拆散，序列中上一条非零时间戳即本行锚点
func (s *Session) TimeHistogram(nBuckets int) *Histogram {
	if nBuckets <= 0 {
		nBuckets = 60
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mode == modeBrowseVirtual {
		return s.histFromIndexLocked(s.browseFiles, nBuckets)
	}
	return s.histFromResultsLocked(s.results, nBuckets)
}

// histFromIndexLocked 零物化全览的直方图：从各文件行索引直接聚合（锁内调用）。
// 文件级并行——单文件单遍扫描是内存带宽型操作，2 亿行串行需秒级，
// 按文件切分后各 goroutine 聚合局部桶再合并（合并成本 O(桶数×文件数)，可忽略）
func (s *Session) histFromIndexLocked(files []*FileEntry, nBuckets int) *Histogram {
	// 桶骨架：O(文件数) 汇总时间范围；无时间文件行数直接计入 NoTS（无需扫描）
	var minTS, maxTS int64
	var noTS int64
	var timed []*FileEntry
	for _, e := range files {
		if e.TSMin <= 0 {
			noTS += int64(e.idx.LineCount())
			continue
		}
		timed = append(timed, e)
		if minTS == 0 || e.TSMin < minTS {
			minTS = e.TSMin
		}
		if e.TSMax > maxTS {
			maxTS = e.TSMax
		}
	}
	if len(timed) == 0 {
		return &Histogram{NoTS: noTS}
	}
	start, step, n := histFrame(minTS, maxTS, nBuckets)

	type part struct {
		buckets [][7]int32
	}
	parts := make([]part, len(timed))
	sem := make(chan struct{}, min(8, runtime.NumCPU()))
	var wg sync.WaitGroup
	for i, e := range timed {
		wg.Add(1)
		go func(i int, e *FileEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			parts[i].buckets = aggIndex(e, start, step, n)
		}(i, e)
	}
	wg.Wait()

	h := &Histogram{Start: start, Step: step, HasData: true, NoTS: noTS}
	h.Buckets = make([][7]int32, n)
	for _, p := range parts {
		for b, c := range p.buckets {
			for j := 0; j < 7; j++ {
				h.Buckets[b][j] += c[j]
			}
		}
	}
	h.finish()
	return h
}

// aggIndex 单文件行索引聚合为局部桶：单遍扫描，「进度/锚点」无需分离——
// 这里逐行无条件推进（与 sortMerged 的 key 扫描同构），无时间行继承
// lastTS，文件开头继承 firstTS（与 filterCandidates 同规则）
func aggIndex(e *FileEntry, start, step int64, n int) [][7]int32 {
	out := make([][7]int32, n)
	ts := e.idx.ts
	levels := e.idx.levels
	firstTS := int64(0)
	for _, t := range ts {
		if t > 0 {
			firstTS = t
			break
		}
	}
	lastTS := int64(0)
	add := func(t int64, lv int8) {
		b := int((t - start) / step)
		if b < 0 {
			b = 0
		} else if b >= n {
			b = n - 1
		}
		if lv >= 0 && int(lv) < 6 {
			out[b][lv]++
		} else {
			out[b][6]++
		}
	}
	for i, t := range ts {
		switch {
		case t > 0:
			lastTS = t
		case lastTS > 0:
			t = lastTS
		case firstTS > 0:
			t = firstTS
		default:
			continue // 整文件无时间戳（上层已按 TSMin 过滤，防御性兜底）
		}
		add(t, levels[i])
	}
	return out
}

// anchorState 结果数组遍历的锚点继承状态：序列中最近一条非零时间戳（须同文件）。
// firstTS 按文件懒算缓存（含 0 值——无时间文件只算一次）
type anchorState struct {
	lastTS  int64
	lastFid int64
	firstTS map[int64]int64
}

func (a *anchorState) effective(r *ResultRow, entryById map[int64]*FileEntry) int64 {
	if r.TS > 0 {
		a.lastTS, a.lastFid = r.TS, r.FileID
		return r.TS
	}
	if a.lastFid == r.FileID && a.lastTS > 0 {
		return a.lastTS
	}
	if a.firstTS == nil {
		a.firstTS = make(map[int64]int64)
	}
	t, ok := a.firstTS[r.FileID]
	if !ok {
		t = int64(0)
		if e := entryById[r.FileID]; e != nil {
			for _, x := range e.idx.ts {
				if x > 0 {
					t = x
					break
				}
			}
		}
		a.firstTS[r.FileID] = t
	}
	if t > 0 {
		a.lastTS, a.lastFid = t, r.FileID
	}
	return t
}

// histFromResultsLocked 物化结果（查询/全览物化，平铺或合并序皆可）的直方图：
// 两遍 O(n)——第一遍带锚点继承定时间范围（桶骨架），第二遍分桶。
// 结果上限内（2000 万封顶）两遍约 200ms，惰性调用不阻塞查询主路径
func (s *Session) histFromResultsLocked(results []ResultRow, nBuckets int) *Histogram {
	entryById := s.entryByIdLocked()
	st := &anchorState{}
	var minTS, maxTS int64
	var noTS int64
	for i := range results {
		t := st.effective(&results[i], entryById)
		if t <= 0 {
			noTS++
			continue
		}
		if minTS == 0 || t < minTS {
			minTS = t
		}
		if t > maxTS {
			maxTS = t
		}
	}
	if minTS == 0 {
		return &Histogram{NoTS: noTS}
	}
	start, step, n := histFrame(minTS, maxTS, nBuckets)

	h := &Histogram{Start: start, Step: step, HasData: true, NoTS: noTS}
	h.Buckets = make([][7]int32, n)
	st = &anchorState{firstTS: st.firstTS} // 复用 firstTS 缓存，继承状态从头来
	for i := range results {
		t := st.effective(&results[i], entryById)
		if t <= 0 {
			continue
		}
		b := int((t - h.Start) / h.Step)
		if b < 0 {
			b = 0
		} else if b >= n {
			b = n - 1
		}
		if lv := results[i].Level; lv >= 0 && int(lv) < 6 {
			h.Buckets[b][lv]++
		} else {
			h.Buckets[b][6]++
		}
	}
	h.finish()
	return h
}

// finish 汇总最大桶总量（聚合热点路径不做逐桶求和，末尾一遍 O(桶数)）
func (h *Histogram) finish() {
	for _, c := range h.Buckets {
		t := c[0] + c[1] + c[2] + c[3] + c[4] + c[5] + c[6]
		if t > h.MaxTotal {
			h.MaxTotal = t
		}
	}
}

// IndexByTime 定位结果集中「锚点有效时间 >= tsNS 的最早一条」的全局下标（双击直方图跳转用；
// 无匹配返回 -1）。合并序与平铺序语义统一：都跳到该时刻之后最早出现的那条日志。
//   - 物化结果（查询/全览物化）：O(n) 单遍 + 锚点继承（与直方图聚合同一 effective 口径）
//   - 零物化全览：按文件对 idx.ts 二分（日志时间基本单调；无时间行 ts=0 视作极小被自然
//     跳过，堆栈续行落在锚点附近，定位差一两行可接受），各文件候选取全局最早
func (s *Session) IndexByTime(tsNS int64) int {
	if tsNS <= 0 {
		return -1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mode == modeBrowseVirtual {
		bestVal := int64(0)
		bestIdx := -1
		for fi, e := range s.browseFiles {
			ts := e.idx.ts
			li := sort.Search(len(ts), func(i int) bool { return ts[i] >= tsNS })
			if li >= len(ts) {
				continue
			}
			// 二分假设 ts 单调，但无时间行（ts=0）会打破单调使落点偏后：
			// 限定窗口内向前线性校正找更早的命中行（堆栈/SQL 续行段通常千行内）
			lo := max(0, li-1024)
			for i := lo; i < li; i++ {
				if ts[i] >= tsNS {
					li = i
					break
				}
			}
			if bestIdx < 0 || ts[li] < bestVal {
				var base int64
				if fi > 0 {
					base = s.browsePrefix[fi-1]
				}
				bestVal = ts[li]
				bestIdx = int(base) + li
			}
		}
		return bestIdx
	}
	entryById := s.entryByIdLocked()
	st := &anchorState{}
	bestVal := int64(0)
	bestIdx := -1
	for i := range s.results {
		t := st.effective(&s.results[i], entryById)
		if t >= tsNS && (bestIdx < 0 || t < bestVal) {
			bestVal, bestIdx = t, i
			if t == tsNS {
				break // 恰好命中目标时刻，不可能更早
			}
		}
	}
	return bestIdx
}
