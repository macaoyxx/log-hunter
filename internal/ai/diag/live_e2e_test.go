package diag

// 真模型端到端测试（默认跳过）：LIVE_E2E=1 触发，驱动物理端点跑完整 agent 循环，
// 验证 UI 层无法本地验证的动线——并行批量取证的事件序列、FIFO 配对无卡死、
// find_files/read_source 真实执行、报告产出。用真实 ai.json 的副本授权，
// 不触碰用户配置原件。
//
// 运行：
//   LIVE_E2E=1 LH_AI_JSON=build/bin/data/ai.json \
//   LH_TARGET_DIR="D:\...\bus-form" \
//   go test ./internal/ai/diag -run TestLiveAgentSource -v -timeout 12m

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log-hunter/internal/ai/conf"
)

func TestLiveAgentSource(t *testing.T) {
	if os.Getenv("LIVE_E2E") != "1" {
		t.Skip("真模型 E2E：设 LIVE_E2E=1 触发")
	}
	aiJSON := os.Getenv("LH_AI_JSON")
	if aiJSON == "" {
		aiJSON = filepath.Join("..", "..", "..", "build", "bin", "data", "ai.json")
	}
	target := os.Getenv("LH_TARGET_DIR")
	if target == "" {
		target = `D:\code\IdeaProjects\develop\ys_agilebpm\platform\code\agilebpm\bus-form`
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("目标目录不可访问: %v", err)
	}

	// ai.json 副本开仓（授权写在副本上，用户原件不动）
	tmp := t.TempDir()
	b, err := os.ReadFile(aiJSON)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	cfgPath := filepath.Join(tmp, "ai.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := conf.OpenPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.GrantDir(target); err != nil {
		t.Fatal(err)
	}
	c := store.Get()
	if len(c.Profiles) == 0 || c.ActiveProfile == "" || c.ActiveModel == "" {
		t.Fatal("配置缺活动档案/模型")
	}

	// 事件记录：FIFO 配对模拟（镜像前端逻辑）+ 计时
	start := time.Now()
	first := map[string]time.Duration{}
	calls, results := map[string]int{}, map[string]int{}
	open := map[string]int{} // 同名未完成调用数（前端 FIFO 配对的测试侧断言）
	var contentLen, reasoningLen int
	progressEvents := 0
	d := &Diagnoser{
		Conf:    store,
		DataDir: tmp,
		Emit: func(e Event) {
			el := time.Since(start)
			if _, ok := first[e.Kind]; !ok {
				first[e.Kind] = el
			}
			switch e.Kind {
			case "delta":
				contentLen += len(e.Text)
			case "reasoning":
				reasoningLen += len(e.Text)
			case "tool_call":
				calls[e.Text]++
				open[e.Text]++
				t.Logf("[%6.1fs] 调用 %s", el.Seconds(), e.Text)
			case "tool_result":
				results[e.Text]++
				if open[e.Text] > 0 {
					open[e.Text]--
				} else {
					progressEvents++ // 进度事件（开始读取/等待授权…）
				}
				detail := e.Detail
				if len(detail) > 90 {
					detail = detail[:90] + "…"
				}
				t.Logf("[%6.1fs] 结果 %s | %s", el.Seconds(), e.Text, strings.ReplaceAll(detail, "\n", " ⏎ "))
			case "doc":
				t.Logf("[%6.1fs] 文档 %s", el.Seconds(), e.Text)
			}
		},
		// 兜底自动允许（目标目录已预授权，正常不应触发；触发即说明授权判定有漏）
		AskAuth: func(_ context.Context, tgt, _ string, _ bool) AuthResult {
			t.Logf("!! AskAuth 意外触发: %s", tgt)
			return AuthResult{Allow: true}
		},
	}

	s := &Session{}
	prompt := "分析目录 " + target + ` 的源码结构：先用 find_files 找出文件名含 Form 的类，` +
		`再用 read_source 阅读其中最核心的 2~3 个文件（目录授权已给你），` +
		`最后总结模块分层与表单引擎的关键调用链（引用 文件名:行号）。`
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	rep, err := d.Followup(ctx, s, c.ActiveProfile, c.ActiveModel, c.Effort, prompt, nil, 0)
	total := time.Since(start)

	// ---- 断言 ----
	if err != nil {
		t.Fatalf("agent 循环失败: %v", err)
	}
	if len(rep.Text) < 200 {
		t.Fatalf("报告过短（%d 字符），疑似未完成分析", len(rep.Text))
	}
	totalCalls := 0
	for _, n := range calls {
		totalCalls += n
	}
	if totalCalls == 0 {
		t.Fatal("未发生任何工具调用——动线未走到取证")
	}
	for name, n := range open {
		if n > 0 {
			t.Errorf("FIFO 配对残留：%s 有 %d 个调用未收到结果（前端将永动 spinner）", name, n)
		}
	}

	t.Logf("==== 汇总 ====")
	t.Logf("总耗时 %.1fs | 首个思考 %.1fs | 首段正文 %.1fs", total.Seconds(),
		first["reasoning"].Seconds(), first["delta"].Seconds())
	t.Logf("工具调用 %d 次（%v）| 进度事件 %d | 思考 %d 字符 | 正文 %d 字符",
		totalCalls, calls, progressEvents, reasoningLen, len(rep.Text))
	head := rep.Text
	if len(head) > 400 {
		head = head[:400] + "…"
	}
	t.Logf("报告开头：\n%s", head)
}

// TestLiveFollowupEmptyText 真端点回归：空文本 + 贴图（走视觉中转）不得 400——
// ContentPart.Text 带 omitempty，空文本分段会被序列化成缺 text 字段的
// {"type":"text"} 残段，严格校验的端点直接报「API 调用参数有误」
// （v0.3.3「只贴图不打字」必触发，修复=空文本不加该分段）。
// 断言 = Followup 成功返回非空回复（修复前首个主请求即 400）。
//
//	运行：LIVE_E2E=1 LH_AI_JSON=<ai.json> \
//	  go test ./internal/ai/diag -run TestLiveFollowupEmptyText -v -timeout 6m
func TestLiveFollowupEmptyText(t *testing.T) {
	if os.Getenv("LIVE_E2E") != "1" {
		t.Skip("真模型 E2E：设 LIVE_E2E=1 触发")
	}
	aiJSON := os.Getenv("LH_AI_JSON")
	if aiJSON == "" {
		aiJSON = filepath.Join("..", "..", "..", "build", "bin", "data", "ai.json")
	}
	// ai.json 副本开仓（用户原件不动）
	b, err := os.ReadFile(aiJSON)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "ai.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := conf.OpenPath(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Get()
	if c.ActiveProfile == "" || c.ActiveModel == "" || c.VisionProfile == "" || c.VisionModel == "" {
		t.Fatal("配置缺活动档案/模型或视觉档案（空文本贴图走视觉中转，需视觉档案）")
	}

	// 测试图：标准库生成确定性 PNG（白底黑条），不依赖外部文件
	img := image.NewRGBA(image.Rect(0, 0, 320, 120))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	for x := 40; x < 280; x += 60 {
		draw.Draw(img, image.Rect(x, 30, x+30, 90), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	d := &Diagnoser{Conf: store, DataDir: filepath.Dir(cfgPath), Emit: func(e Event) {}}
	s := &Session{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep, err := d.Followup(ctx, s, c.ActiveProfile, c.ActiveModel, c.Effort, "", []string{dataURL}, 0)
	if err != nil {
		t.Fatalf("空文本+贴图不应报错（空 text 分段 omitempty 回归）: %v", err)
	}
	if len(rep.Text) == 0 {
		t.Fatal("应返回非空回复")
	}
}
