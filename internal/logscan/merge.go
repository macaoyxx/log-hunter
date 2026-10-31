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
		p := int64(-1) // 该文件最近一条非零 ts 的行下标（0-based），-1 = 尚无锚点
		j := i
		for ; j < len(results) && results[j].FileID == fid; j++ {
			if e == nil { // 防御：文件已移除（移除即清结果，正常不出现）
				keys[j] = mergeKeyTail
				continue
			}
			li := results[j].LineNo - 1
			// 从 p+1 推进到当前行，更新最近非零 ts 位置（p 单调前进）
			for k := p + 1; k <= li; k++ {
				if e.idx.ts[k] > 0 {
					p = k
				}
			}
			switch {
			case results[j].TS > 0:
				keys[j] = results[j].TS
			case p >= 0 && p <= li:
				keys[j] = e.idx.ts[p]
			default:
				keys[j] = mergeKeyTail
			}
		}
		i = j
	}
	sort.Stable(&mergeSorter{rows: results, keys: keys})
}

// mergeSorter 行与 key 绑定排序：直接对 results 用 sort.SliceStable 会只换行不换 key
// （key 是独立数组，不随元素移动），比较函数随即错位——Swap 必须同步移动两者
type mergeSorter struct {
	rows []ResultRow
	keys []int64
}

func (m *mergeSorter) Len() int           { return len(m.rows) }
func (m *mergeSorter) Less(a, b int) bool { return m.keys[a] < m.keys[b] }
func (m *mergeSorter) Swap(a, b int) {
	m.rows[a], m.rows[b] = m.rows[b], m.rows[a]
	m.keys[a], m.keys[b] = m.keys[b], m.keys[a]
}

// sortFlat 恢复平铺序（文件加入顺序 + 行号升序）。FileID 自增即加入顺序
func sortFlat(results []ResultRow) {
	if len(results) < 2 {
		return
	}
	sort.SliceStable(results, func(a, b int) bool {
		if results[a].FileID != results[b].FileID {
			return results[a].FileID < results[b].FileID
		}
		return results[a].LineNo < results[b].LineNo
	})
}

// SetMerged 切换结果视图模式（平铺 / 时间合并），有结果时即时重排、不重新扫描文件。
// 外层 opMu 与 RunQuery/RemoveFile 串行化；排序全程持 s.mu 写锁——原地重排的是
// s.results 底层数组，锁外进行会让并发 GetResults（RLock）读到行重复/缺失的中间态
func (s *Session) SetMerged(merged bool) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merged = merged
	results := s.results
	entries := make([]*FileEntry, len(s.entries))
	copy(entries, s.entries)
	if len(results) < 2 {
		return
	}
	entryById := make(map[int64]*FileEntry, len(entries))
	for _, e := range entries {
		entryById[e.ID] = e
	}
	// 合并排序的 key 计算要求平铺序输入：先归位再合并（结果当前可能是上次的合并序）。
	// 两趟稳定排序均为几百 ms 量级（200 万上限内），切换可感知但不卡顿
	sortFlat(results)
	if merged {
		sortMerged(results, entryById)
	}
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
