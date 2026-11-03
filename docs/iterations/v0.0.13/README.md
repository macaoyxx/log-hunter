# v0.0.13 — UI 全面重构「指挥舱」+ 主题系统
- 状态：🟡 进行中
- 起止：2026-11-03 ~ 进行中
- 相关提交：
  - （待提交）

## 目标（为什么做）
v0.0.12 的「碳晶·磷光」界面被全盘否定：布局层级乱、操作路径深、结果表观感差、状态反馈弱四项全中。项目即将开源分发，需要一套现代、专业、有辨识度的 UI。经多轮可视化方案对比定稿「指挥舱 v2 纯版」（Linear/Raycast 冷峻效率派），并新增主题系统（外观三态 × 5 套主题色）。

## 范围（做了什么，清单）
- **指挥舱布局**：左侧指挥轨（作战概览卡 / 文件滚动列表 / 级别频谱面板 / 状态卡，可收起 236→54px，收起态文件缩写徽章）+ 指令条（查询条件全 chips 化一触即达）+ 结果表 + IDE 状态栏；查询历史移回搜索框时钟下拉；设置单一入口在最右端（二级弹层：一级 主题/检查更新/关于，点主题进二级出外观与主题色）
- **文件管理三件套**：侧轨 ＋添加 / 清空按钮、文件卡 hover ×移除单个；勾选=参与查询（变化即实时重查，清零=清结果）、移除=清索引两层语义分离
- **主题系统**：外观三态（跟随系统/夜巡/日间）× 5 套主题色（冰蓝/极光青/磷光绿/铂金/暖琥珀），设置弹层即时切换；config.json 升级为 `settings.theme = {appearance, palette}`（旧 `theme` 字段读时自动迁移），后续设置项均在 settings 下扩展
- **结果表新视觉**：文件名中段省略保留首尾、ERROR/FATAL 行左缘色条、双击行开右侧详情面板（±N 上下文、放大、复制走后端、↑↓ 走命中）；导出合并为单按钮下拉（TXT/CSV）
- **实时内存**：指挥轨状态卡 2s 轮询 App 进程工作集 + 系统已用/总量（新绑定 `MemUsage`，psapi 直调取工作集、GlobalMemoryStatusEx 取系统），两行 nowrap 防挤乱
- **扫描反馈**：进度条 + 逐行落格 + 骨架屏（纯版无雷达动效），尊重 prefers-reduced-motion
- **字体与图标**：自托管 Inter + JetBrains Mono（latin，OFL），中文走系统栈；emoji 图标全部替换为手写内联 SVG 图标集（零依赖离线）
- **style.css token 体系重建**：冷黑基底 + 面板顶缘微光 + 5 palette × 双主题全 token 化

## 影响面
- `data/config.json` 结构升级：顶层 `theme` 字符串 → `settings.theme` 对象（读时自动迁移旧格式并回写，无感升级）；前端绑定 `SetTheme` → `SetThemeSettings`
- localStorage 新增 `lh-palette`；`<html>` 新增 `data-palette` 属性
- 前端组件结构变更：FileBar → SideRail；新增 ThemePopover；QueryBar/ResultTable/ContextPanel/ContextMenu/TimePicker 大改
- 引擎层（logscan）零改动；全部行为红线保留（虚拟滚动槽位复用/画布封顶/防注入/OnFileDrop/后端复制等）

## 结果
- 编码完成，全链路验证绿：`go test ./internal/...`（appconf 新增旧格式迁移/非法回落/全新安装不建盘用例）；`npm run build`（vue-tsc + vite，字体入产物）；`wails build` 通过
- 待办：`wails dev` 真机走查（拖拽/勾选重查/主题切换/重启偏好恢复/大结果滚动性能）→ 用户确认后提交

