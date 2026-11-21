package main

// 真模型端到端（默认跳过）：LIVE_E2E=1 触发。验证本轮工具改动（search 上限 500 +
// offset 续读、get_line 全文）在真实模型循环里可用——schema 加参数不破坏 agent 循环，
// 模型能实际调用 search 并完成分析。
//
// 运行：LIVE_E2E=1 go test . -run TestLiveSearchOffset -v -timeout 10m

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log-hunter/internal/ai/conf"
	"log-hunter/internal/ai/diag"
)

func TestLiveSearchOffset(t *testing.T) {
	if os.Getenv("LIVE_E2E") != "1" {
		t.Skip("真模型 E2E：设 LIVE_E2E=1 触发")
	}
	// 600 条 ERROR：前 580 条同模板噪音（E502），后 20 条为不同根因（E999）——
	// 逼模型在 search 单次拿满 500 条后，用 offset 续读剩余 100 条才能看到根因
	var b strings.Builder
	for i := 1; i <= 580; i++ {
		fmt.Fprintf(&b, "2024-01-15 10:00:%02d.000 ERROR payment failed code=E502 id=%d\n", i%60, i)
	}
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "2024-01-15 10:09:%02d.000 ERROR payment timeout code=E999 id=%d\n", i%60, i)
	}
	b.WriteString("2024-01-15 10:10:00.000 ERROR slow sql: " +
		strings.Repeat("SELECT * FROM big_table WHERE ", 200) + "x=1\n")
	a := loadApp(t, b.String())

	aiJSON := os.Getenv("LH_AI_JSON")
	if aiJSON == "" {
		aiJSON = filepath.Join("build", "bin", "data", "ai.json")
	}
	raw, err := os.ReadFile(aiJSON)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "ai.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := conf.OpenPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Get()
	if c.ActiveProfile == "" || c.ActiveModel == "" {
		t.Fatal("配置缺活动档案/模型")
	}

	calls := map[string]int{}
	d := &diag.Diagnoser{
		Conf:    store,
		Tools:   a.aiToolset(),
		DataDir: filepath.Dir(cfgPath),
		Emit: func(e diag.Event) {
			if e.Kind == "tool_call" {
				calls[e.Text]++
				t.Logf("调用 %s", e.Text)
			}
		},
		AskAuth: func(_ context.Context, _, _ string, _ bool) diag.AuthResult {
			return diag.AuthResult{Allow: true}
		},
	}
	s := &diag.Session{}
	prompt := "我已加载日志 app.log。请用 search 工具找出全部 ERROR 行（命中约 600 条，" +
		"单次返回上限 500，超出用 offset 续读穷尽），区分错误模式并定位根因，给出「取证范围」与根因假设。"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	rep, err := d.Followup(ctx, s, c.ActiveProfile, c.ActiveModel, c.Effort, prompt, nil, 0)
	if err != nil {
		t.Fatalf("agent 循环失败: %v", err)
	}
	if len(rep.Text) < 100 {
		t.Fatalf("报告过短（%d 字符），疑似未完成分析", len(rep.Text))
	}
	if calls["search"] == 0 {
		t.Fatal("未调用 search 工具——动线未走到取证")
	}
	t.Logf("工具调用: %v", calls)
	head := rep.Text
	if len(head) > 600 {
		head = head[:600] + "…"
	}
	t.Logf("报告开头:\n%s", head)
}
