package logscan

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestNormalizeTemplate 归一占位：GUID/日期时间/IP/长十六进制/0x/小数/整数
func TestNormalizeTemplate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2024-01-15 10:23:45.123 ERROR pay failed", "<ts> ERROR pay failed"},
		{"2024-01-15T10:23:45 up", "<ts> up"},
		{"trace=550e8400-e29b-41d4-a716-446655440000 ok", "trace=<gid> ok"},
		{"connect 192.168.1.100:8080 fail", "connect <ip>:<n> fail"},
		{"sid=a1b2c3d4e5f60718 done", "sid=<hex> done"},
		{"id=0x1F count=42", "id=<hex> count=<n>"},
		{"cost=3.14s", "cost=<n>s"},
	}
	for _, c := range cases {
		if got := normalizeTemplate(c.in); got != c.want {
			t.Errorf("normalizeTemplate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestClusterResults 零物化全览模式下的聚类：同模板行归一组、组排序、锚点
func TestClusterResults(t *testing.T) {
	content := `2024-01-15 10:23:45.001 ERROR [pay] payment failed code=E502 id=1001
	at com.example.Pay.call(Pay.java:42)
2024-01-15 10:23:46.002 ERROR [pay] payment failed code=E502 id=1002
2024-01-15 10:23:47.003 ERROR [pay] payment failed code=E502 id=1003
2024-01-15 10:23:48.004 INFO [order] done id=2001
2024-01-15 10:23:49.005 WARN [order] retry depth=3
`
	s := newTestSession(t, content)
	defer s.ClearAll()
	e := s.Files()[0]
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	rep := s.ClusterResults(nil)
	// 4 种模板：ERROR（3 行）、堆栈、INFO、WARN
	if len(rep.Groups) != 4 {
		t.Fatalf("期望 4 组: %d", len(rep.Groups))
	}
	if rep.Sampled || rep.Participated != 6 || rep.TotalRows != 6 {
		t.Fatalf("6 行不应采样: %+v", rep)
	}
	g := rep.Groups[0]
	if g.Count != 3 || g.Level != int8(LevelError) {
		t.Fatalf("最大组应为 3 条 ERROR: count=%d level=%d", g.Count, g.Level)
	}
	if want := "<ts> ERROR [pay] payment failed code=E<n> id=<n>"; g.Template != want {
		t.Fatalf("模板 = %q, want %q", g.Template, want)
	}
	if len(g.Samples) != 3 || g.Samples[0].Idx != 0 || g.Samples[0].LineNo != 1 {
		t.Fatalf("代表行锚点错误: %+v", g.Samples)
	}
	if g.FirstTS == 0 || g.LastTS == 0 || g.FirstTS > g.LastTS {
		t.Fatalf("时间范围错误: %d~%d", g.FirstTS, g.LastTS)
	}
}

// TestClusterResultsSampling 超采样上限的均匀采样：两组计数比例应保持。
// 数据用 3 行循环（2 ERROR + 1 INFO）——与 stride=2 互质，避免严格 2 周期交替
// 被步进采样系统性跳采一类（见 clusterSampleLimit 注释的已知弱点）
func TestClusterResultsSampling(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40000; i++ {
		fmt.Fprintf(&b, "2024-01-15 10:23:45.%03d ERROR [pay] payment failed code=E502 id=%d\n", i%1000, i)
		fmt.Fprintf(&b, "2024-01-15 10:23:46.%03d ERROR [pay] payment failed code=E502 id=%d\n", i%1000, i+1)
		fmt.Fprintf(&b, "2024-01-15 10:23:47.%03d INFO [order] done id=%d\n", i%1000, i)
	}
	s := newTestSession(t, b.String())
	defer s.ClearAll()
	e := s.Files()[0]
	if _, err := s.RunQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	rep := s.ClusterResults(nil)
	if !rep.Sampled {
		t.Fatal("12 万行应触发采样")
	}
	if rep.Participated > clusterSampleLimit {
		t.Fatalf("参与数超上限: %d", rep.Participated)
	}
	if len(rep.Groups) != 2 {
		t.Fatalf("期望 2 组: %d", len(rep.Groups))
	}
	ratio := float64(rep.Groups[0].Count) / float64(rep.Groups[1].Count)
	// 数据真实比例 = 8 万 ERROR : 4 万 INFO = 2:1，采样应忠实保持（容差 ±10%）
	if ratio < 1.8 || ratio > 2.2 {
		t.Fatalf("采样后比例失真: %.2f（%d vs %d）", ratio, rep.Groups[0].Count, rep.Groups[1].Count)
	}
}

// TestSideQuery 影子查询命中正确且主结果集不受影响（agent 取证不破坏用户屏幕）
func TestSideQuery(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()
	e := s.Files()[0]

	// 主查询：ERROR 命中 1 条
	stats, err := s.RunQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Keywords: []string{"ERROR"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Fatalf("主查询应命中 1 条: %d", stats.Total)
	}

	// 影子查询：INFO 条件应命中 2 条
	rows, err := s.SideQuery(context.Background(), &QuerySpec{
		FileIDs: []int64{e.ID}, Keywords: []string{"INFO"},
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("影子查询应命中 2 条: %d", len(rows))
	}
	if rows[0].Text == "" || rows[0].File == "" || rows[0].LineNo != 1 {
		t.Fatalf("影子行字段不完整: %+v", rows[0])
	}

	// 关键断言：影子查询后主结果集不变
	if s.Stats().Total != 1 {
		t.Fatalf("主结果集被污染: %d", s.Stats().Total)
	}
	got := s.GetResults(0, 10)
	if len(got) != 1 || got[0].Level != int8(LevelError) {
		t.Fatalf("主结果内容被污染: %+v", got)
	}
	// 主结果集聚类仍只有 1 行（ERROR 模板）
	rep := s.ClusterResults(nil)
	if rep.TotalRows != 1 || len(rep.Groups) != 1 {
		t.Fatalf("聚类应基于 1 条主结果: %+v", rep)
	}

	// limit 钳制：超上限收敛到 50
	all, err := s.SideQuery(context.Background(), &QuerySpec{FileIDs: []int64{e.ID}}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) > sideQueryMaxLimit {
		t.Fatalf("limit 未钳制: %d", len(all))
	}
}

// TestSideCluster 全局级别聚类：级别过滤生效、锚点 -1（不在结果集）
func TestSideCluster(t *testing.T) {
	s := newTestSession(t, stdLog)
	defer s.ClearAll()

	rep, err := s.SideCluster(context.Background(), nil, []int8{int8(LevelError)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// stdLog 中 ERROR 1 行（堆栈行无级别，不在候选）
	if len(rep.Groups) != 1 || rep.Groups[0].Count != 1 {
		t.Fatalf("ERROR 聚类应 1 组 1 行: %+v", rep.Groups)
	}
	if rep.Groups[0].FirstIdx != -1 {
		t.Fatalf("SideCluster 锚点应为 -1: %d", rep.Groups[0].FirstIdx)
	}
	if rep.Groups[0].Samples[0].File == "" || rep.Groups[0].Samples[0].LineNo != 3 {
		t.Fatalf("样本行引用错误: %+v", rep.Groups[0].Samples[0])
	}

	// 无级别过滤：全部 6 行参与
	rep2, err := s.SideCluster(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Participated != 6 || rep2.Sampled {
		t.Fatalf("全量聚类应 6 行无采样: %+v", rep2)
	}
}
