package logscan

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// maxLineBytes 单行防御上限（64KB）：正常日志远小于此，仅防异常超长行撑爆界面/传输
const maxLineBytes = 64 << 10

// resultMode 结果集的存储形态，决定 GetResults/Export 的取行方式与 SetMerged 的切换行为：
//   - modeQuery   查询（含条件全览/合并）物化的 results 数组，受 resultLimit 上限约束
//   - modeBrowseVirtual 纯全览（无条件+平铺）的零物化视图——不存 results，按窗口从
//     行索引现翻译（Total=Σ行数永不截断，额外内存≈0）
//   - modeBrowseMat 全览切换到时间合并时按需物化的结果（上限内），切回平铺即丢弃
type resultMode int8

const (
	modeQuery resultMode = iota
	modeBrowseVirtual
	modeBrowseMat
)

// Session 一次排查会话：文件与索引全内存，不落盘；清空即释放。
// zip 解压产物是唯一例外——落系统临时目录，随移除/清空/退出自动删除，保持零残留
type Session struct {
	mu      sync.RWMutex
	entries []*FileEntry
	nextID  int64
	results []ResultRow
	stats   QueryStats
	cancel  context.CancelFunc // 当前进行中的查询/索引
	opMu    sync.Mutex         // 串行化长操作（加载/查询），避免并发扫描互相干扰
	merged  bool               // 结果视图模式：false=平铺（默认，文件加入序+行号）；true=按时间归并

	mode        resultMode     // 当前结果形态（见 resultMode 注释）
	browseFiles []*FileEntry   // 零物化全览的文件快照（勾选顺序）
	browsePrefix []int64       // browseFiles 各文件行数前缀和（全局行号→文件二分定位）
	resultLimit int            // 查询结果条数上限（按物理内存自适应，见 resultlimit.go）
	lastFreeAt  time.Time      // 上次向系统归还内存的时刻（条件 FreeOSMemory 节流用）

	tempRoot  string           // zip 解压临时根目录（懒建，ClearAll 整体删除）
	tempLock  *os.File         // 临时根锁文件句柄（持至进程退出，供其他实例启动清扫探活）
	zipDirSeq int              // zip 子目录序号
	zips      map[string]bool  // 已加载 zip 源绝对路径（防重复拖入双份内存；目录清空时同步清除）
}

// NewSession 创建空会话；结果上限按本机物理内存自适应
func NewSession() *Session {
	return &Session{
		zips:        make(map[string]bool),
		resultLimit: calcResultLimit(totalPhysicalMem()),
	}
}

// FileEntry 已加载文件的信息（JSON 给前端）+ 内部资源
type FileEntry struct {
	ID      int64  `json:"id"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Enc     string `json:"enc"`
	Format  string `json:"format"` // std/bracket/level-first/syslog/httpd/json/plain
	HasTime bool   `json:"hasTime"`
	LineCnt int64  `json:"lineCnt"`
	TSMin   int64  `json:"tsMin"` // unixNano
	TSMax   int64  `json:"tsMax"`

	lf     *lineFile
	idx    *LineIndex
	parser *LineParser
	enc    TextEncoding

	tempDir string // zip 解压来源目录（非空 = 临时解压产物，随移除/清空删除）
	zipAbs  string // 来源 zip 绝对路径（该 zip 目录清空时同步清 s.zips 登记，防重拖被挡）
}

// QuerySpec 查询条件（时间戳为 unixNano，0 = 不限；级别为 Level 枚举值列表）
type QuerySpec struct {
	FileIDs    []int64 `json:"fileIds"`
	Keywords   []string `json:"keywords"`
	Excludes   []string `json:"excludes"`
	Regex      string  `json:"regex"`
	KeywordAnd bool    `json:"keywordAnd"`
	TSStart    int64   `json:"tsStart"`
	TSEnd      int64   `json:"tsEnd"`
	Levels     []int8  `json:"levels"`
}

// ResultRow 单条命中（JSON 给前端）。Preview 为整行全文（64KB 防御截断），
// LineLen 为原始行长（rune 数，不导出，供查询统计最长行）
type ResultRow struct {
	FileID  int64  `json:"fileId"`
	LineNo  int64  `json:"lineNo"`
	Offset  int64  `json:"offset"`
	TS      int64  `json:"ts"`
	Level   int8   `json:"level"`
	Preview string `json:"preview"`
	LineLen int    `json:"-"`
}

// QueryStats 查询统计（LevelCounts 为 TRACE..FATAL 六级各自命中数，
// MaxLineLen 为最长行 rune 数——前端据此撑开横向滚动宽度；
// SkippedNoTime 为因时间条件被跳过的无时间格式文件数，前端据此提示；
// ResultLimit 为本次会话的结果条数上限——内存自适应，截断提示文案据此动态展示）
type QueryStats struct {
	Total         int    `json:"total"`
	Truncated     bool   `json:"truncated"`
	CostMs        int64  `json:"costMs"`
	LevelCounts   [6]int `json:"levelCounts"`
	MaxLineLen    int    `json:"maxLineLen"`
	SkippedNoTime int    `json:"skippedNoTime"`
	ResultLimit   int    `json:"resultLimit"`
}

// FileProgress 文件加载进度（事件负载）
type FileProgress struct {
	Name   string `json:"name"`   // 当前文件名
	Phase  string `json:"phase"`  // detect | index | unzip
	Done   int64  `json:"done"`   // 已处理字节（unzip 为已完成条目数）
	Total  int64  `json:"total"`  // 总字节（unzip 为条目总数）
	FileNo int    `json:"fileNo"` // 第几个文件（1-based）
}

// QueryProgress 查询进度（事件负载）
type QueryProgress struct {
	Scanned int64 `json:"scanned"`
	Total   int64 `json:"total"`
	Matched int   `json:"matched"`
}

// CancelAll 取消进行中的加载或查询
func (s *Session) CancelAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// AddFiles 打开文件、探测格式并构建索引；.zip 自动解压后加载其中文本文件；
// 同一会话内重复添加同一路径直接返回已有条目。
// onProgress 可为 nil；单个文件失败不影响其余（err 聚合返回）；
// notices 为非错误提示（zip 内跳过项等）。
// 多文件并行加载（4 路）：拖入一批日志是常态，串行逐个建索引会让 N 个文件
// 耗时≈N×单文件；zip 保持串行（内部已逐条目、临时目录序号非并发安全）
func (s *Session) AddFiles(ctx context.Context, paths []string, onProgress func(FileProgress)) (added []*FileEntry, errs, notices []string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}()

	type job struct {
		path   string
		fileNo int
		isZip  bool
	}
	var jobs []job
	for i, p := range paths {
		jobs = append(jobs, job{p, i + 1, strings.EqualFold(filepath.Ext(p), ".zip")})
	}

	var mu sync.Mutex // 聚合锁（errs/added 与 fileNo 映射）
	addedNo := make(map[int64]int, len(paths)) // 本次新增条目 ID → 拖入序（完成后重排展示用）
	fail := func(p string, fileNo int, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, fmt.Sprintf("%s: %v", filepath.Base(p), err))
	}
	ok := func(e *FileEntry, fileNo int) {
		mu.Lock()
		defer mu.Unlock()
		if e != nil {
			added = append(added, e)
			addedNo[e.ID] = fileNo
		}
	}

	// 普通文件并行（信号量限 4 路：IO 与 CPU 核自然交错，过多并发只会争抢）
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, j := range jobs {
		if j.isZip {
			continue
		}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			e, err := s.addOne(ctx, j.path, j.fileNo, onProgress, "", "")
			if err != nil {
				fail(j.path, j.fileNo, err)
				return
			}
			ok(e, j.fileNo)
		}(j)
	}
	wg.Wait()

	for _, j := range jobs {
		if !j.isZip {
			continue
		}
		if ctx.Err() != nil {
			errs = append(errs, "已取消")
			break // 跳出后走统一重排出口，保持与正常路径一致的拖入序
		}
		es, ns, err := s.addZip(ctx, j.path, j.fileNo, onProgress)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", filepath.Base(j.path), err))
			continue
		}
		mu.Lock()
		added = append(added, es...)
		for _, e := range es {
			addedNo[e.ID] = j.fileNo // 同 zip 条目同序号，稳定排序保持其内部相对序
		}
		notices = append(notices, ns...)
		mu.Unlock()
	}

	// 并发完成序重排回拖入序（旧条目在前不动）——文件列表展示与返回值顺序符合直觉
	s.mu.Lock()
	if len(s.entries) > 1 {
		rank := addedNo
		sort.SliceStable(s.entries, func(a, b int) bool {
			ea, eb := s.entries[a], s.entries[b]
			na, oka := rank[ea.ID]
			nb, okb := rank[eb.ID]
			if oka && okb {
				return na < nb
			}
			return !oka && okb // 旧条目保持在前；新增按拖入序排在后
		})
	}
	s.mu.Unlock()
	sort.SliceStable(added, func(a, b int) bool {
		return addedNo[added[a].ID] < addedNo[added[b].ID]
	})
	return added, errs, notices
}

// addOne 处理单个文件；e 为 nil 且 err 为 nil 表示重复添加已跳过。
// display 非空时作为显示名（zip 解压产物显示「zip名/相对路径」）；
// tempDir 非空标记临时解压产物（随移除/清空删除）
func (s *Session) addOne(ctx context.Context, path string, fileNo int, onProgress func(FileProgress), display, tempDir string) (*FileEntry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// 同会话重复文件直接复用
	s.mu.RLock()
	for _, e := range s.entries {
		if e.Path == abs {
			s.mu.RUnlock()
			return e, nil
		}
	}
	s.mu.RUnlock()

	lf, err := openLineFile(abs)
	if err != nil {
		return nil, fmt.Errorf("打开失败: %w", err)
	}
	fail := func(e error) (*FileEntry, error) {
		_ = lf.Close()
		return nil, e
	}

	// 跨区采样探测编码与格式（头/中/尾各取一段，防止文件开头恰好是特殊段如大段 SQL 误判）
	sample, err := readSample(lf)
	if err != nil {
		return fail(fmt.Errorf("读取样本失败: %w", err))
	}
	enc := detectEncoding(sample)
	parser := DetectFormat(sample, time.Local)

	if onProgress != nil {
		onProgress(FileProgress{Name: filepath.Base(abs), Phase: "index", Total: lf.Size(), FileNo: fileNo})
	}
	idx, err := buildIndex(ctx, lf, parser, func(done, total int64) {
		if onProgress != nil {
			onProgress(FileProgress{Name: filepath.Base(abs), Phase: "index", Done: done, Total: total, FileNo: fileNo})
		}
	})
	if err != nil {
		return fail(fmt.Errorf("构建索引失败: %w", err))
	}

	tsMin, tsMax := idx.TSRange()
	name := filepath.Base(abs)
	if display != "" {
		name = display
	}
	e := &FileEntry{
		Path: abs, Name: name, Size: lf.Size(),
		Enc: enc.String(), Format: parser.Kind, HasTime: parser.HasTime(),
		LineCnt: int64(idx.LineCount()), TSMin: tsMin, TSMax: tsMax,
		lf: lf, idx: idx, parser: parser, enc: enc, tempDir: tempDir,
	}
	if parser.Kind == "text" {
		e.Format = parser.Template
	}
	s.mu.Lock()
	s.nextID++
	e.ID = s.nextID
	s.entries = append(s.entries, e)
	s.mu.Unlock()
	return e, nil
}

// Files 返回当前文件列表（副本）
func (s *Session) Files() []*FileEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*FileEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// RemoveFile 移除单个文件并释放其索引；zip 解压产物移除后若同目录无其他引用，
// 一并删除该 zip 的临时子目录（零残留）
func (s *Session) RemoveFile(id int64) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	var tempDir, zipAbs string
	for i, e := range s.entries {
		if e.ID == id {
			_ = e.lf.Close()
			tempDir = e.tempDir
			zipAbs = e.zipAbs
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			break
		}
	}
	if tempDir != "" {
		used := false
		for _, e := range s.entries {
			if e.tempDir == tempDir {
				used = true
				break
			}
		}
		if !used {
			_ = os.RemoveAll(tempDir)
			// 目录已删，登记同步清除——否则删光该 zip 全部文件后重拖会被「已加载过」挡住
			delete(s.zips, zipAbs)
		}
	}
	s.mu.Unlock()
	s.results = nil
	s.stats = QueryStats{}
	s.mode = modeQuery // 浏览视图状态一并作废（配合前端自动重查重建）
	s.browseFiles = nil
	s.browsePrefix = nil
}

// ClearAll 一键清空：释放全部文件、索引与结果，删除 zip 解压临时根目录，马上可以拖入下一批
func (s *Session) ClearAll() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	for _, e := range s.entries {
		_ = e.lf.Close()
	}
	root := s.tempRoot
	s.entries = nil
	s.tempRoot = ""
	s.zipDirSeq = 0
	s.zips = make(map[string]bool)
	s.mu.Unlock()
	if root != "" {
		if s.tempLock != nil {
			_ = s.tempLock.Close() // Windows 下自己打开的文件删不掉，先放锁
			s.tempLock = nil
		}
		// 删除失败有限重试（杀毒/备份瞬时占用）；仍失败由下次启动 SweepStaleTemp 兜底
		for i := 0; i < 3; i++ {
			if err := os.RemoveAll(root); err == nil || os.IsNotExist(err) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	s.results = nil
	s.stats = QueryStats{}
	s.mode = modeQuery
	s.browseFiles = nil
	s.browsePrefix = nil
	// 引用已全部释放，但 Go GC 惰性（等堆增长才触发）且归还 OS 渐进保守，
	// 任务管理器内存不会立刻回落——清空是低频操作，强制 GC 并归还空闲页，
	// 让"零残留"在进程内存上也立即可见
	debug.FreeOSMemory()
}

// RunQuery 执行查询：时间/级别条件先经内存索引筛出候选行，再对候选（或全文件）做文本匹配；
// 结果按文件加入顺序、行号升序存于内存
func (s *Session) RunQuery(ctx context.Context, spec *QuerySpec, onProgress func(QueryProgress)) (*QueryStats, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	// 快照目标文件（保持加入顺序）
	var targets []*FileEntry
	for _, e := range s.entries {
		for _, id := range spec.FileIDs {
			if e.ID == id {
				targets = append(targets, e)
				break
			}
		}
	}
	s.mu.Unlock()
	defer cancel()

	if len(targets) == 0 {
		return nil, fmt.Errorf("没有可查询的文件")
	}
	start := time.Now()

	var levels map[Level]bool
	if len(spec.Levels) > 0 {
		levels = make(map[Level]bool, len(spec.Levels))
		for _, l := range spec.Levels {
			levels[Level(l)] = true
		}
	}

	hasTimeCond := spec.TSStart > 0 || spec.TSEnd > 0
	hasLevelCond := levels != nil
	var totalBytes int64
	for _, e := range targets {
		totalBytes += e.Size
	}

	results := make([]ResultRow, 0, 1024)
	truncated := false
	var matched int64
	var levelCounts [6]int
	maxLineLen := 0
	var scannedBytes int64
	skippedNoTime := 0
	lastEmit := time.Now()

	// 纯全览快速路径（无文本+无时间+无级别+平铺）：零物化——不收集结果数组，
	// Total=Σ行数、永不截断，GetResults 按窗口从行索引现翻译（额外内存≈0），
	// 多大日志全览都不再被上限卡住。统计直接汇总建索引时预计算的结果（O(文件数)
	// 即时返回——曾因逐行现算统计把全览拖成 O(总行数) 的数秒扫描）。
	// 有时间/级别条件（走候选物化）或合并模式（归并需物化排序）不适用
	if !hasTimeCond && !hasLevelCond && !s.merged &&
		len(spec.Keywords) == 0 && len(spec.Excludes) == 0 && spec.Regex == "" {
		var total int64
		var levelCounts [6]int
		maxLineLen := 0
		for _, e := range targets {
			total += int64(e.idx.LineCount())
			for lv, n := range e.idx.levelCounts {
				levelCounts[lv] += n
			}
			if e.idx.maxLineLen > maxLineLen {
				maxLineLen = e.idx.maxLineLen
			}
		}
		stats := &QueryStats{Total: int(total), CostMs: time.Since(start).Milliseconds(),
			LevelCounts: levelCounts, MaxLineLen: maxLineLen, ResultLimit: s.resultLimit}
		s.mu.Lock()
		s.mode = modeBrowseVirtual
		s.browseFiles = append([]*FileEntry(nil), targets...)
		s.browsePrefix = make([]int64, len(targets))
		var acc int64
		for i, e := range targets {
			acc += int64(e.idx.LineCount())
			s.browsePrefix[i] = acc
		}
		s.results = nil
		s.stats = *stats
		s.mu.Unlock()
		if onProgress != nil {
			onProgress(QueryProgress{Scanned: totalBytes, Total: totalBytes, Matched: int(total)})
		}
		return stats, nil
	}

	for _, e := range targets {
		if truncated {
			break
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 该文件不满足时间条件的快速路径：无时间列但有时间条件 → 跳过（计数供前端提示）
		if hasTimeCond && !e.parser.HasTime() {
			scannedBytes += e.Size
			skippedNoTime++
			continue
		}
		matcher, err := newTextMatcher(spec.Keywords, spec.Excludes, spec.Regex, spec.KeywordAnd, e.enc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name, err)
		}

		emit := func(r ResultRow) {
			results = append(results, r)
			matched++
			if r.Level >= 0 && int(r.Level) < len(levelCounts) {
				levelCounts[r.Level]++
			}
			if r.LineLen > maxLineLen {
				maxLineLen = r.LineLen
			}
		}

		if matcher.empty() {
			// 无文本条件：索引直出，预览延迟到结果窗口生成。
			// 无时间/级别条件 = 浏览全部行；两种来源统一为候选行下标（nil=全行）。
			// 循环体内联构造（去迭代器闭包 + emit/indexedRow 逐行调用）——千万级
			// 候选时闭包与重复边界检查开销可观（实测 2000 万条 4.6s → 3s 级）
			var cands []int32
			if hasTimeCond || hasLevelCond {
				cands = e.idx.filterCandidates(spec.TSStart, spec.TSEnd, levels)
			}
			limit := s.resultLimit
			offs, tss, lvs := e.idx.offsets, e.idx.ts, e.idx.levels
			n := len(offs)
			before := len(results)
			build := func(li int) {
				next := e.Size
				if li+1 < n {
					next = offs[li+1]
				}
				ll := 0
				if next > offs[li] {
					ll = int(next - offs[li])
				}
				r := ResultRow{FileID: e.ID, LineNo: int64(li + 1), Offset: offs[li],
					TS: tss[li], Level: lvs[li], LineLen: ll}
				results = append(results, r)
				if r.Level >= 0 && int(r.Level) < len(levelCounts) {
					levelCounts[r.Level]++
				}
				if r.LineLen > maxLineLen {
					maxLineLen = r.LineLen
				}
			}
			if cands == nil {
				for li := 0; li < n; li++ {
					if len(results) >= limit {
						truncated = true
						break
					}
					build(li)
				}
			} else {
				for _, li := range cands {
					if len(results) >= limit {
						truncated = true
						break
					}
					build(int(li))
				}
			}
			matched += int64(len(results) - before)
			scannedBytes += e.Size
		} else if cands := e.idx.filterCandidates(spec.TSStart, spec.TSEnd, levels); cands != nil {
			// 时间/级别筛出候选行，仅对候选行做文本匹配（顺序读游标，无随机 IO）
			sr := &seqReader{lf: e.lf}
			for _, li := range cands {
				if int(len(results)) >= s.resultLimit {
					truncated = true
					break
				}
				off := e.idx.offsets[li]
				line, err := sr.line(off)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", e.Name, err)
				}
				if matcher.match(line) {
					emit(indexedRow(e, int(li), line))
				}
			}
			scannedBytes += e.Size
		} else {
			// 无时间/级别条件：并行扫全文件，只收集偏移与行长（预览延迟到结果窗口按需生成）
			matches, serr := scanFile(ctx, e.lf, matcher, func(done, total int64) {
				if onProgress != nil && time.Since(lastEmit) > 80*time.Millisecond {
					lastEmit = time.Now()
					onProgress(QueryProgress{Scanned: scannedBytes + done, Total: totalBytes, Matched: int(matched)})
				}
			})
			if serr != nil {
				return nil, fmt.Errorf("%s: %w", e.Name, serr)
			}
			for _, ms := range matches {
				for _, mref := range ms {
					if int(len(results)) >= s.resultLimit {
						truncated = true
						break
					}
					li := e.idx.LineNoByOffset(mref.off) - 1
					r := ResultRow{FileID: e.ID, LineNo: li + 1, Offset: mref.off,
						Level: levelOf(e, li), LineLen: int(mref.ln)}
					if li >= 0 && int(li) < len(e.idx.ts) {
						r.TS = e.idx.ts[li]
					}
					// 内联 emit（千万级命中时闭包调用开销可观）
					results = append(results, r)
					matched++
					if r.Level >= 0 && int(r.Level) < len(levelCounts) {
						levelCounts[r.Level]++
					}
					if r.LineLen > maxLineLen {
						maxLineLen = r.LineLen
					}
				}
				if truncated {
					break
				}
			}
			scannedBytes += e.Size
		}
		if onProgress != nil {
			onProgress(QueryProgress{Scanned: scannedBytes, Total: totalBytes, Matched: int(matched)})
		}
	}

	// 合并视图模式下按时间归并产出（收集序天然平铺，满足 sortMerged 的输入要求；
	// opMu 已与 SetMerged 互斥，s.merged 读取无竞态）
	if s.merged {
		entryById := make(map[int64]*FileEntry, len(targets))
		for _, e := range targets {
			entryById[e.ID] = e
		}
		sortMerged(results, entryById)
	}

	stats := &QueryStats{Total: len(results), Truncated: truncated,
		CostMs: time.Since(start).Milliseconds(), LevelCounts: levelCounts, MaxLineLen: maxLineLen,
		SkippedNoTime: skippedNoTime, ResultLimit: s.resultLimit}
	s.mu.Lock()
	// 大结果被明显更小的新结果替换时，异步把堆归还系统（查询高峰后工作集及时
	// 回落；30s 节流避免连续查询反复触发）。延迟 1s 再归还：FreeOSMemory 是全局
	// STW，立刻跑会把几百毫秒代价计入紧邻的下一个查询
	if old := s.results; len(old) >= 5_000_000 && len(old) > 4*len(results) &&
		time.Since(s.lastFreeAt) > 30*time.Second {
		s.lastFreeAt = time.Now()
		go func() {
			time.Sleep(time.Second)
			debug.FreeOSMemory()
		}()
	}
	s.mode = modeQuery
	s.browseFiles = nil // 查询结果与零物化全览互斥：清掉浏览视图状态
	s.browsePrefix = nil
	s.results = results
	s.stats = *stats
	s.mu.Unlock()
	return stats, nil
}

// indexedRow 从索引行下标构造结果行；line 为共享游标读出的该行原文（nil 时预览留空、
// 行长用索引相邻偏移差近似）
func indexedRow(e *FileEntry, li int, line []byte) ResultRow {
	preview := ""
	lineLen := 0
	if line != nil {
		preview = capLine(decodeLine(e.enc, line))
		lineLen = utf8.RuneCount(line)
	} else {
		off := e.idx.offsets[li]
		next := e.Size
		if li+1 < len(e.idx.offsets) {
			next = e.idx.offsets[li+1]
		}
		if next > off {
			lineLen = int(next - off)
		}
	}
	return ResultRow{
		FileID: e.ID, LineNo: int64(li + 1), Offset: e.idx.offsets[li],
		TS: e.idx.ts[li], Level: e.idx.levels[li],
		Preview: preview, LineLen: lineLen,
	}
}

// levelOf 取索引中该行级别
func levelOf(e *FileEntry, li int64) int8 {
	if li >= 0 && int(li) < len(e.idx.levels) {
		return e.idx.levels[li]
	}
	return int8(LevelUnknown)
}

// totalRowsLocked 当前结果形态的总行数（锁内调用）
func (s *Session) totalRowsLocked() int {
	if s.mode == modeBrowseVirtual {
		if len(s.browsePrefix) == 0 {
			return 0
		}
		return int(s.browsePrefix[len(s.browsePrefix)-1])
	}
	return len(s.results)
}

// browseRow 由行索引构造全览结果行（零物化路径的取行单元）：行长按相邻偏移差
// 近似并扣除行尾换行（与物化路径 rune 口径在 ASCII 下对齐；多字节编码偏宽无害——
// 画布宽度是上界设计），Preview 留空由 groupedRead 惰性补全
func browseRow(e *FileEntry, li int) ResultRow {
	next := e.Size
	if li+1 < len(e.idx.offsets) {
		next = e.idx.offsets[li+1]
	}
	lineLen := 0
	if next > e.idx.offsets[li] {
		lineLen = int(next - e.idx.offsets[li])
		if li+1 < len(e.idx.offsets) && lineLen > 0 {
			lineLen-- // 中间行的偏移差含 \n，扣除对齐口径（末行不知是否有 \n，容差 1）
		}
	}
	return ResultRow{
		FileID: e.ID, LineNo: int64(li + 1), Offset: e.idx.offsets[li],
		TS: e.idx.ts[li], Level: e.idx.levels[li], LineLen: lineLen,
	}
}

// fetchRows 取全局 [start, end) 行（锁内调用）：查询模式直接切片复制；
// 零物化全览模式从行索引现翻译——全局行号经前缀和二分定位文件（前缀和单调，
// 0 行文件天然跳过），行元数据从平行数组直取
func (s *Session) fetchRows(start, end int) []ResultRow {
	if s.mode != modeBrowseVirtual {
		out := make([]ResultRow, end-start)
		copy(out, s.results[start:end])
		return out
	}
	out := make([]ResultRow, 0, end-start)
	for gi := start; gi < end; gi++ {
		row := int64(gi)
		fi := sortSearchInts64(s.browsePrefix, row) // 第一个累积行数 > row 的文件
		if fi >= len(s.browsePrefix) {
			break // 防御：行号越界（不应发生）
		}
		e := s.browseFiles[fi]
		base := int64(0)
		if fi > 0 {
			base = s.browsePrefix[fi-1]
		}
		out = append(out, browseRow(e, int(row-base)))
	}
	return out
}

// sortSearchInts64 第一个大于 v 的下标（升序数组）
func sortSearchInts64(a []int64, v int64) int {
	return sort.Search(len(a), func(i int) bool { return a[i] > v })
}

// GetResults 分页取结果窗口（虚拟滚动按窗口拉取）。
// 查询期只存偏移不生成预览；此处经 groupedRead 按 (FileID, Offset) 分组顺序读补全预览
// （时间合并序下窗口内文件不再聚簇，直接按序读会退化为随机 IO，见 groupedRead 注释）。
// 零物化全览模式同理——窗口行来自 fetchRows 翻译，预览照旧惰性补全
func (s *Session) GetResults(offset, limit int) []ResultRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := s.totalRowsLocked()
	if offset < 0 || offset >= total || limit <= 0 {
		return []ResultRow{}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	out := s.fetchRows(offset, end)

	entryById := make(map[int64]*FileEntry, len(s.entries))
	for _, e := range s.entries {
		entryById[e.ID] = e
	}
	groupedRead(out, entryById, func(e *FileEntry, i int, line []byte) {
		if out[i].Preview == "" { // 文件已被移除的行不在回调中出现，预览留空
			out[i].Preview = capLine(decodeLine(e.enc, line))
		}
	})
	return out
}

// Stats 最近一次查询统计
func (s *Session) Stats() QueryStats { return s.stats }

// GetLineText 读指定行完整原文（不截断）——右键「复制整行」用：显示层有 64KB 防御
// 截断，复制到剪贴板的应是全文（复制整段 SQL dump 场景）
func (s *Session) GetLineText(fileID, offset int64) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == fileID {
			sr := &seqReader{lf: e.lf}
			line, err := sr.line(offset)
			if err != nil {
				return "", fmt.Errorf("读取失败: %w", err)
			}
			return decodeLine(e.enc, line), nil
		}
	}
	return "", fmt.Errorf("文件已移除")
}

// GetContext 读取指定文件 offset 行的上下文
func (s *Session) GetContext(fileID, offset int64, before, after int64) ([]ContextRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == fileID {
			if offset < 0 || offset >= e.Size {
				return nil, fmt.Errorf("偏移越界")
			}
			return readContext(e.lf, e.enc, e.idx, offset, before, after), nil
		}
	}
	return nil, fmt.Errorf("文件已移除")
}

// ExportResults 导出当前结果为 txt（仅原文行）或 csv（文件,行号,时间,级别,内容）。
// 分批读行写盘（5000 行/批）：零物化全览从索引现翻译、查询模式读结果切片，两模式
// 统一流式——不再全量补全文本常驻内存，千万行导出不撑爆内存。
// 全程持读锁：导出为终端操作，期间写操作（移除/重查）排队等待
func (s *Session) ExportResults(path, format string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := s.totalRowsLocked()
	isCSV := strings.ToLower(format) == "csv"
	if !isCSV && strings.ToLower(format) != "txt" {
		return 0, fmt.Errorf("不支持的导出格式: %s", format)
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	entryById := make(map[int64]*FileEntry, len(s.entries))
	nameById := make(map[int64]string, len(s.entries))
	for _, e := range s.entries {
		entryById[e.ID] = e
		nameById[e.ID] = e.Name
	}
	if isCSV {
		if _, err := f.WriteString("\xEF\xBB\xBFfile,line,time,level,text\r\n"); err != nil {
			return 0, err
		}
	}

	const batch = 5000
	written := 0
	for start := 0; start < total; start += batch {
		end := start + batch
		if end > total {
			end = total
		}
		rows := s.fetchRows(start, end)
		texts := make([]string, len(rows))
		groupedRead(rows, entryById, func(e *FileEntry, i int, line []byte) {
			texts[i] = decodeLine(e.enc, line) // 完整行不截断
		})
		for i := range rows {
			var werr error
			if isCSV {
				ts := ""
				if rows[i].TS > 0 {
					ts = time.Unix(0, rows[i].TS).Format("2006-01-02 15:04:05.000")
				}
				_, werr = fmt.Fprintf(f, "%s,%d,%s,%s,%s\r\n",
					csvEscape(nameById[rows[i].FileID]), rows[i].LineNo, ts,
					Level(rows[i].Level).String(), csvEscape(texts[i]))
			} else {
				_, werr = f.WriteString(texts[i] + "\r\n")
			}
			if werr != nil {
				return written, werr
			}
		}
		written += len(rows)
	}
	return written, nil
}

// readSample 跨区采样为探测样本：大文件取头/中/尾三段（互不重叠），
// 小文件直接全量——避免重叠段重复计入导致命中率失真（段边界对齐换行）
func readSample(lf *lineFile) ([]byte, error) {
	const seg = int64(80 << 10)
	var offs []int64
	switch {
	case lf.size <= seg*2:
		offs = []int64{0}
	case lf.size <= seg*4:
		offs = []int64{0, lf.size - seg}
	default:
		offs = []int64{0, lf.size / 2, lf.size - seg}
	}
	pick := func(off int64) ([]byte, error) {
		end := off + seg
		if end > lf.size {
			end = lf.size
		}
		b, err := lf.readRange(off, end)
		if err != nil || len(b) == 0 {
			return nil, err
		}
		if off > 0 { // 非首段：跳过首个不完整行
			if i := bytes.IndexByte(b, '\n'); i >= 0 {
				b = b[i+1:]
			} else {
				return nil, nil
			}
		}
		if n := len(b); n > 0 && b[n-1] != '\n' { // 段尾去掉不完整行
			if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
				b = b[:i+1]
			}
		}
		return b, nil
	}
	var out []byte
	for _, off := range offs {
		b, err := pick(off)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// csvEscape CSV 字段转义：含逗号/引号/换行的字段加引号并转义内部引号
func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// capLine 防御性截断超长行（64KB，按 rune 边界截），正常日志不会触发
func capLine(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	cut := maxLineBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …[超长已截断]"
}
