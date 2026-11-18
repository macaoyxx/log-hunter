package conf

import "strings"

// Caps 模型能力（内置表查询结果，仅为免手输的预填值——用户可改，非权威数据）
type Caps struct {
	Ctx    int  // 上下文窗口 token
	Vision bool // 是否支持图片输入
	MaxOut int  // 单次响应上限 token（0 = 不发 max_tokens 字段，用端点默认）
}

// 内置能力表：有序规则，先中先用（具体型号在前，家族兜底在后）；
// 匹配为「模型名小写包含模式」。MaxOut==0 的条目表示该模型系对 max_tokens
// 字段敏感（OpenAI o 系认 max_completion_tokens），预填「不发」由用户自行决定
var capTable = []struct {
	pattern string
	caps    Caps
}{
	// DeepSeek
	{"deepseek-reasoner", Caps{Ctx: 131072, MaxOut: 32768}}, // 思考吃输出预算，给大
	{"deepseek-chat", Caps{Ctx: 131072, MaxOut: 8192}},
	// 智谱 GLM（-v 系视觉在前）
	{"glm-4.5v", Caps{Ctx: 131072, Vision: true, MaxOut: 8192}},
	{"glm-4v", Caps{Ctx: 131072, Vision: true, MaxOut: 8192}},
	{"glm-4.6", Caps{Ctx: 204800, MaxOut: 8192}},
	{"glm-4.5", Caps{Ctx: 204800, MaxOut: 8192}},
	{"glm-4", Caps{Ctx: 131072, MaxOut: 8192}},
	// 通义 Qwen（vl 视觉系）
	{"qwen-vl", Caps{Ctx: 131072, Vision: true, MaxOut: 8192}}, // qwen2.5-vl / qwen3-vl 家族名含 qwen…vl，见下方组合判定
	{"qwen3", Caps{Ctx: 131072, MaxOut: 8192}},
	{"qwen2.5", Caps{Ctx: 131072, MaxOut: 8192}},
	{"qwen", Caps{Ctx: 32768, MaxOut: 8192}}, // 老型号/本地量化保守值
	// OpenAI（o 系不发 max_tokens）
	{"o4-mini", Caps{Ctx: 200000, MaxOut: 0}},
	{"o3-mini", Caps{Ctx: 200000, MaxOut: 0}},
	{"o3", Caps{Ctx: 200000, MaxOut: 0}},
	{"o1", Caps{Ctx: 200000, MaxOut: 0}},
	{"gpt-4o-mini", Caps{Ctx: 131072, Vision: true, MaxOut: 16384}},
	{"gpt-4o", Caps{Ctx: 131072, Vision: true, MaxOut: 16384}},
	{"gpt-4.1", Caps{Ctx: 1048576, Vision: true, MaxOut: 32768}},
	{"gpt-5", Caps{Ctx: 400000, Vision: true, MaxOut: 128000}},
	{"gpt-4-turbo", Caps{Ctx: 131072, Vision: true, MaxOut: 4096}},
	// Google（Gemini 系全多模态）
	{"gemini", Caps{Ctx: 1048576, Vision: true, MaxOut: 8192}},
	// Anthropic
	{"claude", Caps{Ctx: 200000, Vision: true, MaxOut: 8192}},
	// 本地多模态常见名
	{"llava", Caps{Ctx: 32768, Vision: true, MaxOut: 4096}},
	{"minicpm-v", Caps{Ctx: 32768, Vision: true, MaxOut: 4096}},
}

// visionKeywords 家族级视觉判定（与 capTable 正交：名字含关键词即视觉，
// 覆盖 qwen2.5-vl-72b 这类「家族名+变体」无法逐一列举的组合）
var visionKeywords = []string{"-vl", "vl-", "vision", "4o", "gemini", "claude", "glm-4v", "glm-5v", "llava", "minicpm-v", "-v-"}

// LookupCaps 按模型名查内置能力（小写模糊匹配）。hitTable=名字命中内置能力表——
// 只有命中才返回确定的窗口/上限数字（事实数据）；未命中（自建网关自定义名等）不编造数字，
// 数字留空走全局默认回落（ResolveCtx/ResolveMaxOut），「空 = 用默认」而非把默认固化进数据。
// vision 判定与表无关：名字含 vision/vl 等关键词即勾
func LookupCaps(name string) (caps Caps, hitTable bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return Caps{}, false
	}
	for _, r := range capTable {
		if strings.Contains(n, r.pattern) {
			c := r.caps
			if !c.Vision && matchVisionKeyword(n) {
				c.Vision = true // 组合判定：qwen2.5-vl-72b 命中 qwen2.5 家族 + vl 关键词
			}
			return c, true
		}
	}
	return Caps{Vision: matchVisionKeyword(n)}, false
}

// knownModel 名字是否命中内置能力表（迁移判断用：旧版本曾把默认值代填进数据，
// 只有非已知模型的「恰等于旧默认 128K」才视为代填产物）
func knownModel(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, r := range capTable {
		if strings.Contains(n, r.pattern) {
			return true
		}
	}
	return false
}

func matchVisionKeyword(n string) bool {
	for _, k := range visionKeywords {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}
