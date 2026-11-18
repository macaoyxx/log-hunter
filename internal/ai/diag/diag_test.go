package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"log-hunter/internal/ai/client"
	"log-hunter/internal/ai/conf"
)

// sseResponse 一段 SSE 响应（chunks 依次 data:，结尾 [DONE]）
func sseResponse(w http.ResponseWriter, chunks []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		f.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	f.Flush()
}

func delta(content string, toolCalls json.RawMessage) string {
	d := map[string]any{}
	if content != "" {
		d["content"] = content
	}
	if toolCalls != nil {
		d["tool_calls"] = toolCalls
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": d}}})
	return string(b)
}

// newTestDiagnoser 构造连向假端点的编排器
func newTestDiagnoser(t *testing.T, mainURL, visionURL string, visionModel string) (*Diagnoser, *conf.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := conf.OpenPath(filepath.Join(dir, "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile := conf.Profile{ID: "p1", Name: "主", BaseURL: mainURL,
		Models: []conf.Model{{Name: "m1"}}}
	vision := conf.Profile{ID: "p2", Name: "视觉", BaseURL: visionURL,
		Models: []conf.Model{{Name: visionModel, Vision: true}}}
	if err := store.Set(conf.Conf{
		Profiles:      []conf.Profile{profile, vision},
		ActiveProfile: "p1", ActiveModel: "m1",
		VisionProfile: "p2", VisionModel: visionModel,
	}); err != nil {
		t.Fatal(err)
	}
	var events []Event
	return &Diagnoser{
		Conf:    store,
		Tools:   Toolset(dummyTool("search"), nil, nil, nil, nil, nil, nil),
		Emit:    func(e Event) { events = append(events, e) },
		DataDir: dir,
		Now:     func() time.Time { return time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local) },
	}, store
}

func dummyTool(name string) ToolFunc {
	return func(ctx context.Context, args string) string {
		return `[app.log:100] 2024-01-15 10:23:45 ERROR payment failed`
	}
}

func TestAgentLoopToolRoundtrip(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// 第一步：模型发起 search 工具调用
			sseResponse(w, []string{delta("", json.RawMessage(
				`[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"keyword\":\"payment\"}"}}]`))})
			return
		}
		// 第二步：拿到工具结果后给最终报告
		sseResponse(w, []string{delta("根因：支付超时 [行 1]", nil)})
	}))
	defer srv.Close()

	d, _ := newTestDiagnoser(t, srv.URL, "", "vl")
	s := &Session{}
	rep, err := d.Diagnose(context.Background(), s, "p1", "m1", "", Base{ResultTotal: 100,
		FileList: "app.log", Stats: "ERROR 100"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Text != "根因：支付超时 [行 1]" {
		t.Fatalf("最终回复错误: %q", rep.Text)
	}
	if calls != 2 {
		t.Fatalf("应有两步请求: %d", calls)
	}
	// 历史结构：system + 基线 + assistant(tool_calls) + tool + assistant(正文)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) != 5 {
		t.Fatalf("消息序列长度: %d", len(s.msgs))
	}
	if len(s.msgs[2].ToolCalls) != 1 || s.msgs[2].ToolCalls[0].Function.Name != "search" {
		t.Fatalf("assistant 工具调用消息错误: %+v", s.msgs[2])
	}
	if s.msgs[3].Role != "tool" || s.msgs[3].ToolCallID != "call_1" {
		t.Fatalf("tool 回填消息错误: %+v", s.msgs[3])
	}
	if s.BaseTotal != 100 {
		t.Fatal("基线快照丢失")
	}
}

func TestWriteDoc(t *testing.T) {
	d, _ := newTestDiagnoser(t, "http://unused", "", "vl")
	s := &Session{Started: d.Now()}
	p, err := s.writeDoc(d, `..\..\evil<>.md`, "# 报告")
	if err != nil {
		t.Fatal(err)
	}
	// 落点锁死报告目录 + 文件名清洗
	if !strings.HasPrefix(p, filepath.Join(d.DataDir, "reports", "diag-20261117-100000")) {
		t.Fatalf("落点越界: %s", p)
	}
	if filepath.Base(p) != "____evil__.md" && !strings.HasSuffix(filepath.Base(p), ".md") {
		t.Fatalf("文件名清洗异常: %s", filepath.Base(p))
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	// 同名加序号
	p2, _ := s.writeDoc(d, `..\..\evil<>.md`, "# again")
	if p2 == p {
		t.Fatal("同名未加序号")
	}
	if len(s.Docs) != 2 {
		t.Fatalf("Docs 清单: %v", s.Docs)
	}
}

func TestFollowupVisionRouting(t *testing.T) {
	// 视觉端点：被调时返回转述
	var visionCalled int32
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&visionCalled, 1)
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// 图片应以 image_url 分段直发视觉模型（按原始 JSON 检查——解码后是 []any）
		var parts []map[string]any
		if err := json.Unmarshal(req.Messages[1].Content, &parts); err != nil ||
			len(parts) != 1 || parts[0]["type"] != "image_url" {
			t.Errorf("视觉请求应含图片分段: %s", req.Messages[1].Content)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"（看不清）（OCR：Connection refused at 10:23:45）"}}]}`)
	}))
	defer visionSrv.Close()

	var mainCalled int32
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mainCalled, 1)
		var req struct {
			Messages []client.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// 追问消息应为纯文本（转述已内联），不含 image_url
		if len(req.Messages) >= 3 {
			if parts, ok := req.Messages[len(req.Messages)-1].Content.([]client.ContentPart); ok {
				for _, p := range parts {
					if p.Type == "image_url" {
						t.Error("纯文本主模型不应收到 image_url")
					}
				}
			}
		}
		sseResponse(w, []string{delta("收到图", nil)})
	}))
	defer mainSrv.Close()

	d, _ := newTestDiagnoser(t, mainSrv.URL, visionSrv.URL, "vl")
	s := &Session{}
	if _, err := d.Diagnose(context.Background(), s, "p1", "m1", "", Base{ResultTotal: 1, FileList: "a", Stats: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Followup(context.Background(), s, "p1", "m1", "", "看这张图", []string{"data:image/png;base64,AAAA"}, 526); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&visionCalled) == 0 {
		t.Fatal("应经视觉档案中转")
	}
	if atomic.LoadInt32(&mainCalled) != 2 {
		t.Fatalf("主端点应被调两次: %d", mainCalled)
	}
}

func TestFollowupVisionDirect(t *testing.T) {
	// 主模型 vision=true：图片直发主端点（无视觉中转）
	var visionSrvCalled int32
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&visionSrvCalled, 1)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}]}`)
	}))
	defer visionSrv.Close()
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// 追问轮的最后一条 Content 应为分段数组（text + image_url）；
		// 首轮请求的最后一条是基线文本（string），跳过检查
		var parts []map[string]any
		if err := json.Unmarshal(req.Messages[len(req.Messages)-1].Content, &parts); err == nil {
			found := false
			for _, p := range parts {
				if p["type"] == "image_url" {
					found = true
				}
			}
			if !found {
				t.Errorf("多模态主模型应直收 image_url: %s", req.Messages[len(req.Messages)-1].Content)
			}
		}
		sseResponse(w, []string{delta("ok", nil)})
	}))
	defer mainSrv.Close()

	d, store := newTestDiagnoser(t, mainSrv.URL, visionSrv.URL, "vl")
	// 主档案模型标记 vision
	c := store.Get()
	c.Profiles[0].Models = []conf.Model{{Name: "m1", Vision: true}}
	_ = store.Set(c)

	s := &Session{}
	if _, err := d.Diagnose(context.Background(), s, "p1", "m1", "", Base{ResultTotal: 1, FileList: "a", Stats: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Followup(context.Background(), s, "p1", "m1", "", "看图", []string{"data:image/png;base64,AAAA"}, 526); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&visionSrvCalled) != 0 {
		t.Fatal("多模态主模型不应走视觉中转")
	}
}

func TestFollowupImageSizeLimit(t *testing.T) {
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w, []string{delta("ok", nil)})
	}))
	defer mainSrv.Close()
	d, _ := newTestDiagnoser(t, mainSrv.URL, "", "vl")
	d.MaxImage = 16
	s := &Session{}
	if _, err := d.Diagnose(context.Background(), s, "p1", "m1", "", Base{ResultTotal: 1, FileList: "a", Stats: "1"}); err != nil {
		t.Fatal(err)
	}
	big := "data:image/png;base64," + strings.Repeat("A", 100)
	if _, err := d.Followup(context.Background(), s, "p1", "m1", "", "x", []string{big}, 0); err == nil {
		t.Fatal("超限图片应报错")
	}
}

func TestTrimHistoryRoundBoundary(t *testing.T) {
	s := &Session{}
	// system + 基线 + 3 轮（每轮 user + assistant 带 tool_calls + tool）——构造超限历史
	s.msgs = []client.Message{
		client.TextMessage("system", "sys"),
		client.TextMessage("user", strings.Repeat("基线", 100)),
	}
	for i := 0; i < 3; i++ {
		s.msgs = append(s.msgs,
			client.TextMessage("user", fmt.Sprintf("追问%d ", i)+strings.Repeat("问", 4000)),
			client.Message{Role: "assistant", Content: "", ToolCalls: []client.ToolCall{{ID: fmt.Sprintf("c%d", i), Function: client.FunctionCall{Name: "search", Arguments: "{}"}}}},
			client.ToolMessage(fmt.Sprintf("c%d", i), "search", strings.Repeat("果", 4000)),
			client.TextMessage("assistant", "答"),
		)
	}
	before := len(s.msgs)
	kept := s.trimHistory(1000) // 极小预算强制截断
	if len(kept) >= before {
		t.Fatalf("应发生截断: %d -> %d", before, len(kept))
	}
	// 头部仍是 system；末尾保留最后一轮的完整配对（user→assistant(tool_calls)→tool→assistant）
	if kept[0].Role != "system" || kept[len(kept)-1].Role != "assistant" {
		t.Fatalf("截断边界错误: 首=%s 末=%s", kept[0].Role, kept[len(kept)-1].Role)
	}
	for i, m := range kept {
		if m.Role == "tool" && (i == 0 || kept[i-1].Role != "assistant" || len(kept[i-1].ToolCalls) == 0) {
			t.Fatalf("tool 消息失去配对的 assistant(tool_calls)：位置 %d", i)
		}
	}
	// s.msgs 已被更新为截断后版本
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) != len(kept) {
		t.Fatal("会话历史未同步截断")
	}
}

// TestFollowupNoSessionColdStart 无会话直接追问：不再报「请先诊断」，
// 以纯问答开局（专家 system + 用户消息）正常走 agent 循环
func TestFollowupNoSessionColdStart(t *testing.T) {
	var called int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		sseResponse(w, []string{delta("这行是 INFO 常规日志，无异常。", nil)})
	}))
	defer srv.Close()

	d, _ := newTestDiagnoser(t, srv.URL, "", "vl")
	s := &Session{} // 未 Diagnose，无会话
	rep, err := d.Followup(context.Background(), s, "p1", "m1", "", "请分析这段日志：16:56:25 INFO xxx", nil, 0)
	if err != nil {
		t.Fatalf("无会话追问不应报错: %v", err)
	}
	if rep.Text == "" {
		t.Fatal("应返回回复")
	}
	if atomic.LoadInt32(&called) == 0 {
		t.Fatal("端点未被调用")
	}
}
