package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseServer 构造 SSE 假端点：chunks 依次写出（自动加 data: 前缀与 [DONE] 结尾）
func sseServer(t *testing.T, chunks []string, check func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// deltaChunk 构造 delta 增量块
func deltaChunk(content, reasoning string, toolCalls json.RawMessage) string {
	d := map[string]any{}
	if content != "" {
		d["content"] = content
	}
	if reasoning != "" {
		d["reasoning_content"] = reasoning
	}
	if toolCalls != nil {
		d["tool_calls"] = toolCalls
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": d}}})
	return string(b)
}

func TestChatStreamTextAndReasoning(t *testing.T) {
	srv := sseServer(t, []string{
		deltaChunk("", "让我想想", nil),
		deltaChunk("支付失败", "", nil),
		deltaChunk("的根因是", "", nil),
		deltaChunk("连接超时。", "", nil),
	}, nil)
	defer srv.Close()

	c := NewClient(Endpoint{BaseURL: srv.URL, APIKey: "sk-test"})
	var text, think strings.Builder
	msg, err := c.ChatStream(context.Background(), "test-model", []Message{TextMessage("user", "hi")}, nil, "", 0,
		StreamCallbacks{
			OnDelta:     func(s string) { text.WriteString(s) },
			OnReasoning: func(s string) { think.WriteString(s) },
		})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "支付失败的根因是连接超时。" {
		t.Fatalf("正文聚合错误: %q", msg.Content)
	}
	if text.String() != msg.Content {
		t.Fatalf("OnDelta 与正文不一致: %q", text.String())
	}
	if think.String() != "让我想想" {
		t.Fatalf("思考聚合错误: %q", think.String())
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("不应有工具调用: %+v", msg.ToolCalls)
	}
}

func TestChatStreamToolCalls(t *testing.T) {
	// tool_calls 增量：index 0 的 name 首片到达、arguments 跨 3 片；index 1 完整
	srv := sseServer(t, []string{
		deltaChunk("", "", json.RawMessage(`[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":""}}]`)),
		deltaChunk("", "", json.RawMessage(`[{"index":0,"function":{"arguments":"{\"keyword\":\"timeout\"}"}}]`)),
		deltaChunk("", "", json.RawMessage(`[{"index":1,"id":"call_2","type":"function","function":{"name":"get_line","arguments":"{\"file\":\"a.log\"}"}}]`)),
	}, nil)
	defer srv.Close()

	c := NewClient(Endpoint{BaseURL: srv.URL})
	var called []ToolCall
	msg, err := c.ChatStream(context.Background(), "m", []Message{TextMessage("user", "x")}, nil, "", 0,
		StreamCallbacks{OnToolCall: func(tc ToolCall) { called = append(called, tc) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("应聚合 2 个调用: %+v", msg.ToolCalls)
	}
	tc0 := msg.ToolCalls[0]
	if tc0.ID != "call_1" || tc0.Function.Name != "search" || tc0.Function.Arguments != `{"keyword":"timeout"}` {
		t.Fatalf("调用 0 聚合错误: %+v", tc0)
	}
	if msg.ToolCalls[1].Function.Name != "get_line" {
		t.Fatalf("调用 1 聚合错误: %+v", msg.ToolCalls[1])
	}
	if len(called) != 2 {
		t.Fatalf("OnToolCall 应触发 2 次: %d", len(called))
	}
}

func TestChatOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Stream {
			t.Error("ChatOnce 应非流式")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"图里是一段堆栈"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Endpoint{BaseURL: srv.URL})
	out, err := c.ChatOnce(context.Background(), "vl", []Message{TextMessage("user", "看图")}, 512)
	if err != nil {
		t.Fatal(err)
	}
	if out != "图里是一段堆栈" {
		t.Fatalf("回复错误: %q", out)
	}
}

func TestAPIErrorPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Authentication Fails, Your api key is invalid"}}`)
	}))
	defer srv.Close()

	c := NewClient(Endpoint{BaseURL: srv.URL, APIKey: "bad"})
	_, err := c.ChatOnce(context.Background(), "m", []Message{TextMessage("user", "x")}, 0)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("应为 APIError: %v", err)
	}
	if apiErr.Status != 401 || !strings.Contains(apiErr.Error(), "api key is invalid") {
		t.Fatalf("错误未透传: %+v", apiErr)
	}
}

func TestListModels(t *testing.T) {
	// OpenAI 标准格式
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`)
	}))
	defer srv.Close()
	c := NewClient(Endpoint{BaseURL: srv.URL})
	ids, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "deepseek-chat" {
		t.Fatalf("模型列表错误: %v", ids)
	}

	// 裸数组变体
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"qwen2.5:14b"}]`)
	}))
	defer srv2.Close()
	ids2, err := NewClient(Endpoint{BaseURL: srv2.URL}).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids2) != 1 || ids2[0] != "qwen2.5:14b" {
		t.Fatalf("裸数组解析错误: %v", ids2)
	}
}

func TestAuthHeaderRules(t *testing.T) {
	var gotAuth string
	srv := sseServer(t, []string{deltaChunk("ok", "", nil)}, func(r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	})
	defer srv.Close()

	// 有 key 发 Bearer
	if _, err := NewClient(Endpoint{BaseURL: srv.URL, APIKey: "sk-1"}).
		ChatStream(context.Background(), "m", []Message{TextMessage("user", "x")}, nil, "", 0, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-1" {
		t.Fatalf("应发 Bearer: %q", gotAuth)
	}
	// 空 key 不发（Ollama/自建网关）
	if _, err := NewClient(Endpoint{BaseURL: srv.URL}).
		ChatStream(context.Background(), "m", []Message{TextMessage("user", "x")}, nil, "", 0, StreamCallbacks{}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("空 key 不应发 Authorization: %q", gotAuth)
	}
}

func TestChatStreamCancel(t *testing.T) {
	// 服务端写一片后挂住（模拟长思考），ctx 超时后客户端应返回错误
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"开头\"}}]}\n\n")
		flusher.Flush()
		<-r.Context().Done() // 挂住直到客户端断开
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := NewClient(Endpoint{BaseURL: srv.URL}).
		ChatStream(ctx, "m", []Message{TextMessage("user", "x")}, nil, "", 0, StreamCallbacks{})
	if err == nil {
		t.Fatal("ctx 取消应返回错误")
	}
}
