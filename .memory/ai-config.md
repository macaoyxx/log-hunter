# AI 配置保存与设置页草稿逻辑

## 是什么 / 规则

| 事项 | 规则 |
|---|---|
| 能力字段预填（ctx / vision / maxOut） | 只在条目**诞生时**发生一次：拉取到新名（`conf.Prefill`）、手填模型名敲定（绑定 `AiLookupCaps`，只补空、只勾上，空名提交不消耗机会） |
| 保存路径 | `SetAiConfig` 只规整落盘，**不按模型名重算任何能力字段**——保存所见即所得 |
| 「零值 = 未指定」 | 只对数字字段成立。`Vision` 是 bool，false 与「未指定」同值，不能拿零值判断「用户没改过」 |
| 设置页草稿 | 打开时重拉后端配置；草稿已有编辑则整体不替换，只把没动过的授权目录跟随最新（授权是诊断中后端直写盘的） |
| 数字输入框 | 不用 `v-model.number`（清空后留下空串 `''`）；统一经 `toNum` 归一，清空 = 留空 = 用全局默认 |
| 视觉通道路由 | 贴图是否直发主模型，看主模型条目的 `vision` 标记；中转端点只认 `visionProfile + visionModel`，**不看**视觉模型自己的标记 |
| 视觉通道下拉 | 只列所选视觉厂商的视觉模型；切换厂商时不在新厂商视觉列表的值一律清空 |
| 引用自愈 | 选项变化后，只在「该值此前有效」时清空（看 watch 回调的 `prev`）；加载、重拉导致的选项重算不改任何值 |
| 存量悬空视觉模型 | 通道指向未勾视觉的条目时不清值，下拉兜底选项按实际情况标注「未勾视觉」或「不在该厂商」 |

## 关键文件

- `internal/ai/conf/caps.go:81` —— `Prefill`：预填的唯一入口（命中能力表填数字，未命中只按关键词勾 vision）
- `ai.go:123` —— `SetAiConfig`：只规整落盘（注释写明为什么不能在保存里重算）
- `ai.go:132` —— `AiLookupCaps`：手填名敲定时查能力表
- `internal/ai/conf/conf.go:323` —— `ResolveVision`：主模型是否直发图片
- `internal/ai/conf/conf.go:340` —— `VisionEndpoint`：中转端点，不校验视觉标记
- `frontend/src/components/AiSettings.vue:104` —— `nameCommitted` / `prefill`：填名预填一次
- `frontend/src/components/AiSettings.vue:127` —— `toNum`：数字输入归一
- `frontend/src/components/AiSettings.vue:210` —— `watch(visionOptions)`：只在「此前有效」时清空
- `frontend/src/components/AiSettings.vue:217` —— `onVisionVendorChange`：切换视觉厂商的显式处理
- `frontend/src/components/__tests__/AiSettings.spec.ts` —— 草稿逻辑回归用例（写法见下）
- `ai_test.go:99` —— `TestSetAiConfigKeepsUserEdits`：绑定层保存路径契约

## 为什么这么做（设计权衡 / 踩坑）

v0.4.1 实录，三段连环：

1. **保存时重算覆盖用户勾选**。`SetAiConfig` 曾把「窗口/上限均为 0」当作新增条目、保存时按模型名重算能力。而 v0.3.1 起 UI 刻意让数字留空（「空 = 用全局默认」），于是留空条目的视觉勾选每次保存都被写回能力表的判定，名字含 vl/vision 的则取消不了。测试一直是绿的——路由用例直接写 `store.Set`，绕过了保存路径。正解：预填挪到条目诞生时，保存回归纯持久化。数字字段「改过即非零」的旧假设对 bool 根本不成立。

2. **自愈逻辑在加载时改草稿**。给视觉通道加「已选值不在选项里就清空」的 watch 后，打开设置不做任何编辑就出现「草稿未保存」，保存即丢视觉通道。原因是打开时的重拉会替换草稿、选项随之重算，watch 分不清「用户改了」和「加载重算」。而旧版 bug 恰好会留下「通道指向未勾视觉条目」的存量状态，中转一直能用（`VisionEndpoint` 不看标记）。修法：只在「此前有效」时清空，存量悬空值不动并兜底显示真名。

3. **修复的修复又留了边界**。加了「此前有效」条件后，悬空值此前就无效，用户主动切换视觉厂商时被原样保留，落盘成「厂商乙 + 甲的模型」。修法：切换厂商走 `<select>` 的 `@change` 显式处理器（新厂商从事件目标读，不依赖 v-model 与 `@change` 的触发先后），不走 watch，所以加载时的重拉不会触发。

一般化的结论：**自愈、联动类逻辑必须区分「用户显式动作」与「加载/重算」**。用户动作用事件处理器；确需 watch 时用 `prev` 判断「此前有效」。

验证上的两个教训：

- 验证场景要覆盖**存量数据**。只从空配置起步会漏掉「旧 bug 留下的状态」，而修 bug 的改动最容易伤到的恰恰是踩过这个 bug 的配置。改设置页草稿逻辑前，先列「旧版本会留下的不一致状态」，逐个过一遍「打开 → 不编辑直接保存」。
- 修完才写的回归用例要做**变异验证**：逐个把修复改回出错写法，确认对应用例会失败。
- 测试的期望值必须独立拷贝：保存路径曾原地改写入参切片，共用底层数组会让失败用例假通过。

前端测试写法（`AiSettings.spec.ts`）：`createApp` 挂载 + 原生 DOM 事件（`input` 驱动 v-model 与 `@input`，`change` 驱动 `@change`、下拉与复选框）+ `vi.mock` 打桩 wailsjs 绑定，文件头 docblock 指定 happy-dom。**不引 `@vue/test-utils`**：它从 2.4.1 起任何版本都依赖 js-beautify，带 glob / minimatch 的安全通告；审计建议的 2.4.11 反而让 glob 多命中一条高危。测试经 `prebuild` 接入 `npm run build`。

## 相关

[AI 专家面板契约](.memory/ai-agent-panel.md) · [AI 取证工具体系](.memory/ai-agent-tools.md) · [AI 协议序列化契约](.memory/ai-protocol-serialization.md)
