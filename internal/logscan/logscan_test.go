package logscan

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// writeTemp 写临时日志文件
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

const stdLog = `2024-01-15 10:23:45.123 INFO  [order-svc] order created id=1001
2024-01-15 10:23:45.456 WARN  [order-svc] retry queue depth=5
2024-01-15 10:23:46.001 ERROR [pay-svc] payment failed code=E502
	at com.example.Pay.call(Pay.java:42)
	at com.example.Order.create(Order.java:100)
2024-01-15 10:23:47.000 INFO  [order-svc] order done id=1001
`

func TestDetectFormatStd(t *testing.T) {
	p := &LineParser{}
	_ = p
	parser := DetectFormat([]byte(stdLog), time.Local)
	if parser.Kind != "text" {
		t.Fatalf("期望 text，得到 %s", parser.Kind)
	}
	if parser.Template != "std" {
		t.Fatalf("期望 std 模板，得到 %s", parser.Template)
	}
	if !parser.HasTime() {
		t.Fatal("应识别出时间戳")
	}
	// 无时间戳的堆栈行 ts=0；有时间行解析正确
	ts, lv := parser.ParseLine([]byte("2024-01-15 10:23:45.123 ERROR [pay-svc] boom"))
	if ts == 0 {
		t.Fatal("时间解析失败")
	}
	want := time.Date(2024, 1, 15, 10, 23, 45, 123000000, time.Local).UnixNano()
	if ts != want {
		t.Fatalf("时间解析错误: got %d want %d", ts, want)
	}
	if lv != LevelError {
		t.Fatalf("级别错误: %v", lv)
	}
}

func TestDetectFormatJSON(t *testing.T) {
	jsonLog := `{"timestamp":"2024-03-01T12:00:00.001Z","level":"ERROR","msg":"boom"}
{"timestamp":"2024-03-01T12:00:00.002Z","level":"INFO","msg":"ok"}
{"timestamp":"2024-03-01T12:00:00.003Z","level":"WARN","msg":"slow"}
`
	parser := DetectFormat([]byte(jsonLog), time.Local)
	if parser.Kind != "json" {
		t.Fatalf("期望 json，得到 %s（tsKey=%q lvKey=%q）", parser.Kind, parser.tsKey, parser.lvKey)
	}
	ts, lv := parser.ParseLine([]byte(`{"timestamp":"2024-03-01T12:00:00.001Z","level":"ERROR","msg":"boom"}`))
	if ts == 0 {
		t.Fatal("JSON 时间解析失败")
	}
	if lv != LevelError {
		t.Fatalf("JSON 级别错误: %v", lv)
	}
}

func TestDetectFormatPlain(t *testing.T) {
	parser := DetectFormat([]byte("hello world\nfoo bar\n"), time.Local)
	if parser.Kind != "plain" {
		t.Fatalf("期望 plain，得到 %s", parser.Kind)
	}
	ts, lv := parser.ParseLine([]byte("anything"))
	if ts != 0 || lv != LevelUnknown {
		t.Fatal("plain 应返回无时间无级别")
	}
}

func TestNormalizeLevel(t *testing.T) {
	cases := map[string]Level{
		"WARNING": LevelWarn, "E": LevelError, "err": LevelError,
		"CRITICAL": LevelFatal, "INF": LevelInfo, "nope": LevelUnknown,
	}
	for in, want := range cases {
		if got := normalizeLevel(in); got != want {
			t.Errorf("normalizeLevel(%q)=%v want %v", in, got, want)
		}
	}
}

func TestBuildIndexAndQuery(t *testing.T) {
	path := writeTemp(t, stdLog)
	lf, err := openLineFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	sample, _ := lf.readRange(0, lf.Size())
	parser := DetectFormat(sample, time.Local)
	idx, err := buildIndex(context.Background(), lf, parser, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 6 行（含 2 行堆栈）
	if idx.LineCount() != 6 {
		t.Fatalf("行数错误: %d", idx.LineCount())
	}
	// 第 4、5 行是堆栈，无时间
	if idx.ts[3] != 0 || idx.ts[4] != 0 {
		t.Fatal("堆栈行不应有时间戳")
	}
	if idx.levels[2] != int8(LevelError) {
		t.Fatalf("第 3 行应为 ERROR: %d", idx.levels[2])
	}
	// 行号反查
	if no := idx.LineNoByOffset(idx.offsets[3]); no != 4 {
		t.Fatalf("行号反查错误: %d", no)
	}
}

// newTestSession 构造带单文件的会话
func newTestSession(t *testing.T, content string) *Session {
	t.Helper()
	s := NewSession()
	added, errs, _ := s.AddFiles(context.Background(), []string{writeTemp(t, content)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(added) != 1 {
		t.Fatalf("应加载 1 个文件: %d", len(added))
	}
	return s
}

func TestRunQueryKeyword(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]
	stats, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs:  []int64{e.ID},
		Keywords: []string{"ERROR"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("期望 1 条命中: %d", stats.Total)
	}
	r := s.GetResults(0, 10)[0]
	if r.LineNo != 3 {
		t.Fatalf("应命中第 3 行: %d", r.LineNo)
	}
	if r.Level != int8(LevelError) {
		t.Fatal("级别应为 ERROR")
	}
}

func TestRunQueryKeywordOrExclude(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]

	// OR 语义
	stats, _ := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Keywords: []string{"pay-svc", "WARN"}, KeywordAnd: false,
	}, nil)
	if stats.Total != 2 {
		t.Fatalf("OR 查询期望 2 条: %d", stats.Total)
	}

	// AND 语义
	stats, _ = s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Keywords: []string{"ERROR", "E502"}, KeywordAnd: true,
	}, nil)
	if stats.Total != 1 {
		t.Fatalf("AND 查询期望 1 条: %d", stats.Total)
	}

	// 排除
	stats, _ = s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Keywords: []string{"order"}, Excludes: []string{"WARN"},
	}, nil)
	if stats.Total != 2 {
		t.Fatalf("排除后期望 2 条: %d", stats.Total)
	}
}

func TestRunQueryRegex(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]
	stats, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Regex: `code=E\d+`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("正则命中期望 1 条: %d", stats.Total)
	}
}

func TestRunQueryTimeAndLevel(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]

	// 时间范围：10:23:45.200 起 → 45.123 的 INFO 排除；ERROR 的 2 行堆栈续行
	// 向前继承 ERROR(46.001) 锚点一并保留 → WARN + ERROR + 堆栈×2 + INFO(done) = 5 条
	start := time.Date(2024, 1, 15, 10, 23, 45, 200000000, time.Local).UnixNano()
	stats, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}, TSStart: start}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 5 {
		t.Fatalf("时间过滤期望 5 条: %d", stats.Total)
	}

	// 级别过滤
	stats, _ = s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Levels: []int8{int8(LevelError)},
	}, nil)
	if stats.Total != 1 {
		t.Fatalf("级别过滤期望 1 条: %d", stats.Total)
	}

	// 时间 + 关键字组合
	stats, _ = s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, TSStart: start, Keywords: []string{"done"},
	}, nil)
	if stats.Total != 1 {
		t.Fatalf("组合查询期望 1 条: %d", stats.Total)
	}
}

func TestGetContext(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]
	stats, _ := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}, Keywords: []string{"ERROR"}}, nil)
	if stats.Total != 1 {
		t.Fatalf("期望 1 条: %d", stats.Total)
	}
	r := s.GetResults(0, 1)[0]
	rows, err := s.GetContext(e.ID, r.Offset, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("上下文期望 3 行: %d", len(rows))
	}
	found := false
	for _, row := range rows {
		if row.IsTarget {
			found = true
			if row.LineNo != 3 {
				t.Fatalf("目标行号错误: %d", row.LineNo)
			}
		}
	}
	if !found {
		t.Fatal("上下文中缺少目标行标记")
	}
}

func TestRemoveAndClear(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]
	if len(s.Files()) != 1 {
		t.Fatal("应加载 1 个文件")
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}, Keywords: []string{"INFO"}}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Stats().Total == 0 {
		t.Fatal("应有查询结果")
	}
	s.RemoveFile(e.ID)
	if len(s.Files()) != 0 {
		t.Fatal("移除后应无文件")
	}
	if s.Stats().Total != 0 {
		t.Fatal("移除后结果应清空")
	}

	s2 := newTestSession(t, stdLog)
	defer s2.ClearAll()
	s2.ClearAll()
	if len(s2.Files()) != 0 {
		t.Fatal("清空后应无文件")
	}
}

func TestGBKEncoding(t *testing.T) {
	gbkBytes := []byte{0xD6, 0xD0, 0xCE, 0xC4} // "中文" 的 GBK 编码
	if got := detectEncoding(gbkBytes); got != EncGBK {
		t.Fatalf("应识别 GBK: %v", got)
	}
	if got := detectEncoding([]byte("hello 中文")); got != EncUTF8 {
		t.Fatalf("应识别 UTF-8: %v", got)
	}
	s := "订单创建失败"
	enc := encodeKeyword(EncGBK, s)
	if dec := decodeLine(EncGBK, enc); dec != s {
		t.Fatalf("GBK 编解码往返失败: %q", dec)
	}
}

func TestValidateRegexPath(t *testing.T) {
	// 非法正则应报错而不是 panic
	if _, err := newTextMatcher([]string{"a"}, nil, "[invalid", true, EncUTF8); err == nil {
		t.Fatal("非法正则应返回错误")
	}
	if _, err := newTextMatcher([]string{"a"}, nil, `code=E\d+`, true, EncUTF8); err != nil {
		t.Fatal(err)
	}
}

func TestFormatKinds(t *testing.T) {
	cases := []struct {
		name, log, wantTmpl string
	}{
		{"bracket", "[2024-05-01 08:00:00] [WARN] disk almost full\n[2024-05-01 08:00:01] [INFO] ok\n[2024-05-01 08:00:02] [ERROR] fail\n", "bracket"},
		{"level-first", "ERROR 2024-05-01 08:00:00 db down\nINFO 2024-05-01 08:00:01 recovered\nERROR 2024-05-01 08:00:02 again\n", "level-first"},
		{"bracket-level-agilebpm", "2026-10-30 00:48:25 699 [] [InterRequestInterceptor.sign ][INFO][FeignRequestInterceptor.java : 94] -- refresh sign, count: 60\n2026-10-30 00:48:25 704 [] [Thread-52 ][DEBUG][JdbcTemplate.java : 667] -- Executing prepared SQL query\n2026-10-30 00:48:25 710 [] [xxl-rpc, Netty ][INFO][XxlJobExecutor.java : 224] -- regist success\n", "bracket-level"},
	}
	for _, c := range cases {
		p := DetectFormat([]byte(c.log), time.Local)
		if p.Kind != "text" || p.Template != c.wantTmpl {
			t.Errorf("%s: 期望 text/%s，得到 %s/%s", c.name, c.wantTmpl, p.Kind, p.Template)
			continue
		}
		if c.name == "bracket-level-agilebpm" {
			ts, lv := p.ParseLine([]byte("2026-10-30 00:48:25 699 [] [Thread-52 ][INFO][XxlJobExecutor.java : 224] -- ok"))
			if ts == 0 {
				t.Fatal("空格毫秒时间未解析")
			}
			want := time.Date(2026, 10, 30, 0, 48, 25, 699000000, time.Local).UnixNano()
			if ts != want {
				t.Fatalf("时间错误: got %d want %d", ts, want)
			}
			if lv != LevelInfo {
				t.Fatalf("方括号级别未识别: %v", lv)
			}
		}
	}
}

func TestMain(m *testing.M) {
	// 便于在输出中看到包名
	fmt.Println("logscan tests")
	os.Exit(m.Run())
}
func TestNormalizeTS(t *testing.T) {
	cases := map[string]string{
		"12:00:00 699":   "12:00:00.699",
		"12:00:00.699":   "12:00:00.699",
		"12:00:00,699":   "12:00:00,699",
		"12:00:00":       "12:00:00",
		"Jan 2 11:22:33": "Jan 2 11:22:33", // syslog 不受影响
	}
	for in, want := range cases {
		if got := normalizeTS(in); got != want {
			t.Errorf("normalizeTS(%q)=%q want %q", in, got, want)
		}
	}
}

func TestLevelCountsAndLens(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]
	st, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}, Keywords: []string{"order"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// stdLog 中含 order 的行：2 条 INFO（行1、行6）+ 1 条 WARN（行2 order-svc）
	if st.LevelCounts[int(LevelInfo)] != 2 || st.LevelCounts[int(LevelWarn)] != 1 {
		t.Fatalf("级别计数错误: INFO=%d WARN=%d", st.LevelCounts[int(LevelInfo)], st.LevelCounts[int(LevelWarn)])
	}
	if st.MaxLineLen <= 0 {
		t.Fatalf("最长行应 > 0: %d", st.MaxLineLen)
	}
}

func TestCapLine(t *testing.T) {
	short := "正常日志行"
	if got := capLine(short); got != short {
		t.Fatalf("短行不应截断: %q", got)
	}
	long := strings.Repeat("a", maxLineBytes+100)
	got := capLine(long)
	if len(got) > maxLineBytes+50 {
		t.Fatalf("超长行应被防御截断: %d", len(got))
	}
}

// TestRunQueryBrowseAll 无条件查询 = 浏览全部行：含无时间戳的堆栈行；
// 多文件按加入顺序拼接、各文件内行号升序
func TestRunQueryBrowseAll(t *testing.T) {
	s := NewSession()
	defer s.ClearAll()
	// 双文件：stdLog（6 行）+ 单行文件
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, stdLog), writeTemp(t, "2024-01-15 10:23:48.000 WARN [x] tail\n")}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(added) != 2 {
		t.Fatalf("应加载 2 个文件: %d", len(added))
	}
	stats, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{added[0].ID, added[1].ID}, // 条件全空 = 浏览全部
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 7 {
		t.Fatalf("全览应 7 条（6+1）: %d", stats.Total)
	}
	// 顺序：文件 1 的 6 行在前（含 2 行无时间戳堆栈），文件 2 的 1 行在后
	rows := s.GetResults(0, 10)
	if rows[0].LineNo != 1 || rows[0].FileID != added[0].ID {
		t.Fatalf("首行应为文件 1 第 1 行: %+v", rows[0])
	}
	if rows[3].TS != 0 || rows[4].TS != 0 {
		t.Fatal("堆栈行（无时间戳）也应出现在全览中")
	}
	if rows[6].LineNo != 1 || rows[6].FileID != added[1].ID {
		t.Fatalf("末行应为文件 2 第 1 行: %+v", rows[6])
	}
}

// gbkTestName 把中文名转为 GBK 字节串：模拟老 Windows 打包工具写入的非 UTF-8 文件名
func gbkTestName(t *testing.T, s string) string {
	t.Helper()
	out, err := simplifiedchinese.GBK.NewEncoder().String(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAddZip(t *testing.T) {
	dir := t.TempDir()

	// 内层 zip（真实 zip 字节）：嵌套 zip 应被二进制校验自动跳过（不递归）
	var inner bytes.Buffer
	iw := zip.NewWriter(&inner)
	w, _ := iw.Create("inner.log")
	_, _ = w.Write([]byte("inner\n"))
	_ = iw.Close()

	zipPath := filepath.Join(dir, "logs.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ = zw.Create("logs/app.log.101") // 轮转命名：验证不吃扩展名的亏
	_, _ = w.Write([]byte(stdLog))
	w, _ = zw.Create("bin/logo.png") // 二进制（PNG 头含 NUL）
	_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0x00, 0x0D, 0x0A})
	w, _ = zw.Create("empty.txt") // 空文件（不写内容）
	w, _ = zw.Create("nested.zip")
	_, _ = w.Write(inner.Bytes())
	eh, _ := zw.CreateHeader(&zip.FileHeader{Name: "../evil.txt", Method: zip.Store}) // Zip Slip
	_, _ = eh.Write([]byte("evil"))
	gh, _ := zw.CreateHeader(&zip.FileHeader{Name: gbkTestName(t, "日志.log"), Method: zip.Store}) // GBK 文件名
	_, _ = gh.Write([]byte("gbk name file\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	s := NewSession()
	defer s.ClearAll()
	added, errs, notices := s.AddFiles(context.Background(), []string{zipPath}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	// 文本条目 2 个：轮转命名日志 + GBK 中文名文件
	if len(added) != 2 {
		t.Fatalf("应加载 2 个文本文件: %d（notices=%v）", len(added), notices)
	}
	if added[0].Name != "logs.zip/logs/app.log.101" {
		t.Fatalf("显示名错误: %s", added[0].Name)
	}
	if added[1].Name != "logs.zip/日志.log" {
		t.Fatalf("GBK 文件名解码错误: %s", added[1].Name)
	}
	// 跳过提示：二进制 / 空 / 嵌套 zip / 路径非法
	joined := strings.Join(notices, "|")
	for _, want := range []string{"二进制", "空文件", "nested.zip", "evil.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("跳过提示应含 %q: %v", want, notices)
		}
	}
	// Zip Slip：解压目录之外无逃逸文件
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); !os.IsNotExist(err) {
		t.Error("Zip Slip：evil.txt 不应出现在解压目录外")
	}
	// zip 内文本可正常查询
	e := added[0]
	stats, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}, Keywords: []string{"E502"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("zip 内文本查询应命中 1 条: %d", stats.Total)
	}
	// 重复拖入同一 zip：跳过不重复加载
	added2, _, notices2 := s.AddFiles(context.Background(), []string{zipPath}, nil)
	if len(added2) != 0 || !strings.Contains(strings.Join(notices2, ""), "已加载过") {
		t.Fatalf("重复 zip 应跳过: added=%d notices=%v", len(added2), notices2)
	}
	// 临时目录生命周期：全部移除 → zip 子目录删（根保留）；清空 → 根整体删除
	root := s.tempRoot
	if root == "" {
		t.Fatal("应创建临时根目录")
	}
	sub := filepath.Join(root, "zip-1")
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("zip 子目录应存在: %v", err)
	}
	for _, e := range s.Files() {
		s.RemoveFile(e.ID)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Error("全部移除后 zip 子目录应删除")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("临时根在 ClearAll 前应保留")
	}
	s.ClearAll()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("清空后临时根应删除")
	}
}

// TestFilterCandidatesAnchor 单元级验证时间筛选的锚点继承：
// 行0 无锚点（文件开头无时间）、行2/3 继承行1 的 100、行5 继承行4 的 200
func TestFilterCandidatesAnchor(t *testing.T) {
	ix := &LineIndex{
		offsets: []int64{0, 10, 20, 30, 40, 50},
		ts:      []int64{0, 100, 0, 0, 200, 0},
		levels:  []int8{-1, -1, -1, -1, -1, -1},
	}
	// 范围 [100,100]：行1 及继承 100 的行2/3；行0 无锚点排除、行4/5 超范围
	cands := ix.filterCandidates(100, 100, nil)
	if len(cands) != 3 || cands[0] != 1 || cands[1] != 2 || cands[2] != 3 {
		t.Fatalf("锚点继承筛选错误: %v", cands)
	}
	// 范围 [150,250]：行4 及继承 200 的行5
	cands = ix.filterCandidates(150, 250, nil)
	if len(cands) != 2 || cands[0] != 4 || cands[1] != 5 {
		t.Fatalf("尾部继承筛选错误: %v", cands)
	}
	// 锚点只影响时间判断：级别条件仍排除无级别行（语义不变）
	cands = ix.filterCandidates(0, 0, map[Level]bool{LevelInfo: true})
	if len(cands) != 0 {
		t.Fatalf("级别过滤应排除全部无级别行: %v", cands)
	}
}

// TestSetMergedTimeLine 平铺↔时间合并来回切换：两文件时间交错归并、
// 无时间文件置尾（按加入序）、切回平铺完全恢复原序
func TestSetMergedTimeLine(t *testing.T) {
	logA := `2024-01-15 10:00:01.000 INFO a1
2024-01-15 10:00:03.000 INFO a2
2024-01-15 10:00:04.000 INFO a3
`
	logB := `2024-01-15 10:00:02.000 INFO b1
2024-01-15 10:00:05.000 INFO b2
`
	plainDump := "select * from orders where id=1\nselect * from users\n"

	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, logA), writeTemp(t, logB), writeTemp(t, plainDump)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(added) != 3 {
		t.Fatalf("应加载 3 个文件: %d", len(added))
	}
	allIDs := []int64{added[0].ID, added[1].ID, added[2].ID}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: allIDs}, nil); err != nil {
		t.Fatal(err)
	}
	if s.stats.Total != 7 {
		t.Fatalf("全览应 7 条: %d", s.stats.Total)
	}
	if len(s.results) != 0 {
		t.Fatalf("纯全览零物化：results 应为空数组: %d", len(s.results))
	}

	// 平铺序（零物化窗口翻译）：A1,A2,A3,B1,B2,P1,P2
	flatSeq := resultSeq(s.GetResults(0, 7))

	// 切合并：从索引物化 + 时间归并 A1,B1,A2,A3,B2 + P1,P2 置尾
	s.SetMerged(true)
	want := []resultRef{
		{added[0].ID, 1}, {added[1].ID, 1}, {added[0].ID, 2},
		{added[0].ID, 3}, {added[1].ID, 2}, {added[2].ID, 1}, {added[2].ID, 2},
	}
	if got := resultSeq(s.results); !seqEqual(got, want) {
		t.Fatalf("合并序错误:\n got  %v\n want %v", got, want)
	}

	// 切回平铺：丢弃物化回零物化视图（Total 恢复全量、results 清空、翻译序复原）
	s.SetMerged(false)
	if s.stats.Total != 7 || len(s.results) != 0 {
		t.Fatalf("切回平铺应回零物化: total=%d results=%d", s.stats.Total, len(s.results))
	}
	if got := resultSeq(s.GetResults(0, 7)); !seqEqual(got, flatSeq) {
		t.Fatalf("切回平铺翻译序不一致:\n got  %v\n flat %v", got, flatSeq)
	}
}

// resultRef/resultSeq/seqEqual 结果顺序断言辅助（FileID+行号）
type resultRef struct {
	file   int64
	lineNo int64
}

func resultSeq(rows []ResultRow) []resultRef {
	out := make([]resultRef, len(rows))
	for i, r := range rows {
		out[i] = resultRef{r.FileID, r.LineNo}
	}
	return out
}

func seqEqual(a, b []resultRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSetMergedStackFollows 堆栈块不被其他节点同刻的行插散：
// 文件 C 的 ERROR(10:00:01) 带堆栈续行、文件 D 恰好也有 10:00:01 的行——
// 稳定排序下堆栈续行（继承锚点 10:00:01）紧贴主行，D 的行排在其后。
// （首尾补时间行是给格式探测凑命中率——堆栈占比过高整文件会被判 plain）
func TestSetMergedStackFollows(t *testing.T) {
	logC := `2024-01-15 10:00:00.500 INFO c0
2024-01-15 10:00:01.000 ERROR boom
	at com.example.C.call(C.java:1)
	at com.example.D.run(D.java:2)
2024-01-15 10:00:01.500 INFO c9
`
	logD := "2024-01-15 10:00:01.000 INFO d1\n"

	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, logC), writeTemp(t, logD)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !added[0].HasTime {
		t.Fatal("logC 应探测为有时间格式")
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{added[0].ID, added[1].ID}}, nil); err != nil {
		t.Fatal(err)
	}
	s.SetMerged(true)
	want := []resultRef{
		{added[0].ID, 1},                                           // c0 (00.500)
		{added[0].ID, 2}, {added[0].ID, 3}, {added[0].ID, 4},       // ERROR + 堆栈×2（同 key 稳定序不散）
		{added[1].ID, 1},                                           // D 的同刻行在其后（文件加入序）
		{added[0].ID, 5},                                           // c9 (01.500)
	}
	if got := resultSeq(s.results); !seqEqual(got, want) {
		t.Fatalf("堆栈块被插散:\n got  %v\n want %v", got, want)
	}
	// 无时间行位置归位但 TS 字段保持原值 0（时间列展示空）
	for _, r := range s.results[2:4] {
		if r.TS != 0 {
			t.Fatalf("无时间行的 TS 字段不应被改写: %+v", r)
		}
	}
}

// TestSetMergedAutoQuery 合并模式下新查询直接按合并序产出
func TestSetMergedAutoQuery(t *testing.T) {
	logA := "2024-01-15 10:00:01.000 INFO a1\n2024-01-15 10:00:03.000 INFO a2\n"
	logB := "2024-01-15 10:00:02.000 INFO b1\n"

	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, logA), writeTemp(t, logB)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	s.SetMerged(true) // 先切模式再查询
	if _, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{added[0].ID, added[1].ID}}, nil); err != nil {
		t.Fatal(err)
	}
	want := []resultRef{{added[0].ID, 1}, {added[1].ID, 1}, {added[0].ID, 2}}
	if got := resultSeq(s.results); !seqEqual(got, want) {
		t.Fatalf("合并模式查询应直接产出时间序:\n got  %v\n want %v", got, want)
	}
}

// TestGetResultsMergedPreview 合并序下窗口预览补全正确性：
// 时钟回拨文件（时间序≠offset 序）合并后按时间序展示，groupedRead 按 offset
// 分组顺序读，每行 preview 必须对应自己的内容
func TestGetResultsMergedPreview(t *testing.T) {
	// 回拨日志：第 1 行时间晚于第 2 行 → 合并序 = 行2、行1（offset 反序）
	rewind := "2024-01-15 10:00:05.000 INFO later-line\n2024-01-15 10:00:01.000 INFO early-line\n"
	logB := "2024-01-15 10:00:03.000 INFO b1\n"

	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, rewind), writeTemp(t, logB)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{added[0].ID, added[1].ID}}, nil); err != nil {
		t.Fatal(err)
	}
	s.SetMerged(true)
	rows := s.GetResults(0, 3)
	if len(rows) != 3 {
		t.Fatalf("应取 3 条: %d", len(rows))
	}
	wantTexts := []string{"early-line", "b1", "later-line"}
	for i, want := range wantTexts {
		if !strings.Contains(rows[i].Preview, want) {
			t.Fatalf("第 %d 行预览错误: 期望含 %q，得到 %q", i, want, rows[i].Preview)
		}
	}
}

// TestSkippedNoTime 时间条件跳过无时间文件的计数
func TestSkippedNoTime(t *testing.T) {
	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, stdLog), writeTemp(t, "no time line 1\nno time line 2\n")}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	allIDs := []int64{added[0].ID, added[1].ID}

	// 无时间条件：都参与（6 + 2 = 8 条），不提示跳过
	st, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: allIDs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 8 || st.SkippedNoTime != 0 {
		t.Fatalf("无时间条件应全部参与: total=%d skipped=%d", st.Total, st.SkippedNoTime)
	}

	// 有时间条件：无时间文件跳过并计数
	start := time.Date(2024, 1, 15, 10, 23, 45, 0, time.Local).UnixNano()
	st, err = s.RunQuery(context.Background(), &QuerySpec{FileIDs: allIDs, TSStart: start}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 6 || st.SkippedNoTime != 1 {
		t.Fatalf("时间条件应跳过无时间文件: total=%d skipped=%d", st.Total, st.SkippedNoTime)
	}
}

// TestCalcResultLimit 结果上限换算 clamp 三边界（下限保底/线性区/封顶）
func TestCalcResultLimit(t *testing.T) {
	cases := []struct {
		totalPhys int64
		want      int
	}{
		{0, resultLimitFloor},                    // 检测失败兜底
		{256 << 20, resultLimitFloor},            // 256MB → 换算值低于下限，保底
		{1 << 30, 1 << 30 / 4 / resultRowBytes},  // 1GB → 线性区（约 419 万）
		{4 << 30, 4 << 30 / 4 / resultRowBytes},  // 4GB → 线性区
		{16 << 30, resultLimitCap},               // 16GB → 封顶
		{64 << 30, resultLimitCap},               // 64GB → 封顶
	}
	for _, c := range cases {
		if got := calcResultLimit(c.totalPhys); got != c.want {
			t.Errorf("calcResultLimit(%d)=%d want %d", c.totalPhys, got, c.want)
		}
	}
}

// TestBrowseAllStatsMatch 零物化全览与物化路径（正则全命中）统计口径一致：
// Total / LevelCounts 严格相等；MaxLineLen 在 ASCII 内容下两种口径（字节近似/rune）一致
func TestBrowseAllStatsMatch(t *testing.T) {
	logB := "2024-01-15 10:23:48.000 WARN [x] tail\n"
	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, stdLog), writeTemp(t, logB)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	allIDs := []int64{added[0].ID, added[1].ID}

	browse, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: allIDs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	query, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: allIDs, Regex: ".*"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if browse.Total != query.Total {
		t.Fatalf("Total 不一致: 零物化=%d 物化=%d", browse.Total, query.Total)
	}
	if browse.LevelCounts != query.LevelCounts {
		t.Fatalf("LevelCounts 不一致: %v vs %v", browse.LevelCounts, query.LevelCounts)
	}
	if browse.MaxLineLen != query.MaxLineLen {
		t.Fatalf("MaxLineLen 不一致（ASCII 内容应相等）: %d vs %d", browse.MaxLineLen, query.MaxLineLen)
	}
	if browse.Truncated {
		t.Fatal("零物化全览不应截断")
	}
}

// TestBrowseGetResultsCrossFile 零物化全览的跨文件窗口翻译：
// stdLog（6 行）+ 单行文件，窗口 [4,7) 跨两文件——行号/来源/内容逐一对应
func TestBrowseGetResultsCrossFile(t *testing.T) {
	logB := "2024-01-15 10:23:48.000 WARN [x] tail\n"
	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, stdLog), writeTemp(t, logB)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{added[0].ID, added[1].ID}}, nil); err != nil {
		t.Fatal(err)
	}
	rows := s.GetResults(4, 3) // 全局第 5-7 行
	if len(rows) != 3 {
		t.Fatalf("窗口应 3 行: %d", len(rows))
	}
	// stdLog 行 5（堆栈）、行 6（INFO done）、logB 行 1（tail）
	if rows[0].FileID != added[0].ID || rows[0].LineNo != 5 || rows[0].TS != 0 {
		t.Fatalf("第 5 行应为文件 1 的无时间堆栈行: %+v", rows[0])
	}
	if rows[1].FileID != added[0].ID || rows[1].LineNo != 6 || !strings.Contains(rows[1].Preview, "order done") {
		t.Fatalf("第 6 行应为文件 1 的 INFO done: %+v", rows[1])
	}
	if rows[2].FileID != added[1].ID || rows[2].LineNo != 1 || !strings.Contains(rows[2].Preview, "tail") {
		t.Fatalf("第 7 行应为文件 2 第 1 行: %+v", rows[2])
	}
}

// TestExportBrowseAll 零物化全览导出：txt 内容 = 各文件顺序全部原文行
func TestExportBrowseAll(t *testing.T) {
	logB := "2024-01-15 10:23:48.000 WARN [x] tail\n"
	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(),
		[]string{writeTemp(t, stdLog), writeTemp(t, logB)}, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{added[0].ID, added[1].ID}}, nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "out.txt")
	n, err := s.ExportResults(p, "txt")
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("导出应 7 行: %d", n)
	}
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n")
	if len(lines) != 7 {
		t.Fatalf("导出行数错误: %d", len(lines))
	}
	if !strings.Contains(lines[2], "payment failed") || !strings.Contains(lines[6], "tail") {
		t.Fatalf("导出内容顺序错误: %v", lines)
	}
}

// TestBrowseMergeTruncate 上限内的全览合并物化截断与切回恢复
func TestBrowseMergeTruncate(t *testing.T) {
	s := newTestSession(t, stdLog) // 6 行
	defer s.ClearAll()
	e := s.Files()[0]
	s.resultLimit = 3 // 压低上限验证截断路径（真实值按内存自适应）
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	if s.stats.Total != 6 {
		t.Fatalf("零物化全览不受上限: %d", s.stats.Total)
	}
	s.SetMerged(true)
	if s.stats.Total != 3 || !s.stats.Truncated || len(s.results) != 3 {
		t.Fatalf("合并物化应截断到 3: total=%d trunc=%v rows=%d",
			s.stats.Total, s.stats.Truncated, len(s.results))
	}
	s.SetMerged(false)
	if s.stats.Total != 6 || s.stats.Truncated || len(s.results) != 0 {
		t.Fatalf("切回平铺应恢复零物化全量: total=%d trunc=%v rows=%d",
			s.stats.Total, s.stats.Truncated, len(s.results))
	}
}

// TestBuildIndexStats 建索引预计算统计正确性：级别计数与最长行（字节不含 \n）
func TestBuildIndexStats(t *testing.T) {
	s := newTestSession(t, stdLog) // 6 行：INFO×2、WARN×1、ERROR×1、无级别堆栈×2
	defer s.ClearAll()
	idx := s.Files()[0].idx
	if idx.levelCounts[LevelInfo] != 2 || idx.levelCounts[LevelWarn] != 1 || idx.levelCounts[LevelError] != 1 {
		t.Fatalf("级别计数错误: %v", idx.levelCounts)
	}
	// 最长行 = ERROR 行（"2024-01-15 10:23:46.001 ERROR [pay-svc] payment failed code=E502"，不含 \n）
	want := len("2024-01-15 10:23:46.001 ERROR [pay-svc] payment failed code=E502")
	if idx.maxLineLen != want {
		t.Fatalf("最长行错误: %d want %d", idx.maxLineLen, want)
	}
}

// TestSeqReaderHugeLine 超过 4MB 缓冲的单行（走 fallback 读路径）：读量有 16MB
// 封顶不读到文件尾（其后还有大内容也不 OOM），行内容完整返回
func TestSeqReaderHugeLine(t *testing.T) {
	// 结构：首行 5MB 无换行（超 4MB 缓冲）+ 其后 2MB 正常行——旧行为会把
	// [5MB 行首, 文件尾] 共 7MB+ 全量读入找换行；若行后是数 GB 同理爆炸
	huge := strings.Repeat("A", 5<<20)
	rest := strings.Repeat("2024-01-15 10:00:01.000 INFO tail\n", 60000)
	p := filepath.Join(t.TempDir(), "huge.log")
	os.WriteFile(p, []byte(huge+"\n"+rest), 0644)

	s := NewSession()
	defer s.ClearAll()
	added, errs, _ := s.AddFiles(context.Background(), []string{p}, nil)
	if len(errs) > 0 || len(added) != 1 {
		t.Fatalf("加载失败: %v", errs)
	}
	e := added[0]
	if e.idx.LineCount() != 60001 {
		t.Fatalf("行数错误: %d", e.idx.LineCount())
	}
	// 首行偏移 0：GetLineText 走 fallback，应完整返回 5MB（< 16MB 封顶）
	text, err := s.GetLineText(e.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != 5<<20 {
		t.Fatalf("超长行应完整返回 5MB: %d", len(text))
	}
	// GetResults 首屏预览正常（64KB 截断），不触发无界读
	s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}}, nil)
	rows := s.GetResults(0, 1)
	if len(rows) != 1 || len(rows[0].Preview) > maxLineBytes+64 {
		t.Fatalf("预览应 64KB 截断: %d", len(rows[0].Preview))
	}
}
