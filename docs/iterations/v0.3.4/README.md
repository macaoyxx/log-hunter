# v0.3.4 — AI 面板空文本贴图 400 修复

- 状态：🟢 已上线
- 起止：2026-11-19 ~ 2026-11-19
- 相关提交：
  - f2e16a3 LogHunter - AI诊断 - v0.3.4 - 修复空文本贴图必报400
  - 25c2181 LogHunter - 工程 - v0.3.4 - 全仓gofmt格式归零

## 目标（为什么做）

v0.3.3 后实测 AI 面板「只贴截图不打字直接发送」必报端点 400「API 调用参数有误」，输入框带文本则一切正常。

## 范围（做了什么，清单）

- **修复**：空文本时不再生成首个 text 分段——`ContentPart.Text` 带 omitempty，空串分段被序列化成缺 text 字段的 `{"type":"text"}` 残段，严格校验的端点直接拒收；带文本行为不变
- **回归用例**：真端点 E2E `TestLiveFollowupEmptyText`（LIVE_E2E=1 门禁，ai.json 副本开仓，测试图标准库生成），空文本+贴图全链路断言成功返回
- **沉淀**：`.memory/ai-protocol-serialization.md`——text 分段必须带非空 text 字段；协议层 bug 复现必须 dump 框架真实请求体（手写 JSON 与框架序列化差一个 omitempty，结论会反）
- **工程**：全仓 gofmt 归零（5 处历史空白级偏差）

## 结果（上线状态、验证方式、遗留事项）

- 已上线 v0.3.4（tag + GitHub Release，附 zip 与 sha256）
- 验证：go test 全过；真端点 E2E 双场景（空文本 / 带文本 + 真实截图）通过；wails build 通过
- 遗留：无
