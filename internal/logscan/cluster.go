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
	// clusterSampleTextCap 组内代表行的展示文本上限（字节，rune 安全截断）
	clusterSampleTextCap = 800
	// sideQueryMaxLimit 影子查询单次返回行数上限：agent 上下文防护（发更多只是浪费 token）
	sideQueryMaxLimit = 50
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

// SideQuery 一次性查询（不写 s.results / s.stats / s.mode，主结果集不受影响）：
// agent 自主取证用——若走 RunQuery 主路径会把用户屏幕上的结果集覆盖掉。
// limit 钳到 sideQueryMaxLimit；凑够即停（不收集全部再截断）；
// spec.FileIDs 为空 = 全部已加载文件。全程持读锁
func (s *Session) SideQuery(ctx context.Context, spec *QuerySpec, limit int) ([]SideRow, error) {
	if limit <= 0 || limit > sideQueryMaxLimit {
		limit = sideQueryMaxLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.entries) == 0 {
		return nil, errors.New("没有已加载的文件")
	}
	targets := sideTargets(s.entries, spec.FileIDs)
	if len(targets) == 0 {
		return nil, errors.New("指定的文件不存在或已移除")
	}

	hasTimeCond := spec.TSStart > 0 || spec.TSEnd > 0
	var levels map[Level]bool
	if len(spec.Levels) > 0 {
		levels = make(map[Level]bool, len(spec.Levels))
		for _, l := range spec.Levels {
			levels[Level(l)] = true
		}
	}

	out := make([]SideRow, 0, limit)
	for _, e := range targets {
		if len(out) >= limit {
			break
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if hasTimeCond && !e.parser.HasTime() {
			continue // 无时间列不参与时间筛选（与 RunQuery 同语义）
		}
		matcher, err := newTextMatcher(spec.Keywords, spec.Excludes, spec.Regex, spec.KeywordAnd, e.enc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name, err)
		}
		var cands []int32
		if hasTimeCond || levels != nil {
			cands = e.idx.filterCandidates(spec.TSStart, spec.TSEnd, levels)
		}
		sr := &seqReader{lf: e.lf}
		var readErr error
		take := func(li int) bool {
			off := e.idx.offsets[li]
			line, err := sr.line(off)
			if err != nil {
				readErr = fmt.Errorf("%s: %w", e.Name, err)
				return false
			}
			out = append(out, SideRow{File: e.Name, FileID: e.ID, LineNo: int64(li + 1),
				Offset: off, TS: e.idx.ts[li], Level: e.idx.levels[li],
				Text: capSampleText(decodeLine(e.enc, line))})
			return true
		}
		// 逐行读 + 匹配，凑够即停：agent 取证一次最多 50 行，顺序读游标即可
		// （无时间条件的全文件文本匹配最坏扫整文件，但命中关键字的查询通常很快凑满）
		if matcher.empty() {
			iterateCands(cands, e.idx.LineCount(), func(li int) bool {
				return len(out) < limit && take(li)
			})
		} else {
			iterateCands(cands, e.idx.LineCount(), func(li int) bool {
				if len(out) >= limit {
					return false
				}
				line, err := sr.line(e.idx.offsets[li])
				if err != nil {
					readErr = fmt.Errorf("%s: %w", e.Name, err)
					return false
				}
				return !matcher.match(line) || take(li)
			})
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return out, nil
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
		Text: capSampleText(decodeLine(e.enc, raw))}, nil
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
