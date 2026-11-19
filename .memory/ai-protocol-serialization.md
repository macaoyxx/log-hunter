# AI 协议序列化契约与 omitempty 坑

## 是什么 / 规则

- OpenAI 兼容协议的多模态分段契约：`type:"text"` 分段必须带**非空** `text` 字段。字段缺失会被严格校验的端点直接 400「API 调用参数有误」（实测 GLM-5.3 经 LiteLLM 如此）；显式 `"text":""` 该端点接受，但属于未定义行为，不要依赖。
- `client.ContentPart.Text` 带 `omitempty`（client.go）——空串输入会被序列化成 `{"type":"text"}` **缺字段残段**，这正是坑的源头。
- 组装规则：**空文本直接跳过 text 分段**（diag.go Followup 的修复点），不要塞空串分段占位。
- 排障铁律：协议层 bug 复现必须 **dump 框架真实发出的请求体**再原样重放、逐字段二分；手写 JSON 与 Go 序列化差一个 omitempty，复现结论就会整个反掉。

## 关键文件

- `internal/ai/client/client.go:74` —— `Text` 带 omitempty，序列化契约源头
- `internal/ai/diag/diag.go:272` —— 空文本跳过分段（修复点，含坑的注释）
- `internal/ai/diag/live_e2e_test.go` —— `TestLiveFollowupEmptyText` 真端点回归用例（`LIVE_E2E=1` 触发，配置走 ai.json 副本）

## 为什么这么做（踩坑实录）

v0.3.3 实录：「只贴图不打字」必 400，带文本正常。排障绕了三圈——effort 参数、工具调用历史、视觉开关逐个怀疑又逐个被实测推翻，根因是手写复现（node 脚本）写的是 `"text":""` 显式空串（上游接受），而 Go omitempty 实际发出的是**字段缺失**（上游拒绝），十几组测试全过导致持续误判。最终给 client.go 加临时 env 开关 dump 真实请求体，`part[0] 键=type`（无 text 字段）一眼定罪。

教训两条：
1. 用户给的**复现判别句**（空文本挂 / 带文本好）是最强证据，拿到后沿该维度直接二分，不要发散；
2. 「应用必挂、手工构造却不挂」= 序列化差异，先 dump 再说话，别拿手写等价当结论。

## 相关

[AI 取证工具体系](.memory/ai-agent-tools.md) · [AI 专家面板契约](.memory/ai-agent-panel.md)
