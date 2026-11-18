// Package client 实现 OpenAI 兼容协议的 HTTP 客户端（纯标准库，无第三方依赖、无 Wails 依赖）。
// 覆盖：多模态消息（text/image_url）、function calling（含 SSE 流式 tool_calls 增量聚合）、
// reasoning_content 思考通道（DeepSeek-R1/GLM 思考模型）、非流式单发、模型列表。
//
// 兼容面：任何实现 /chat/completions + /models 的端点（OpenAI/DeepSeek/智谱 GLM/Ollama /v1/自建网关）。
// 协议取舍（防不兼容 400，字段缺省一律不发）：
//   - effort（reasoning_effort）非全端点通用参数，空串不发；
//   - maxOut 用 max_tokens（生态事实标准字段）；OpenAI o 系认 max_completion_tokens，
//     由调用侧对这类模型传 0（不发），本包不做名字自动切换；
//   - APIKey 为空不发 Authorization header（Ollama/自建网关免鉴权）；
//   - 端点错误（含「不支持 tools」类）原样透传 body，不做二次包装。
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ---- 端点与客户端 ----

// Endpoint 端点凭据（多档案即多实例；BaseURL 填到版本根，如 https://api.deepseek.com/v1）
type Endpoint struct {
	BaseURL string
	APIKey  string // 空 = 不发 Authorization
}

// Client OpenAI 兼容客户端：Endpoint 无状态可复用，并发安全
type Client struct {
	ep   Endpoint
	http *http.Client
}

// NewClient 创建客户端。超时策略：不设整体 Timeout（流式响应可达分钟级），
// 连接超时 15s 由 Transport 承担；总时长由调用方 context 控制
func NewClient(ep Endpoint) *Client {
	return &Client{
		ep: ep,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 4,
			},
		},
	}
}

// APIError 端点返回的非 2xx 错误（Body 原样透传，调用方/用户可直接看到端点报错原因）
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 500 { // 超长错误体截断（HTML 错误页等）
		body = body[:500] + "…"
	}
	return fmt.Sprintf("端点返回 %d: %s", e.Status, body)
}

// ---- OpenAI 兼容消息模型 ----

// ContentPart 多模态内容分段（text 或 image_url）
type ContentPart struct {
	Type     string    `json:"type"` // "text" | "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 图片引用（data:image/png;base64,… 或 https URL）
type ImageURL struct {
	URL string `json:"url"`
}

// ImagePart 构造图片分段（dataURL 为 data:image/…;base64,… 形式）
func ImagePart(dataURL string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}}
}

// TextPart 构造文本分段
func TextPart(text string) ContentPart {
	return ContentPart{Type: "text", Text: text}
}

// Message 一条消息。Content 为 string（纯文本）或 []ContentPart（多模态）——
// 序列化时按实际类型输出；ToolCalls 为 assistant 消息发起的工具调用（回填历史用）
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool 时回填对应调用 ID
	Name       string     `json:"name,omitempty"`         // role=tool 时的工具名
}

// TextMessage 纯文本消息
func TextMessage(role, text string) Message {
	return Message{Role: role, Content: text}
}

// PartsMessage 多模态消息（文本 + 图片分段）
func PartsMessage(role string, parts ...ContentPart) Message {
	return Message{Role: role, Content: parts}
}

// ToolMessage 工具执行结果消息
func ToolMessage(callID, name, result string) Message {
	return Message{Role: "tool", Content: result, ToolCallID: callID, Name: name}
}

// ToolCall 一次函数调用（Function.Arguments 为 JSON 字符串——OpenAI 协议原样）
type ToolCall struct {
	Index    int          `json:"index"` // 流式增量按 index 聚合；非流式恒 0
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数名与参数
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDefinition 工具声明
type ToolDefinition struct {
	Type     string       `json:"type"` // "function"
	Function FunctionSpec `json:"function"`
}

// FunctionSpec 工具规格（Parameters 为 JSON Schema 对象）
type FunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ---- 流式回调与请求 ----

// StreamCallbacks 流式回调（全部可选；在 IO 循环中同步调用，回调内不应做重活）
type StreamCallbacks struct {
	OnDelta     func(text string) // 正文增量
	OnReasoning func(text string) // 思考过程增量（reasoning_content，模型不返回则无）
	OnToolCall  func(tc ToolCall) // 单个工具调用聚合完成时触发一次
}

// chatRequest 请求体（字段缺省不发：tools/effort/max_tokens）
type chatRequest struct {
	Model    string           `json:"model"`
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Effort   string           `json:"reasoning_effort,omitempty"`
	MaxOut   int              `json:"max_tokens,omitempty"`
}

// chatResponse 非流式响应（只取需要的字段）
type chatResponse struct {
	Choices []struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// streamChunk SSE 增量块
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatStream 流式对话：SSE 逐段回调，返回聚合完成的 assistant 消息
// （含全文 content 与聚合后的 tool_calls——调用方应将其 append 进历史再回填 tool 结果）。
// effort 空串/工具为空时对应字段不发
func (c *Client) ChatStream(ctx context.Context, model string, msgs []Message, tools []ToolDefinition, effort string, maxOut int, cb StreamCallbacks) (Message, error) {
	body, err := json.Marshal(chatRequest{
		Model: model, Messages: msgs, Tools: tools, Stream: true, Effort: effort, MaxOut: maxOut,
	})
	if err != nil {
		return Message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ep.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Message{}, c.apiError(resp)
	}

	// SSE 扫描：行缓冲上限 4MB（单 chunk 超大防御，正常远小于此）
	var out Message
	out.Role = "assistant"
	var content strings.Builder
	// tool_calls 增量按 index 聚合：id/name 首片到达，arguments 逐片追加
	tcs := make(map[int]*ToolCall)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // 注释行/空行/事件行忽略
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue // 非 JSON 数据行（个别网关的心跳等），跳过
		}
		if chunk.Error != nil {
			return Message{}, fmt.Errorf("端点流内错误: %s", chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			if d := ch.Delta.Content; d != "" {
				content.WriteString(d)
				if cb.OnDelta != nil {
					cb.OnDelta(d)
				}
			}
			if d := ch.Delta.ReasoningContent; d != "" {
				if cb.OnReasoning != nil {
					cb.OnReasoning(d)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				agg, ok := tcs[tc.Index]
				if !ok {
					cp := tc // 复制再入表（range 变量复用）
					agg = &cp
					tcs[tc.Index] = agg
				} else {
					if tc.ID != "" {
						agg.ID = tc.ID
					}
					if tc.Function.Name != "" {
						agg.Function.Name = tc.Function.Name
					}
					agg.Function.Arguments += tc.Function.Arguments
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, err
	}
	// 聚合结果按 index 稳定输出
	if len(tcs) > 0 {
		idxs := make([]int, 0, len(tcs))
		for i := range tcs {
			idxs = append(idxs, i)
		}
		for i := 0; i < len(idxs); i++ { // 小规模插入排序避免引 sort
			for j := i + 1; j < len(idxs); j++ {
				if idxs[j] < idxs[i] {
					idxs[i], idxs[j] = idxs[j], idxs[i]
				}
			}
		}
		for _, i := range idxs {
			tc := *tcs[i]
			tc.Type = "function"
			out.ToolCalls = append(out.ToolCalls, tc)
			if cb.OnToolCall != nil {
				cb.OnToolCall(tc)
			}
		}
	}
	out.Content = content.String()
	// 思考过程（reasoning_content）只走回调不入历史——回传给模型无意义且部分端点拒绝
	return out, nil
}

// ChatOnce 非流式单发（视觉中转图→文字等内部步骤用）：返回回复全文
func (c *Client) ChatOnce(ctx context.Context, model string, msgs []Message, maxOut int) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: model, Messages: msgs, Stream: false, MaxOut: maxOut,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ep.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", c.apiError(resp)
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("端点错误: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("端点返回空 choices")
	}
	return out.Choices[0].Message.Content, nil
}

// ListModels 拉取端点模型列表（配置面板下拉用）
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ep.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError(resp)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	// 部分兼容端点返回 {"models":[...]}（Ollama 新版兼容层）/裸数组——逐一兼容
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err == nil && len(out.Data) > 0 {
		ids := make([]string, 0, len(out.Data))
		for _, m := range out.Data {
			ids = append(ids, m.ID)
		}
		return ids, nil
	}
	var alt struct {
		Models []struct {
			ID   string `json:"id"`   // Ollama /v1 实际同 OpenAI 格式，此处防御扩展
			Name string `json:"name"` // 裸 {"models":[{"name":...}]} 变体
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &alt); err == nil && len(alt.Models) > 0 {
		ids := make([]string, 0, len(alt.Models))
		for _, m := range alt.Models {
			if m.ID != "" {
				ids = append(ids, m.ID)
			} else if m.Name != "" {
				ids = append(ids, m.Name)
			}
		}
		return ids, nil
	}
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		ids := make([]string, 0, len(arr))
		for _, m := range arr {
			ids = append(ids, m.ID)
		}
		return ids, nil
	}
	return nil, fmt.Errorf("无法解析模型列表响应")
}

// setHeaders 公共请求头（key 空不发 Authorization——Ollama/自建网关免鉴权）
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.ep.APIKey)
	}
}

// apiError 非 2xx 转为 APIError（body 原样透传）
func (c *Client) apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return &APIError{Status: resp.StatusCode, Body: string(body)}
}
