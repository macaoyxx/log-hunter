package logscan

import (
	"context"
	"testing"
	"time"
)

// tsOf 测试辅助：本地时间字符串 → unixNano
func tsOf(t *testing.T, s string) int64 {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04:05.000", s, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return tm.UnixNano()
}

// bucketOf 时间戳落在哪个桶
func bucketOf(h *Histogram, ts int64) int {
	return int((ts - h.Start) / h.Step)
}

// newSessionWith 测试辅助：建会话并加载给定内容（ClearAll 释放文件句柄——注册必须
// 晚于 writeTemp 内的 t.TempDir：cleanup 是 LIFO，后注册先执行，否则 Windows 下
// TempDir 清理因文件占用失败）
func newSessionWith(t *testing.T, contents ...string) *Session {
	t.Helper()
	s := NewSession()
	paths := make([]string, len(contents))
	for i, c := range contents {
		paths[i] = writeTemp(t, c)
	}
	if _, errs, _ := s.AddFiles(context.Background(), paths, nil); len(errs) > 0 {
		t.Fatalf("加载失败: %v", errs)
	}
	t.Cleanup(s.ClearAll)
	return s
}

func allQuery(t *testing.T, s *Session) {
	t.Helper()
	ids := make([]int64, 0, len(s.entries))
	for _, e := range s.Files() {
		ids = append(ids, e.ID)
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: ids}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestNiceStep 整刻度桶宽取值与向上取整
func TestNiceStep(t *testing.T) {
	const (
		ms  = int64(time.Millisecond)
		sec = int64(time.Second)
		min = int64(time.Minute)
		hr  = int64(time.Hour)
		day = int64(24 * time.Hour)
	)
	cases := []struct {
		raw, want int64
	}{
		{1, 100 * ms},            // 亚秒一律 100ms 起步
		{99 * ms, 100 * ms},      // 不足 100ms 也取 100ms
		{101 * ms, 200 * ms},     // 超过 100ms → 200ms
		{1 * sec, 1 * sec},       // 恰整秒
		{1500 * ms, 2 * sec},     // 1.5s → 2s
		{9 * sec, 10 * sec},
		{25 * sec, 30 * sec},
		{90 * sec, 2 * min},
		{7 * min, 10 * min},
		{45 * min, 1 * hr},
		{2 * hr, 2 * hr},
		{5 * hr, 6 * hr},
		{30 * hr, 2 * day},       // 1.25d → 2d（按整天向上取整）
		{3 * day, 3 * day},
	}
	for _, c := range cases {
		if got := niceStep(c.raw); got != c.want {
			t.Errorf("niceStep(%dns)=%d want %d", c.raw, got, c.want)
		}
	}
}

// TestTimeHistogramBrowse 零物化全览：锚点继承（堆栈续行归主行桶）、级别计数、NoTS
func TestTimeHistogramBrowse(t *testing.T) {
	s := newSessionWith(t, stdLog)
	allQuery(t, s)
	if s.mode != modeBrowseVirtual {
		t.Fatalf("无条件平铺全览应为零物化模式，得到 %v", s.mode)
	}
	h := s.TimeHistogram(60)
	if !h.HasData {
		t.Fatal("应有时间分布")
	}
	if h.NoTS != 0 {
		t.Fatalf("有锚点可继承的文件不应有 NoTS，得到 %d", h.NoTS)
	}
	// 续行（at 开头，级别 unknown）继承 10:23:46.001 的桶：该桶 [4]=1、[6]=2
	b := bucketOf(h, tsOf(t, "2024-01-15 10:23:46.001"))
	if h.Buckets[b][4] != 1 || h.Buckets[b][6] != 2 {
		t.Fatalf("主行桶计数错误: ERROR=%d unknown=%d（期望 1/2，续行应锚点继承并计入未知段）",
			h.Buckets[b][4], h.Buckets[b][6])
	}
	// 全文件 6 行都进桶：总量守恒
	var total int32
	for _, c := range h.Buckets {
		total += c[0] + c[1] + c[2] + c[3] + c[4] + c[5] + c[6]
	}
	if total != 6 {
		t.Fatalf("桶内总行数应 6（4 时间行 + 2 续行），得到 %d", total)
	}
	if h.MaxTotal != 3 {
		t.Fatalf("峰值桶应为 3（主行 + 2 续行），得到 %d", h.MaxTotal)
	}
}

// TestTimeHistogramNoTimeFile 整文件无时间戳：HasData=false、行数计入 NoTS
func TestTimeHistogramNoTimeFile(t *testing.T) {
	s := newSessionWith(t, "hello world\nfoo bar\nbaz\n")
	allQuery(t, s)
	h := s.TimeHistogram(60)
	if h.HasData {
		t.Fatal("无时间文件不应有分布")
	}
	if h.NoTS != 3 {
		t.Fatalf("NoTS 应为 3，得到 %d", h.NoTS)
	}
	if len(h.Buckets) != 0 {
		t.Fatalf("不应有桶，得到 %d 个", len(h.Buckets))
	}
}

// TestTimeHistogramQueryMode 查询物化路径 + 混合时间/无时间文件（无时间文件按无文本条件全览会被并入吗——
// 无时间文件在全览下进桶方式 = NoTS；带关键字查询时无时间文件参与文本匹配，命中行无锚点 → NoTS）
func TestTimeHistogramQueryMode(t *testing.T) {
	s := newSessionWith(t, stdLog, "plain line one\nE502 in plain file\n")
	ids := make([]int64, 0)
	for _, e := range s.Files() {
		ids = append(ids, e.ID)
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: ids, Keywords: []string{"E502"}}, nil); err != nil {
		t.Fatal(err)
	}
	if s.mode != modeQuery {
		t.Fatalf("带条件查询应为物化模式，得到 %v", s.mode)
	}
	h := s.TimeHistogram(60)
	if !h.HasData {
		t.Fatal("应有时间分布")
	}
	// 命中：stdLog 的 E502 行（10:23:46.001 ERROR）+ plain 文件的 E502 行（无时间 → NoTS）
	b := bucketOf(h, tsOf(t, "2024-01-15 10:23:46.001"))
	if h.Buckets[b][4] != 1 {
		t.Fatalf("ERROR 桶计数应 1，得到 %d", h.Buckets[b][4])
	}
	if h.NoTS != 1 {
		t.Fatalf("无时间命中行应计入 NoTS=1，得到 %d", h.NoTS)
	}
}

// TestTimeHistogramMergedEqualsFlat 合并序与平铺序聚合结果一致（合计桶计数）
func TestTimeHistogramMergedEqualsFlat(t *testing.T) {
	logB := `2024-01-15 10:23:44.000 INFO  [b] b1
2024-01-15 10:23:46.500 ERROR [b] b2
2024-01-15 10:23:48.000 INFO  [b] b3
`
	s := newSessionWith(t, stdLog, logB)
	allQuery(t, s)
	flat := s.TimeHistogram(60)
	sum := func(h *Histogram) (map[int]int32, int64) {
		m := make(map[int]int32)
		for i, c := range h.Buckets {
			t2 := c[0] + c[1] + c[2] + c[3] + c[4] + c[5] + c[6]
			if t2 > 0 {
				m[i] = t2
			}
		}
		return m, int64(h.MaxTotal)
	}
	want, wantMax := sum(flat)

	s.SetMerged(true)
	merged := s.TimeHistogram(60)
	got, gotMax := sum(merged)

	if wantMax != gotMax {
		t.Fatalf("合并序 MaxTotal=%d 与平铺序 %d 不一致", gotMax, wantMax)
	}
	if len(got) != len(want) {
		t.Fatalf("非空桶数不一致: merged=%d flat=%d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("桶 %d 计数不一致: merged=%d flat=%d", i, got[i], v)
		}
	}
}

// TestTimeHistogramAfterRemove 结果作废后调用不崩、返回空
func TestTimeHistogramAfterRemove(t *testing.T) {
	s := newSessionWith(t, stdLog)
	allQuery(t, s)
	for _, e := range s.Files() {
		s.RemoveFile(e.ID)
	}
	h := s.TimeHistogram(60)
	if h == nil {
		t.Fatal("应返回空结构而非 nil")
	}
	if h.HasData || len(h.Buckets) != 0 {
		t.Fatalf("结果作废后应为空直方图，得到 %+v", h)
	}
}

// TestIndexByTime 双击直方图跳转定位：零物化与物化两路径、无匹配/早于首行边界
func TestIndexByTime(t *testing.T) {
	s := newSessionWith(t, stdLog)
	allQuery(t, s)
	// stdLog 行序：[t1 INFO, t2 WARN, t3 ERROR, (无时间续行)×2, t4 INFO]
	// 零物化路径：二分定位
	if i := s.IndexByTime(tsOf(t, "2024-01-15 10:23:46.001")); i != 2 {
		t.Fatalf("跳到 t3 应定位下标 2，得到 %d", i)
	}
	if i := s.IndexByTime(tsOf(t, "2024-01-15 10:23:44.000")); i != 0 {
		t.Fatalf("早于首行应定位下标 0，得到 %d", i)
	}
	if i := s.IndexByTime(tsOf(t, "2024-01-15 10:23:48.000")); i != -1 {
		t.Fatalf("晚于末行应返回 -1，得到 %d", i)
	}
	// 物化路径：关键字查询命中 INFO 两行（t1、t4）
	ids := make([]int64, 0)
	for _, e := range s.Files() {
		ids = append(ids, e.ID)
	}
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: ids, Keywords: []string{"INFO"}}, nil); err != nil {
		t.Fatal(err)
	}
	// t2 落在 t1 与 t4 之间 → 应跳到 t4（结果下标 1）
	if i := s.IndexByTime(tsOf(t, "2024-01-15 10:23:45.456")); i != 1 {
		t.Fatalf("物化路径跳到 t4 应定位下标 1，得到 %d", i)
	}
	if i := s.IndexByTime(tsOf(t, "2024-01-15 10:23:45.000")); i != 0 {
		t.Fatalf("物化路径早于首行应定位下标 0，得到 %d", i)
	}
}

// TestTimeHistogramParallel 多文件零物化并行聚合与总量守恒（11 个小文件跨越并行门槛）
func TestTimeHistogramParallel(t *testing.T) {
	var contents []string
	for i := 0; i < 11; i++ {
		// 每个文件 3 行，时间错开在 10:23:4x
		contents = append(contents, "2024-01-15 10:23:45.100 INFO  [f] a\n2024-01-15 10:23:45.200 WARN  [f] b\n2024-01-15 10:23:45.300 ERROR [f] c\n")
	}
	s := newSessionWith(t, contents...)
	allQuery(t, s)
	h := s.TimeHistogram(60)
	if !h.HasData {
		t.Fatal("应有时间分布")
	}
	var total int32
	for _, c := range h.Buckets {
		total += c[0] + c[1] + c[2] + c[3] + c[4] + c[5] + c[6]
	}
	if total != 33 {
		t.Fatalf("11 文件 ×3 行总量应 33，得到 %d", total)
	}
}
