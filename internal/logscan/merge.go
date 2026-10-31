package logscan

import (
	"math"
	"sort"
)

// mergeKeyTail 无锚点行（文件开头未出现时间 / 整文件无时间格式）的排序 key：
// 取 int64 最大值使这些行在稳定排序中自然置尾，保持文件内原相对顺序
const mergeKeyTail = int64(math.MaxInt64)

// sortMerged 时间合并排序：按「向前继承锚点」的时间 key 对结果集稳定排序，
// 多文件命中归并成单一时间线。
//
// key 规则（与 filterCandidates 的时间筛选同一语义，无时间行附属前一条有时间日志）：
//   - 行自身 TS>0 → key=TS
//   - TS=0 → 继承同文件行号最近的前一条非零时间戳
//   - 无锚点 → mergeKeyTail 置尾（无时间文件的命中自然落此处，按加入序排列）
//
// 为什么要求输入是平铺序（同一文件连续、行号升序）：锚点继承需对照文件索引的
// ts 数组逐行推进，用「最近非零 ts 行下标 p」随行号单调前进，扫描总量不超过
// 文件总行数，均摊 O(1)——乱序输入则每行都要回扫。
//
// 稳定排序保证同 key 保持原相对顺序：异常堆栈/SQL 续行紧贴主行不被其他节点
// 同刻的行插散、无时间文件之间按加入序、同毫秒跨文件按加入序（节点时钟精度
// 有限，文件加入序是可预期的确定性次序）。
func sortMerged(results []ResultRow, entryById map[int64]*FileEntry) {
	if len(results) < 2 {
		return
	}
	keys := make([]int64, len(results))
	for i := 0; i < len(results); {
		fid := results[i].FileID
		e := entryById[fid]
		// 扫描进度与锚点分离：scanned 是组内已扫到的行下标（每行无条件推进，
		// 否则遇大段无时间行时锚点卡住、后续行反复重扫全零区间——实测数据里
		// 连续数十万无时间行会把这里放大成每行上万步、总扫描数百亿次）；
		// anchor 是最近一条非零 ts 的行下标（无时间行的继承锚点）
		scanned := int64(-1)
		anchor := int64(-1)
		j := i
		for ; j < len(results) && results[j].FileID == fid; j++ {
			if e == nil { // 防御：文件已移除（移除即清结果，正常不出现）
				keys[j] = mergeKeyTail
				continue
			}
			li := results[j].LineNo - 1
			for k := scanned + 1; k <= li; k++ {
				if e.idx.ts[k] > 0 {
					anchor = k
				}
			}
			scanned = li
			switch {
			case results[j].TS > 0:
				keys[j] = results[j].TS
			case anchor >= 0 && anchor <= li:
				keys[j] = e.idx.ts[anchor]
			default:
				keys[j] = mergeKeyTail
			}
		}
		i = j
	}
	permuteStable(results, func(a, b int) bool { return keys[a] < keys[b] })
}

// permuteStable 稳定排序 results 并原地应用：排 int32 下标 + 置换环跟随重排。
// 为什么不直接排数据：sort.Stable 的 Swap 是整结构体交换（64B/次），数百万行的
// 排序需 O(n·log n) 次交换、拷贝量爆炸（实测 387 万行 44s 卡死）；排 4 字节下标
// 再 O(n) 置换，总结构体交换降为每行至多一次。less 基于当前下标比较（如 keys[a]）
func permuteStable(results []ResultRow, less func(a, b int) bool) {
	if len(results) < 2 {
		return
	}
	order := make([]int32, len(results))
	for i := range order {
		order[i] = int32(i)
	}
	sort.SliceStable(order, func(x, y int) bool { return less(int(order[x]), int(order[y])) })
	// order[k] = 排序后第 k 位取原下标 order[k]；置换环跟随需要逆语义
	//（dest[e] = 元素 e 的目标位置），先 O(n) 求逆再应用
	dest := make([]int32, len(order))
	for pos, src := range order {
		dest[src] = int32(pos)
	}
	for i := 0; i < len(dest); i++ {
		for dest[i] != int32(i) {
			j := dest[i]
			results[i], results[j] = results[j], results[i]
			dest[i], dest[j] = dest[j], dest[i]
		}
	}
}

// sortFlat 恢复平铺序（文件加入顺序 + 行号升序）。FileID 自增即加入顺序
func sortFlat(results []ResultRow) {
	permuteStable(results, func(a, b int) bool {
		ra, rb := &results[a], &results[b]
		if ra.FileID != rb.FileID {
			return ra.FileID < rb.FileID
		}
		return ra.LineNo < rb.LineNo
	})
}

// SetMerged 切换结果视图模式（平铺 / 时间合并），即时重排、不重新扫描文件。
// 外层 opMu 与 RunQuery/RemoveFile 串行化；排序全程持 s.mu 写锁——原地重排的是
// s.results 底层数组，锁外进行会让并发 GetResults（RLock）读到行重复/缺失的中间态
func (s *Session) SetMerged(merged bool) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merged = merged

	switch {
	case s.mode == modeBrowseVirtual && merged:
		// 零物化全览 → 时间合并：归并需要可排序的结果数组，从行索引按需物化
		// （受自适应上限约束，超大日志合并全览会截断并提示——切换即接受该权衡）
		s.materializeBrowseLocked()
		if len(s.results) >= 2 {
			sortMerged(s.results, s.entryByIdLocked())
		}
	case s.mode == modeBrowseMat && !merged:
		// 全览物化 → 平铺：丢弃物化回零物化视图，Total 恢复全量、截断解除
		s.results = nil
		s.mode = modeBrowseVirtual
		s.stats.Total = s.totalRowsLocked()
		s.stats.Truncated = false
	default:
		// 查询结果的平铺↔合并两态排序
		results := s.results
		if len(results) < 2 {
			return
		}
		// 合并排序的 key 计算要求平铺序输入：先归位再合并（结果当前可能是上次的合并序）。
		// 两趟稳定排序均为几百 ms 量级（自适应上限内），切换可感知但不卡顿
		sortFlat(results)
		if merged {
			sortMerged(results, s.entryByIdLocked())
		}
	}
}

// entryByIdLocked 当前文件表的 id 映射（锁内调用）
func (s *Session) entryByIdLocked() map[int64]*FileEntry {
	m := make(map[int64]*FileEntry, len(s.entries))
	for _, e := range s.entries {
		m[e.ID] = e
	}
	return m
}

// materializeBrowseLocked 从行索引物化全览结果（持锁调用）：按文件分段顺序构造，
// 避免逐行二分；受 resultLimit 约束，超出截断。级别/最长行统计沿用全览口径不变
func (s *Session) materializeBrowseLocked() {
	virtualTotal := s.totalRowsLocked() // 先取零物化全量（改 mode 后 totalRows 语义即变）
	limit := s.resultLimit
	results := make([]ResultRow, 0, min(limit, virtualTotal))
	for _, e := range s.browseFiles {
		if len(results) >= limit {
			break
		}
		for li := 0; li < e.idx.LineCount() && len(results) < limit; li++ {
			results = append(results, browseRow(e, li))
		}
	}
	s.results = results
	s.mode = modeBrowseMat
	s.stats.Total = len(results)
	s.stats.Truncated = len(results) < virtualTotal
}

// groupedRead 按 (FileID, Offset) 排序后分组顺序读每行原文，回调 (所属文件, 结果下标, 行字节)。
// 为什么不直接按结果顺序读：平铺序下结果天然按文件聚簇、offset 升序，顺序读即最优；
// 时间合并序打乱聚簇后窗口内同文件行散布且可能时钟回拨（时间序≠offset 序），先按
// offset 升序整理访问顺序才能复用 seqReader 缓冲（无随机 IO）。平铺序下该排序是
// 稳定 no-op（本就有序），两种模式共用一条路径
func groupedRead(rows []ResultRow, entryById map[int64]*FileEntry, fn func(e *FileEntry, i int, line []byte)) {
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, rb := &rows[order[a]], &rows[order[b]]
		if ra.FileID != rb.FileID {
			return ra.FileID < rb.FileID
		}
		return ra.Offset < rb.Offset
	})
	for k := 0; k < len(order); {
		fid := rows[order[k]].FileID
		e := entryById[fid]
		h := k
		for ; h < len(order) && rows[order[h]].FileID == fid; h++ {
		}
		if e == nil { // 文件已被移除：该组全部跳过（预览留空）
			k = h
			continue
		}
		sr := &seqReader{lf: e.lf}
		for ; k < h; k++ {
			if line, err := sr.line(rows[order[k]].Offset); err == nil {
				fn(e, order[k], line)
			}
		}
	}
}
