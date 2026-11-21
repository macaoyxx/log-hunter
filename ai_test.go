package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
