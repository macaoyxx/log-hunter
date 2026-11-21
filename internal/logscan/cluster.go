package logscan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 本文件实现两类能力，全部为只读操作、不改变会话结果状态：
//  1. ClusterResults —— 当前结果集的错误聚类（模板归一 + 指纹分组），
//     回答「错误有几种、各多少条、代表行长什么样」；
//  2. SideQuery / SideCluster —— AI 取证用的影子查询：一次性查询/聚类，
//     绝不写 s.results——agent 自主取证若走主查询路径会覆盖用户屏幕上的结果集。
//
// 性能口径：归一在 UTF-8 侧（decodeLine 之后），与高亮同侧，GBK 文件无需特殊处理；
// 参与聚类的行数有采样上限（clusterSampleLimit），超出按步长均匀采样并在报告标注口径，
// 组数为确定性结果（同输入同输出），毫秒~秒级，全程持读锁（与导出同语义：秒级终端操作）

// ---- 常量 ----

const (
	// clusterSampleLimit 参与聚类的行数上限：归一是逐行正则替换，千万级全量会到分钟级，
	// 10 万行实测秒级内；超出按步长均匀采样（组分布代表性足够，计数标注采样口径）。
	// 已知弱点：严格周期交替且周期恰等于 stride 的数据会被系统性跳采一类（概率极低，
	// 真实日志的聚簇/爆发形态不受影响——采样按密度等比例覆盖各段）
	clusterSampleLimit = 100_000
	// clusterMaxGroups 返回组数上限：组过多失去「一眼看清」意义，其余归并为「其他」组
	clusterMaxGroups = 200
	// clusterBatch 每批处理的参与行数（groupedRead 顺序读的批粒度，与导出一致量级）
	clusterBatch = 5000
	// clusterNormInputCap 参与归一的行前缀上限（字节）：超长行（SQL dump 等）后半段
	// 通常是数据而非模板结构，截前 2KB 控制正则代价
	clusterNormInputCap = 2048
	// clusterTemplateCap 模板展示/key 上限（rune）：分组区分度集中在前缀，截断控制内存
	// （最坏 10 万组 × 256B key = 25MB 级，可承受）
	clusterTemplateCap = 256
	// clusterSampleTextCap 组内代表行/影子查询单行的展示文本上限（字节，rune 安全截断）。
	// 放宽到 2KB：SQL/dump 行 800B 太紧，截断残句直接影响定位；超长行读全走 get_line
	clusterSampleTextCap = 2048
	// sideQueryMaxLimit 影子查询单次返回行数上限：中小规模命中一次拿全免翻页；
	// 更大量级转 cluster 聚合（窗口物理上限），offset 续读兜底
	sideQueryMaxLimit = 500
	// sideOffsetMax 影子查询 offset 上限：防深翻页（跳过行虽只计数仍要扫候选）
	sideOffsetMax = 5000
	// sideCountCap 有文本条件的命中计数上限：找够行后继续只计数给模型规模感，
	// 到顶标「≥N」提前终止——无文本条件走索引精确计数（零成本），此处仅兜文本过滤路径
	sideCountCap = 100_000
	// followCap follow 工具拉取行数上限：链路尾部用 offset 续读补，不必一次灌爆；
	// 1000 行最坏约 2MB 文本，是历史预算可承受的边界
	followCap = 1000
	// followCollectCap follow 全收集的命中上限：offset 是全局时间序偏移需全收集，
	// 但 10 万命中时模型（步数×窗口）已读不完、时间序精确无意义，封顶防极端
	// 宽标识的全扫耗时与内存爆炸（10 万 × 40B ≈ 4MB）
	followCollectCap = 100_000
)

// ---- 数据结构（JSON 给前端）----

// ClusterSample 组内代表行：锚点两用——Idx 为当前结果集全局下标（-1 = 非结果集行，
// 见 SideCluster），File+LineNo 供「文件名:行号」口径引用（开上下文面板）
type ClusterSample struct {
	Idx    int64  `json:"idx"`
	File   string `json:"file"`
	FileID int64  `json:"fileId"`
	LineNo int64  `json:"lineNo"`
	Offset int64  `json:"offset"` // 文件内字节偏移（开上下文面板用）
	TS     int64  `json:"ts"`
	Level  int8   `json:"level"`
	Text   string `json:"text"`
}

// ClusterGroup 同模板行组
type ClusterGroup struct {
	Template string          `json:"template"` // 归一模板（易变项已替换为 <ts>/<n> 等占位符）
	Count    int             `json:"count"`    // 组内参与行数（采样口径下为采样计数）
	Level    int8            `json:"level"`    // 组内众数级别（-1 = 无级别/混合）
	FirstTS  int64           `json:"firstTs"`
	LastTS   int64           `json:"lastTs"`
	FirstIdx int64           `json:"firstIdx"` // 组内首条全局下标（-1 = 不在当前结果集，如「其他」组）
	Samples  []ClusterSample `json:"samples"`
}

// ClusterReport 聚类报告
type ClusterReport struct {
	Groups       []ClusterGroup `json:"groups"`
	TotalRows    int            `json:"totalRows"`    // 聚类范围总行数
	Participated int            `json:"participated"` // 实际参与聚类行数
	Sampled      bool           `json:"sampled"`      // true = 参与数 < 总数（采样口径）
	CostMs       int64          `json:"costMs"`
}

// ClusterProgress 聚类进度（事件负载）
type ClusterProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// SideRow 影子查询单行（agent 取证返回）
type SideRow struct {
	File   string `json:"file"`
	FileID int64  `json:"fileId"`
	LineNo int64  `json:"lineNo"`
	Offset int64  `json:"offset"`
	TS     int64  `json:"ts"`
	Level  int8   `json:"level"`
	Text   string `json:"text"`
}

// ---- 模板归一 ----

// clusterNormRe 易变项归一（单遍替换，alternation 分支按「最具体优先」排列——Go 正则
// 为 leftmost-first 语义，GUID/时间戳/IP 必须排在裸数字前，否则被数字分支截胡）：
// GUID > 日期时间（含 ISO 的 T 与空格分隔、时分秒、毫秒变体）> 时:分:秒 > IPv4 >
// 长十六进制（traceId/sessionId 等）> 0x 前缀十六进制 > 小数/整数
var clusterNormRe = regexp.MustCompile(
	`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}` +
		`|\d{4}[-/]\d{1,2}[-/]\d{1,2}[T ]\d{1,2}:\d{2}(:\d{2})?(\.\d{1,9})?` +
		`|\d{1,2}:\d{2}(:\d{2})?(\.\d{1,9})?` +
		`|\d{1,3}(\.\d{1,3}){3}` +
		`|[0-9a-fA-F]{16,}` +
		`|0[xX][0-9a-fA-F]+` +
		`|\d+\.\d+|\d+`)

// normalizeTemplate 行文本 → 归一模板：易变项替换为占位符，前后截断控制代价与内存
func normalizeTemplate(s string) string {
	if len(s) > clusterNormInputCap {
		s = s[:clusterNormInputCap]
	}
	out := clusterNormRe.ReplaceAllStringFunc(s, clusterPlaceholder)
	if len(out) > clusterTemplateCap {
		cut := clusterTemplateCap
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut]
	}
	return out
}

// clusterPlaceholder 按匹配串特征分类占位符（alternation 保证匹配完整，此处仅判型）
func clusterPlaceholder(m string) string {
	switch {
	case len(m) == 36 && strings.Count(m, "-") == 4:
		return "<gid>"
	case strings.ContainsAny(m, ":-/"):
		return "<ts>"
	case strings.Count(m, ".") == 3:
		return "<ip>"
	case hasHexLetter(m):
		return "<hex>"
	default:
		return "<n>"
	}
}

// hasHexLetter 判定「真」十六进制串：全 hex 字符且至少含一个 a-f 字母——
// 0-9 是 hex 子集，纯数字串（如 8080）不能凭此判 hex（应归 <n>）；
// 0x 前缀已明示十六进制，剥前缀后全 hex 即可
func hasHexLetter(s string) bool {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
		if s == "" {
			return false
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		return true
	}
	hasLetter := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
			hasLetter = true
		default:
			return false
		}
	}
	return hasLetter
}

// capSampleText 代表行展示文本截断（rune 边界安全）
func capSampleText(s string) string {
	if len(s) <= clusterSampleTextCap {
		return s
	}
	cut := clusterSampleTextCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …"
}

// ---- 分组累计器（ClusterResults 与 SideCluster 共用）----

type clusterGroupAcc struct {
	count    int
	levels   [6]int
	firstTS  int64
	lastTS   int64
	firstIdx int64
	samples  []ClusterSample
}

type clusterAcc struct {
	mu     sync.Mutex
	groups map[string]*clusterGroupAcc
}

func newClusterAcc() *clusterAcc {
	return &clusterAcc{groups: make(map[string]*clusterGroupAcc, 256)}
}

// add 计入一行：tpl 为归一模板，idx 为聚类范围内的序号（结果集全局下标或 -1），
// sample 非 nil 时计入代表行（每组保留前 3 条）
func (a *clusterAcc) add(tpl string, ts int64, idx int64, level int8, sample *ClusterSample) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.groups[tpl]
	if g == nil {
		g = &clusterGroupAcc{firstIdx: idx, firstTS: ts, lastTS: ts}
		a.groups[tpl] = g
	}
	g.count++
	if level >= 0 && int(level) < len(g.levels) {
		g.levels[level]++
	}
	if ts > 0 {
		if g.firstTS == 0 || ts < g.firstTS {
			g.firstTS = ts
		}
		if ts > g.lastTS {
			g.lastTS = ts
		}
	}
	if sample != nil && len(g.samples) < 3 {
		g.samples = append(g.samples, *sample)
	}
}

// finish 排序取 top N，其余归并「其他」组；众数级别在此计算（add 热路径零计算）
func (a *clusterAcc) finish(total, participated int, sampled bool, costMs int64) *ClusterReport {
	type kv struct {
		tpl string
		g   *clusterGroupAcc
	}
	all := make([]kv, 0, len(a.groups))
	for t, g := range a.groups {
		all = append(all, kv{t, g})
	}
	// 组内计数降序，同数按模板字典序稳定排（确定性输出）
	sort.Slice(all, func(i, j int) bool {
		if all[i].g.count != all[j].g.count {
			return all[i].g.count > all[j].g.count
		}
		return all[i].tpl < all[j].tpl
	})

	rep := &ClusterReport{TotalRows: total, Participated: participated, Sampled: sampled, CostMs: costMs}
	if len(all) == 0 {
		return rep
	}
	top := len(all)
	if top > clusterMaxGroups {
		top = clusterMaxGroups
	}
	for _, e := range all[:top] {
		rep.Groups = append(rep.Groups, e.g.group(e.tpl))
	}
	if rest := len(all) - top; rest > 0 {
		var cnt int
		var lv [6]int
		for _, e := range all[top:] {
			cnt += e.g.count
			for i, n := range e.g.levels {
				lv[i] += n
			}
		}
		others := ClusterGroup{Template: "（其余模板合计）", Count: cnt, FirstIdx: -1,
			Level: modeLevel(lv)}
		rep.Groups = append(rep.Groups, others)
	}
	return rep
}

func (g *clusterGroupAcc) group(tpl string) ClusterGroup {
	return ClusterGroup{
		Template: tpl, Count: g.count, Level: modeLevel(g.levels),
		FirstTS: g.firstTS, LastTS: g.lastTS, FirstIdx: g.firstIdx, Samples: g.samples,
	}
}

// modeLevel 众数级别（无级别行返回 -1）
func modeLevel(lv [6]int) int8 {
	best, n := -1, 0
	for i, c := range lv {
		if c > n {
			best, n = i, c
		}
	}
	return int8(best)
}

// ---- 当前结果集聚类 ----

// ClusterResults 对当前结果集（三态统一）做模板归一聚类：>10 万行均匀采样。
// 全程持读锁（与导出同语义）；onProgress 每批回调（读锁内调用，不得再进 Session 锁）
func (s *Session) ClusterResults(onProgress func(ClusterProgress)) *ClusterReport {
	start := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := s.totalRowsLocked()
	rep := &ClusterReport{TotalRows: total, CostMs: 0}
	if total == 0 {
		return rep
	}

	stride, sampled := 1, false
	if total > clusterSampleLimit {
		stride = (total + clusterSampleLimit - 1) / clusterSampleLimit
		sampled = true
	}
	participated := (total + stride - 1) / stride

	entryById := make(map[int64]*FileEntry, len(s.entries))
	nameById := make(map[int64]string, len(s.entries))
	for _, e := range s.entries {
		entryById[e.ID] = e
		nameById[e.ID] = e.Name
	}

	acc := newClusterAcc()
	done := 0
	// 批间推进：每批取 stride 间隔的 clusterBatch 个参与行，groupedRead 批量补全文本
	for base := 0; base < total; base += stride * clusterBatch {
		end := base + stride*clusterBatch
		if end > total {
			end = total
		}
		rows := make([]ResultRow, 0, (end-base+stride-1)/stride)
		gis := make([]int64, 0, cap(rows)) // 每行对应的结果集全局下标（采样步进）
		for gi := base; gi < end; gi += stride {
			rows = append(rows, s.fetchRowAt(gi))
			gis = append(gis, int64(gi))
		}
		groupedRead(rows, entryById, func(e *FileEntry, i int, line []byte) {
			if rows[i].Preview == "" {
				rows[i].Preview = capLine(decodeLine(e.enc, line))
			}
		})
		for i := range rows {
			r := &rows[i]
			acc.add(normalizeTemplate(r.Preview), r.TS, gis[i], r.Level, &ClusterSample{
				Idx: gis[i], File: nameById[r.FileID], FileID: r.FileID,
				LineNo: r.LineNo, Offset: r.Offset, TS: r.TS, Level: r.Level,
				Text: capSampleText(r.Preview),
			})
		}
		done += len(rows)
		if onProgress != nil {
			onProgress(ClusterProgress{Done: done, Total: participated})
		}
	}
	return acc.finish(total, participated, sampled, time.Since(start).Milliseconds())
}

// fetchRowAt 取结果集单个全局下标的行元数据（锁内调用；fetchRows 的单行版，
// 采样下标不连续，逐行取避免窗口复制浪费）
func (s *Session) fetchRowAt(gi int) ResultRow {
	if s.mode != modeBrowseVirtual {
		return s.results[gi]
	}
	row := int64(gi)
	fi := sortSearchInts64(s.browsePrefix, row)
	if fi >= len(s.browsePrefix) {
		fi = len(s.browsePrefix) - 1 // 防御：不应发生
	}
	base := int64(0)
	if fi > 0 {
		base = s.browsePrefix[fi-1]
	}
	return browseRow(s.browseFiles[fi], int(row-base))
}

// ---- 影子查询（agent 取证专用，绝不改变会话结果状态）----

// SideQueryResult 影子查询结果：Rows 展示窗口（Offset 为窗口起始命中序号）；
// Total 命中总数——无文本条件时为精确值，有文本条件且 Capped=true 时为下限
// （计数到 sideCountCap 提前终止，真实总数更大）
type SideQueryResult struct {
	Rows   []SideRow `json:"rows"`
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
	Capped bool      `json:"capped"`
}

// SideQuery 一次性查询（不写 s.results / s.stats / s.mode，主结果集不受影响）：
// agent 自主取证用——若走 RunQuery 主路径会把用户屏幕上的结果集覆盖掉。
// offset 跳过前 N 条命中（续读窗口），limit 钳到 sideQueryMaxLimit。
// 计数分场景：无文本条件（纯时间/级别/文件过滤）命中数从索引候选精确算出（零成本、
// 不读文本）；有文本条件才逐行读+匹配，找够窗口后继续只计数到 sideCountCap 提前终止
// （稀有关键字最坏全扫与改造前同量级，常见关键字只多扫一小段）。
// spec.FileIDs 为空 = 全部已加载文件。全程持读锁
func (s *Session) SideQuery(ctx context.Context, spec *QuerySpec, offset, limit int) (SideQueryResult, error) {
	if limit <= 0 || limit > sideQueryMaxLimit {
		limit = sideQueryMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	if offset > sideOffsetMax {
		offset = sideOffsetMax
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.entries) == 0 {
		return SideQueryResult{}, errors.New("没有已加载的文件")
	}
	targets := sideTargets(s.entries, spec.FileIDs)
	if len(targets) == 0 {
		return SideQueryResult{}, errors.New("指定的文件不存在或已移除")
	}

	hasTimeCond := spec.TSStart > 0 || spec.TSEnd > 0
	var levels map[Level]bool
	if len(spec.Levels) > 0 {
		levels = make(map[Level]bool, len(spec.Levels))
		for _, l := range spec.Levels {
			levels[Level(l)] = true
		}
	}

	res := SideQueryResult{Rows: make([]SideRow, 0, limit), Offset: offset}
	for _, e := range targets {
		if res.Capped {
			break
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if hasTimeCond && !e.parser.HasTime() {
			continue // 无时间列不参与时间筛选（与 RunQuery 同语义）
		}
		matcher, err := newTextMatcher(spec.Keywords, spec.Excludes, spec.Regex, spec.KeywordAnd, e.enc)
		if err != nil {
			return SideQueryResult{}, fmt.Errorf("%s: %w", e.Name, err)
		}
		var cands []int32
		if hasTimeCond || levels != nil {
			cands = e.idx.filterCandidates(spec.TSStart, spec.TSEnd, levels)
		}

		if matcher.empty() {
			// 无文本条件：命中集 = 候选行（cands 或全量），总数精确零成本（只数不读），
			// 窗口取本文件命中段与全局 [offset, offset+limit) 的交集
			n := e.idx.LineCount()
			if cands != nil {
				n = len(cands)
			}
			fileStart := res.Total // 本文件首条命中的全局序号
			res.Total += n
			lo := offset - fileStart
			if lo < 0 {
				lo = 0
			}
			hi := offset + limit - fileStart
			if hi > n {
				hi = n
			}
			if lo >= hi {
				continue
			}
			sr := &seqReader{lf: e.lf}
			for k := lo; k < hi; k++ {
				li := k
				if cands != nil {
					li = int(cands[k])
				}
				off := e.idx.offsets[li]
				raw, err := sr.line(off)
				if err != nil {
					return SideQueryResult{}, fmt.Errorf("%s: %w", e.Name, err)
				}
				res.Rows = append(res.Rows, SideRow{File: e.Name, FileID: e.ID, LineNo: int64(li + 1),
					Offset: off, TS: e.idx.ts[li], Level: e.idx.levels[li],
					Text: capSampleText(decodeLine(e.enc, raw))})
			}
			continue
		}

		// 有文本条件：逐行读+匹配；命中的全局序号 = res.Total（跨文件累计），
		// 跳过 offset 之前的命中、取 [offset, offset+limit) 窗口；计数到 sideCountCap 提前终止
		sr := &seqReader{lf: e.lf}
		var readErr error
		iterateCands(cands, e.idx.LineCount(), func(li int) bool {
			if res.Total >= sideCountCap {
				res.Capped = true
				return false
			}
			line, err := sr.line(e.idx.offsets[li])
			if err != nil {
				readErr = fmt.Errorf("%s: %w", e.Name, err)
				return false
			}
			if !matcher.match(line) {
				return true
			}
			idx := res.Total
			res.Total++
			if idx >= offset && idx < offset+limit {
				res.Rows = append(res.Rows, SideRow{File: e.Name, FileID: e.ID, LineNo: int64(li + 1),
					Offset: e.idx.offsets[li], TS: e.idx.ts[li], Level: e.idx.levels[li],
					Text: capSampleText(decodeLine(e.enc, line))})
			}
			return true
		})
		if readErr != nil {
			return SideQueryResult{}, readErr
		}
	}
	return res, nil
}

// SideStack 异常块整块读取（agent 工具 get_stack）：向前找主行（最近一条有时间戳
// 的行——异常主行通常带时间戳），向后收到下一条有时间戳的行为止（堆栈/SQL 续行
// 天然无时间戳）。一次给齐全块原文（带行号），替代 3~5 次 get_context 拼接；
// 上限 500 行 / 256KB，两种截断都显式标注并引导 get_context 续读
func (s *Session) SideStack(fileName string, line int64) ([]SideRow, error) {
	const maxLines = 500
	const maxBytes = 256 << 10
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.findEntryByNameLocked(fileName)
	if e == nil {
		return nil, errors.New("文件未找到: " + fileName)
	}
	if line < 1 || int(line) > e.idx.LineCount() {
		return nil, fmt.Errorf("%s 共 %d 行，行号 %d 越界", e.Name, e.idx.LineCount(), line)
	}
	li := int(line - 1)
	// 向前找主行；整段无时间戳（纯 dump 文件）则以目标行为界向前不再回溯
	main := li
	for main > 0 && e.idx.ts[main] == 0 {
		main--
	}
	if e.idx.ts[main] == 0 {
		main = li
	}
	end := main
	for end+1 < e.idx.LineCount() && e.idx.ts[end+1] == 0 && end+1-main+1 < maxLines {
		end++
	}
	sr := &seqReader{lf: e.lf}
	out := make([]SideRow, 0, end-main+1)
	var bytes int
	for i := main; i <= end; i++ {
		off := e.idx.offsets[i]
		raw, err := sr.line(off)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name, err)
		}
		text := capSampleText(decodeLine(e.enc, raw))
		out = append(out, SideRow{File: e.Name, FileID: e.ID, LineNo: int64(i + 1),
			Offset: off, TS: e.idx.ts[i], Level: e.idx.levels[i], Text: text})
		bytes += len(text)
		if bytes >= maxBytes {
			out[len(out)-1].Text += "…（已达 256KB 上限截断，需要更多用 get_context 续读）"
			break
		}
	}
	// 字节未到顶但行数到顶（后面仍有续行）：同样标注，避免静默截断
	if bytes < maxBytes && end+1 < e.idx.LineCount() && e.idx.ts[end+1] == 0 {
		out[len(out)-1].Text += "…（已达 500 行上限截断，需要更多用 get_context 续读）"
	}
	return out, nil
}

// SideFollow 跨全部文件按关联标识拉取（agent 工具 follow）：给请求 ID/线程名/
// 订单号等任意标识，一次给齐散落在所有文件的相关行，按时间戳合并排序成单一时间线
// （多节点交叉排查的核心动线——search 只给命中窗口且按文件分堆，串不起全链路）。
// offset 跳过合并时间序前 N 行（截断后续读窗口），窗口上限 followCap 行；
// capped = 从 offset 起仍有剩余（引导续读或换更精确的标识）。全程持读锁
func (s *Session) SideFollow(ctx context.Context, id string, offset int) (rows []SideRow, capped bool, err error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false, errors.New("缺少关联标识")
	}
	if offset < 0 {
		offset = 0
	}
	if offset > sideOffsetMax {
		offset = sideOffsetMax
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.entries) == 0 {
		return nil, false, errors.New("没有已加载的文件")
	}

	// 收集阶段只存轻量命中（文件/行号/偏移/时间戳/级别），不存展示文本。
	// 关键：offset 是「全局时间序」的偏移，必须收集全部命中再排序才准确——
	// 按扫描序截断再排序会让跨文件时间交错的命中漏读/重读。
	// 轻量结构（~40B/条）让全收集内存可控（百万命中 ~40MB，远小于全内存索引）。
	type hit struct {
		e     *FileEntry
		li    int
		off   int64
		ts    int64
		level int8
	}
	var hits []hit
	for _, e := range s.entries {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if len(hits) >= followCollectCap {
			break // 收集到上限：剩余命中不再收集（10 万级时间序精确已无意义）
		}
		// 单标识按一个关键字整串匹配（标识含空格不拆词）
		matcher, mErr := newTextMatcher([]string{id}, nil, "", false, e.enc)
		if mErr != nil {
			return nil, false, fmt.Errorf("%s: %w", e.Name, mErr)
		}
		sr := &seqReader{lf: e.lf}
		for li := 0; li < e.idx.LineCount(); li++ {
			raw, rErr := sr.line(e.idx.offsets[li])
			if rErr != nil {
				return nil, false, fmt.Errorf("%s: %w", e.Name, rErr)
			}
			if !matcher.match(raw) {
				continue
			}
			if len(hits) >= followCollectCap {
				break
			}
			hits = append(hits, hit{e, li, e.idx.offsets[li], e.idx.ts[li], e.idx.levels[li]})
		}
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}

	// 全局时间序稳定排序（同刻保持文件序）；无时间戳行（个别无时间文件）排前不散乱
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].ts != hits[j].ts {
			return hits[i].ts < hits[j].ts
		}
		return false
	})

	// 窗口切片：跳过 offset 之前的命中，取 offset 起的 ≤ followCap 行；
	// capped = 从 offset 起仍有剩余未读
	if offset >= len(hits) {
		return nil, false, nil
	}
	hits = hits[offset:]
	if len(hits) > followCap {
		hits = hits[:followCap]
		capped = true
	}

	// 读窗口展示文本：按 (文件, 行号) 排序顺序读（防随机 IO），再按时间序回填
	rows = make([]SideRow, len(hits))
	order := make([]int, len(hits))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if hits[order[a]].e.ID != hits[order[b]].e.ID {
			return hits[order[a]].e.ID < hits[order[b]].e.ID
		}
		return hits[order[a]].li < hits[order[b]].li
	})
	var lastE *FileEntry
	var sr *seqReader
	for _, i := range order {
		h := hits[i]
		if h.e != lastE {
			lastE = h.e
			sr = &seqReader{lf: h.e.lf}
		}
		raw, rErr := sr.line(h.off)
		if rErr != nil {
			return nil, false, fmt.Errorf("%s: %w", h.e.Name, rErr)
		}
		rows[i] = SideRow{File: h.e.Name, FileID: h.e.ID, LineNo: int64(h.li + 1),
			Offset: h.off, TS: h.ts, Level: h.level,
			Text: capSampleText(decodeLine(h.e.enc, raw))}
	}
	return rows, capped, nil
}

// FileIDsByNames 文件名→ID 解析（search 的 files 过滤参数用）：精确名/基名命中，
// 子串唯一命中也收；返回未匹配名供调用方提示
func (s *Session) FileIDsByNames(names []string) (ids []int64, missed []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		var hit *FileEntry
		for _, e := range s.entries {
			if e.Name == n || filepath.Base(e.Path) == n {
				hit = e
				break
			}
		}
		if hit == nil { // 子串唯一命中兜底（模型常只记得文件名片段）
			for _, e := range s.entries {
				if strings.Contains(e.Name, n) {
					if hit != nil {
						hit = nil // 多义不猜
						break
					}
					hit = e
				}
			}
		}
		if hit != nil {
			ids = append(ids, hit.ID)
		} else {
			missed = append(missed, n)
		}
	}
	return ids, missed
}

// iterateCands 遍历候选行下标：cands nil = 全部行（0..n-1）；fn 返回 false 中止
func iterateCands(cands []int32, total int, fn func(li int) bool) {
	if cands != nil {
		for _, li := range cands {
			if !fn(int(li)) {
				return
			}
		}
		return
	}
	for li := 0; li < total; li++ {
		if !fn(li) {
			return
		}
	}
}

// sideTargets 按 FileIDs 过滤目标文件（保持加入顺序；ids 空 = 全部）
func sideTargets(entries []*FileEntry, ids []int64) []*FileEntry {
	if len(ids) == 0 {
		return append([]*FileEntry(nil), entries...)
	}
	var out []*FileEntry
	for _, e := range entries {
		for _, id := range ids {
			if e.ID == id {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// SideCluster 对全部已加载文件（或指定 FileIDs）按级别过滤后全局聚类（agent 视角）：
// 与 ClusterResults 的区别在于作用域——这里是原始日志行而非当前结果集，
// 因此组的 Idx 锚点为 -1（不在结果集内），引用走「文件名:行号」口径。
// 参与行同样受 clusterSampleLimit 约束（跨文件步长均匀采样）
func (s *Session) SideCluster(ctx context.Context, fileIDs []int64, levels []int8, onProgress func(ClusterProgress)) (*ClusterReport, error) {
	start := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.entries) == 0 {
		return nil, errors.New("没有已加载的文件")
	}
	targets := sideTargets(s.entries, fileIDs)
	if len(targets) == 0 {
		return nil, errors.New("指定的文件不存在或已移除")
	}
	var lv map[Level]bool
	if len(levels) > 0 {
		lv = make(map[Level]bool, len(levels))
		for _, l := range levels {
			lv[Level(l)] = true
		}
	}

	// 第一遍：统计各文件目标行数（filterCandidates 结果只用于计数）
	type fileCands struct {
		e     *FileEntry
		cands []int32 // nil = 全部行
		n     int
	}
	fcs := make([]fileCands, 0, len(targets))
	var total int
	for _, e := range targets {
		var cands []int32
		if lv != nil {
			cands = e.idx.filterCandidates(0, 0, lv)
		}
		n := e.idx.LineCount()
		if cands != nil {
			n = len(cands)
		}
		if n > 0 {
			fcs = append(fcs, fileCands{e, cands, n})
			total += n
		}
	}
	if total == 0 {
		return &ClusterReport{CostMs: time.Since(start).Milliseconds()}, nil
	}
	stride, sampled := 1, false
	if total > clusterSampleLimit {
		stride = (total + clusterSampleLimit - 1) / clusterSampleLimit
		sampled = true
	}

	// 第二遍：跨文件均匀采样——目标行全局序号 % stride == 0 者入选，
	// 文件间自动错开起点（单文件内从第 0 行采会系统性偏采文件头部）
	acc := newClusterAcc()
	participated := 0
	base := 0 // 该文件首条目标行的全局序号
	for _, fc := range fcs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pick := make([]int, 0, fc.n/stride+1)
		for k := 0; k < fc.n; k++ {
			if (base+k)%stride == 0 {
				pick = append(pick, k)
			}
		}
		base += fc.n
		if len(pick) == 0 {
			continue
		}
		sr := &seqReader{lf: fc.e.lf}
		rowIdx := int64(-1) // SideCluster 行不在结果集，锚点 -1
		for _, k := range pick {
			var li int
			if fc.cands != nil {
				li = int(fc.cands[k])
			} else {
				li = k
			}
			line, err := sr.line(fc.e.idx.offsets[li])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", fc.e.Name, err)
			}
			text := capSampleText(decodeLine(fc.e.enc, line))
			acc.add(normalizeTemplate(text), fc.e.idx.ts[li], rowIdx, fc.e.idx.levels[li],
				&ClusterSample{Idx: rowIdx, File: fc.e.Name, FileID: fc.e.ID,
					LineNo: int64(li + 1), Offset: fc.e.idx.offsets[li], TS: fc.e.idx.ts[li],
					Level: fc.e.idx.levels[li], Text: text})
			participated++
			if onProgress != nil && participated%clusterBatch == 0 {
				onProgress(ClusterProgress{Done: participated, Total: total})
			}
		}
	}
	return acc.finish(total, participated, sampled, time.Since(start).Milliseconds()), nil
}

// ---- 按名取行（agent 工具：get_context / get_line）----

// findEntryByName 按文件名找条目：先精确匹配，无命中再唯一子串匹配（agent 给的名字
// 常是 search 返回的文件名，偶尔会截断）；歧义/未命中返回 nil
func (s *Session) findEntryByName(name string) *FileEntry {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.Name == name || filepath.Base(e.Path) == name {
			return e
		}
	}
	var hit *FileEntry
	for _, e := range s.entries {
		if strings.Contains(e.Name, name) {
			if hit != nil {
				return nil // 多个命中：歧义
			}
			hit = e
		}
	}
	return hit
}

// SideContext 按文件名+行号取上下文（agent 工具 get_context）：before/after 钳到 [0,50]
func (s *Session) SideContext(fileName string, line int64, before, after int64) ([]SideRow, error) {
	const maxSide = 50
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	if before > maxSide {
		before = maxSide
	}
	if after > maxSide {
		after = maxSide
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.findEntryByNameLocked(fileName)
	if e == nil {
		return nil, errors.New("文件未找到: " + fileName)
	}
	li := line - 1
	if li < 0 || int(li) >= e.idx.LineCount() {
		return nil, fmt.Errorf("%s 共 %d 行，行号 %d 越界", e.Name, e.idx.LineCount(), line)
	}
	rows := readContext(e.lf, e.enc, e.idx, e.idx.offsets[li], before, after)
	out := make([]SideRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SideRow{File: e.Name, FileID: e.ID, LineNo: r.LineNo,
			Offset: r.Offset, TS: r.TS, Level: r.Level, Text: capSampleText(r.Text)})
	}
	return out, nil
}

// SideLineText 按文件名+行号读完整原文（agent 工具 get_line——超长 SQL/堆栈行全文）
func (s *Session) SideLineText(fileName string, line int64) (SideRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.findEntryByNameLocked(fileName)
	if e == nil {
		return SideRow{}, errors.New("文件未找到: " + fileName)
	}
	li := line - 1
	if li < 0 || int(li) >= e.idx.LineCount() {
		return SideRow{}, fmt.Errorf("%s 共 %d 行，行号 %d 越界", e.Name, e.idx.LineCount(), line)
	}
	off := e.idx.offsets[li]
	sr := &seqReader{lf: e.lf}
	raw, err := sr.line(off)
	if err != nil {
		return SideRow{}, fmt.Errorf("%s: %w", e.Name, err)
	}
	return SideRow{File: e.Name, FileID: e.ID, LineNo: line, Offset: off,
		TS: e.idx.ts[li], Level: e.idx.levels[li],
		Text: decodeLine(e.enc, raw)}, nil // 完整原文不截（超长行读全的唯一通路）
}

// findEntryByNameLocked 锁内版按名找条目（SideContext/SideLineText 已持读锁）
func (s *Session) findEntryByNameLocked(name string) *FileEntry {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, e := range s.entries {
		if e.Name == name || filepath.Base(e.Path) == name {
			return e
		}
	}
	var hit *FileEntry
	for _, e := range s.entries {
		if strings.Contains(e.Name, name) {
			if hit != nil {
				return nil
			}
			hit = e
		}
	}
	return hit
}
