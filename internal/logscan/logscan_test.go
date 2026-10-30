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

	// 时间范围：10:23:45.200 起 → 只剩 WARN/ERROR/INFO(done)（45.123 的 INFO 排除，堆栈行因无时间被排除）
	start := time.Date(2024, 1, 15, 10, 23, 45, 200000000, time.Local).UnixNano()
	stats, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}, TSStart: start}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 {
		t.Fatalf("时间过滤期望 3 条: %d", stats.Total)
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
