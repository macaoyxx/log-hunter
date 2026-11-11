# 时间直方图（引擎与组件）

## 是什么 / 规则

结果集按时间桶的分布图（v0.2.0 新增）：60 桶起步、桶宽整刻度（100ms~1d，`niceStepNS` 表，Kibana 同款）、每桶 7 段计数（`[0..5]`=TRACE..FATAL、`[6]`=无级别行）。桶协议是 **7 位**——mock/测试数据少写一位会 NaN。

## 关键文件

- `internal/logscan/histogram.go` —— 聚合（`TimeHistogram`）+ 时刻定位（`IndexByTime`）
- `frontend/src/components/TimeHistogram.vue` —— 渲染与交互（拖框缩放 / 悬停详情 / 双击跳转 / 清除时间）
- `app.go` —— `GetTimeHistogram` / `GetIndexByTime` 薄绑定
- `frontend/src/components/QueryBar.vue` `setTimeRangeAndSearch` —— 拖框→时间条件→立即重查的联动入口（秒精度）

## 为什么这么做（设计权衡）

- **惰性聚合**：全览零物化 2 亿行的 O(n) 扫描若并入查询主路径，会把 0s 出结果的全览拖成秒级 → 查询完成后前端单独拉取；零物化路径按文件并行（goroutine + 8 路信号量）
- **彩色只属于日志信号**：级别色堆叠（TRACE 底→FATAL 顶）；无级别行中性灰段垫底；全部文件无级别时自动退化单色总量条（峰值小字同步转中性色）
- **双击 = 跳转定位**（用户定版）：结果表滚到该时刻之后最早一条并选中；清除时间条件走「清除时间」按钮/胶囊 ×，不占双击手势

## 踩坑

- **onDragMove 漏绑模板**：pointermove 只绑了 onMove（其 dragging 分支直接 return），`dragging.x1` 永不更新 → 拖框永不触发且无报错。给 SVG 加交互时确认每个 handler 真的挂进了模板
- **setPointerCapture 对合成/注入指针抛 NotFoundError**（pointerId 不在活动指针表）会中断 onDown → try-catch 防御，失败退化为普通冒泡监听
- **全览切合并按 resultLimit 截断**（`materializeBrowseLocked`）会让直方图与结果表口径不一致 → 组件 `watch([stats, merged])` 视图切换即重拉；查询模式下重排本不改变分布，多一次聚合是可接受代价
- 峰值标注有密度门槛（桶数 ≥10 且 maxTotal ≥50），稀疏分布不标（避免到处是峰）

## 相关

[时间锚点继承](.memory/anchor-inheritance.md)
