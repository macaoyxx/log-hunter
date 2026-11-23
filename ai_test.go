package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log-hunter/internal/ai/conf"
	"log-hunter/internal/ai/diag"
	"log-hunter/internal/logscan"
)

// loadApp 构造 App 并加载一份临时日志（绑定层工具测试用：只依赖 session，无需 Wails 上下文）
func loadApp(t *testing.T, content string) *App {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "app.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{session: logscan.NewSession()}
	if _, errs, _ := a.session.AddFiles(context.Background(), []string{p}, nil); len(errs) > 0 {
		t.Fatal(errs)
	}
	t.Cleanup(func() { a.session.ClearAll() })
	return a
}

// TestToolSearchOffset 绑定层透传：search offset 窗口序号与续读提示
func TestToolSearchOffset(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("2024-01-15 10:00:00.000 INFO tick\n")
	}
	a := loadApp(t, b.String())

	out := a.toolSearch(context.Background(), `{"keyword":"tick","limit":10,"offset":10}`)
	if !strings.Contains(out, "命中 60 条") || !strings.Contains(out, "展示第 11~20 条") ||
		!strings.Contains(out, "offset=20 续读") {
		t.Fatalf("search offset 窗口提示错误: %s", out)
	}
}

// TestToolSearchPreciseCount 绑定层透传：无文本条件（纯级别）精确计数、不带 ≥ 封顶
func TestToolSearchPreciseCount(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("2024-01-15 10:00:00.000 INFO tick\n")
	}
	a := loadApp(t, b.String())

	out := a.toolSearch(context.Background(), `{"levels":[2],"limit":10}`)
	if !strings.Contains(out, "命中 100 条") {
		t.Fatalf("纯级别应精确计数 100: %s", out)
	}
	if strings.Contains(out, "≥") {
		t.Fatalf("精确计数不应带 ≥ 封顶: %s", out)
	}
}

// TestToolFollowOffset 绑定层透传：follow 首窗口截断提示与续读窗口读尽
func TestToolFollowOffset(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1500; i++ {
		fmt.Fprintf(&b, "2024-01-15 10:00:00.000 INFO req=T9 step=%d\n", i)
	}
	a := loadApp(t, b.String())

	out := a.toolFollow(context.Background(), `{"id":"req=T9"}`)
	if !strings.Contains(out, "1000 行窗口上限截断") || !strings.Contains(out, "offset=1000 续读") {
		t.Fatalf("follow 首窗口截断提示错误: %s", out)
	}

	out2 := a.toolFollow(context.Background(), `{"id":"req=T9","offset":1000}`)
	if strings.Contains(out2, "截断") {
		t.Fatalf("续读窗口读尽后不应再提示截断: %s", out2)
	}
}

// TestToolGetLineFull 绑定层透传：get_line 返回完整原文（超展示截断阈值不截）
func TestToolGetLineFull(t *testing.T) {
	long := strings.Repeat("x", 5000)
	content := "2024-01-15 10:00:00.000 INFO " + long + "\n"
	a := loadApp(t, content)

	out := a.toolGetLine(context.Background(), `{"file":"app.log","line":1}`)
	if !strings.Contains(out, "完整原文") || !strings.HasSuffix(out, long) {
		t.Fatalf("get_line 应返回完整原文: len=%d", len(out))
	}
}

// TestSetAiConfigKeepsUserEdits 绑定层保存路径：用户的能力勾选/数字必须原样落盘。
// 曾按「ctx/maxOut 全零 = 新增条目」在保存时按模型名重算能力——而留空恰是 UI 鼓励的
// 常态（「留空 = 用全局默认」），留空条目的视觉勾选每次保存都被能力表覆盖。
// 此前路由用例（TestFollowupVisionDirect）直接写 store，绕过了保存路径，所以一直是绿的
func TestSetAiConfigKeepsUserEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.json")
	store, err := conf.OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{aiConf: store, aiDiag: &diag.Diagnoser{Conf: store}}
	in := []conf.Model{
		{Name: "GLM-5.3-Flash", Vision: true},      // 数字留空 + 用户勾视觉（名字不含关键词）
		{Name: "some-vl-model"},                    // 名字含关键词 + 用户未勾：关得掉
		{Name: "gpt-4o", Ctx: 100000, MaxOut: 500}, // 用户改过的数字与判定（能力表判 vision）
		{Name: "deepseek-chat"},                    // 留空条目保存时不代填能力表数字
	}
	// 期望 = 输入原样。必须独立拷贝：保存路径曾原地改写入参切片，共用底层数组会让期望值跟着变
	want := append([]conf.Model(nil), in...)
	if err := a.SetAiConfig(conf.Conf{
		Profiles:      []conf.Profile{{ID: "p1", Name: "厂商", BaseURL: "http://x", Models: in}},
		ActiveProfile: "p1", ActiveModel: "GLM-5.3-Flash",
	}); err != nil {
		t.Fatal(err)
	}
	// 盘上重开读取：验证的是落盘结果而非进程内状态
	s2, _ := conf.OpenPath(path)
	got := s2.Get().Profiles[0].Models
	if len(got) != len(want) {
		t.Fatalf("模型数: %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: 落盘 %+v，期望 %+v", want[i].Name, got[i], want[i])
		}
	}
}

// TestAiLookupCaps 手填名预填绑定：返回条目带模型名与预填能力；空名返回空条目
func TestAiLookupCaps(t *testing.T) {
	a := &App{}
	if got := a.AiLookupCaps("gpt-4o"); got != (conf.Model{Name: "gpt-4o", Ctx: 131072, Vision: true, MaxOut: 16384}) {
		t.Errorf("命中能力表: %+v", got)
	}
	if got := a.AiLookupCaps("GLM-5.3-Flash"); got != (conf.Model{Name: "GLM-5.3-Flash"}) {
		t.Errorf("未命中应只带名字: %+v", got)
	}
	if got := a.AiLookupCaps(""); got != (conf.Model{}) {
		t.Errorf("空名: %+v", got)
	}
}
