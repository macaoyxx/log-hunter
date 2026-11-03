# 结果表画布宽与列宽机制（虚拟滚动下的大数据展示坑）

## 规则

横向滚动**画布宽** = `FIXED_W + FILE_COL_W + max(视口宽, 当前渲染窗口最宽行 + 64px)`，随渲染窗口**实时伸缩**（可伸可缩），收缩时钳回 scrollLeft。`ResultTable.vue` 的 `canvasW`/`seenMaxPx`/`measureRenderedRows`。

**固定宽列**（文件名/行号）的 nowrap 文本必须 `min-width: 0 + overflow: hidden + text-overflow: ellipsis`，否则 width 不生效。

## 为什么这么做（三种错法全部实测坐实，勿回头）

| 错法 | 实测后果 |
|---|---|
| 按文件级 `stats.maxLineLen` 预估（v0.0.12 及更早） | agilebpm 真实日志有 120KB 离群行 → 画布封顶 20 万 px、日常行仅数百字符 → 满屏空白、滚动条 0.5% 细如发 |
| grow-only（渲染过最宽行只增不减） | 滚过一条长行后画布永久停在高位，回到日常区滚动条再也回不来（用户实测投诉） |
| 每字符步进估算（×7.2/×12）+ 长样本精化 | 精化门槛（≥400 字符样本）在普通日志上永不触发，等于没改 |

**flex 陷阱**：`.c-file`/`.c-line` 是 flex 子项且 `white-space: nowrap`，flex 项默认 `min-width: auto`（= 内容 min-content 宽）会把 CSS `width` 顶开——表现为「列宽怎么改都不生效、列后拖大段空白」，`text-overflow: ellipsis` 形同虚设。修复是加 `min-width: 0`，不是改 width 数值。

## 关键文件

- `frontend/src/components/ResultTable.vue` —— `canvasW` computed / `measureRenderedRows`（批次渲染后量 `.c-text` 实宽）/ `watch(canvasW)` 钳 scrollLeft；`.c-file`/`.c-line` 样式注释
- MAX_CANVAS_W（20 万 px）封顶语义不变：超长行横向滚到封顶即止，全文走详情面板

## 相关

[引擎并发铁律](.memory/session-concurrency.md)、[Wails 前端离线自测法](.memory/frontend-harness-testing.md)（验证这些行为的量像素方法）
