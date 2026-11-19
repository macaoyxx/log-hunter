# AI 专家面板：事件流配对与生命周期契约

## 规则

### 步骤配对（tool_call ↔ tool_result）
- 工具按调用序执行、结果按序到达 → 前端一律 **FIFO 配对**：`tool_result` 配「最早已发起、未完成的同名步骤」
- 同名步骤全部已完成时，`tool_result` 是进度详情（开始读取 / 等待授权 / 已授权…），累进最近同名卡——一次 read_source 的时间线只占一行
- 轮结束（finishStream）必须把所有未完成的 call 步骤强制置 done：事件丢失/中断不再产生永动 spinner

### 面板生命周期
- 面板首次打开后 **v-show 常驻**（App.vue `aiMounted` + `v-show="aiOpen"`）：叉叉=隐藏，组件永不销毁（直到退出）
- 所有打开入口（设置菜单 / 聚类「AI 诊断」/ 右键问 AI）先置 `aiMounted` 再置 `aiOpen`
- 右键问 AI 的 `prefill` **不得有 busy 守卫**：会话进行中也要把内容带进输入框，发送环节自会挡（此时按钮是「停止」）

## 关键文件
- `frontend/src/components/AiPanel.vue`（applyEvt 的 tool_result 分支）—— FIFO 配对与进度累进
- `frontend/src/components/AiPanel.vue`（finishStream）—— 轮末关闭未完成步骤 / 未决授权卡
- `frontend/src/App.vue`（aiMounted）—— 常驻挂载开关

## 为什么这么做（踩坑）

- **「最近同名」配对 → 永动 spinner**：并行取证提示（S3）上线后模型会一个回复批量发 N 个同名
  read_source；按最近同名配对把所有结果堆到最后一张卡，前面的卡永远等不到完成标记——
  会话结束后仍在转圈（实测 4 卡卡死 2 张）。FIFO + 轮末兜底双保险后经真模型 E2E 验证零残留。
- **v-if 销毁面板 → 三连锁 bug**：关闭丢 busy 态 → ① 事件无流式占位全被 `curStream()` 丢弃
  （"东西没了"）；② 发送绕过前端守卫撞后端「上一条请求仍在进行」；③ 重开丢关闭期间的流。
  曾实现「重开时从后端 RunningState 恢复 busy/占位/计时」的方案后整体撤销——
  v-show 不销毁严格更优：零恢复逻辑，关闭期间流式照常落位。
- 配对/阶段展示的计数单位只能有一个：曾同时显示「第 N 步」（agent 循环轮次）与
  「取证 N 步」（工具调用次数），并行批量后两数必然对不上（18 vs 11），循环号已删。

## 相关
- [引擎共享状态并发铁律](.memory/session-concurrency.md)（后端会话侧的对应约束）
- [Wails 拖放陷阱](.memory/wails-dragdrop.md)（本面板的拖入路径附件分流）
