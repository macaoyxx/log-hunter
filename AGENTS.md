# LogHunter — 工作区指南

本地大日志（GB 级）快速排查工具，Wails v2 桌面应用（Windows 单文件 exe）：Go 1.26 后端 + Vue 3 / TypeScript / Vite 前端。纯本地运行，索引全内存、不落盘。详细设计见 `README.md`。

## 常用命令

```bash
wails dev                    # 开发模式（热更新）
wails build                  # 产出 build/bin/log-hunter.exe
go test ./internal/...       # 核心引擎单测（全部集中在 internal/logscan/logscan_test.go）
go run ./cmd/logscan-bench -size 1024   # 性能基准（生成 1GB 日志并计时）
go run ./tools/genicon       # 重新生成应用图标（build/appicon.png + icon.ico）
cd frontend && npm run build # 前端类型检查（vue-tsc --noEmit）+ 构建
```

前置：Go 1.25+、Node 18+、Wails CLI（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）。

## 架构与分层边界

```
internal/logscan/   核心引擎 —— 纯库，严禁 import Wails 或任何 UI 依赖
├── unzip.go        zip 解压（内容级二进制校验、临时目录生命周期）
internal/appconf/   应用配置持久化 —— 纯库：data/config.json（主题+查询历史），不含 UI
app.go              Wails 绑定层：包装 logscan.Session，进度事件、系统对话框
main.go             Wails 入口（窗口/拖拽/主题配置）
frontend/src/       Vue 3 界面（手写组件，无 UI 框架/无 router/无状态管理库）
cmd/logscan-bench/  性能验收基准
tools/              辅助工具（纯 Go，不进产物）：genicon 图标生成 / packzip 跨平台压缩（发版脚本用）
scripts/            发版脚本（bash 跨平台）：package.sh 仅本地打包 / publish.sh 带确认门发布
docs/iterations/    迭代归档（每版本一目录，收尾规范见全局 AGENTS.md 迭代文档规范）
```

- 引擎层（logscan）↔ 绑定层（app.go）↔ 前端的依赖只能单向向前。进度回调用函数参数传入（`func(p logscan.XxxProgress)`），由 app.go 转成 Wails 事件，引擎本身不感知事件。
- 前端经 `frontend/wailsjs/go/main/App` 调后端（`wails dev`/`build` 自动重新生成绑定）；事件监听 `files:progress` / `query:progress` / `wails:file-drop`，卸载时 `EventsOff`。

## 改核心引擎前必读的设计约束

这些是性能设计的一部分，改动前先理解动机（README「设计要点」）：

1. **查询与结果构建分离**：查询只收集命中偏移；预览文本由前端按窗口经 `GetResults(offset, limit)` 惰性拉取。不要在查询阶段构建结果文本。
2. **元数据索引而非全文索引**：时间/级别走内存索引（偏移/时间戳/级别平行数组）直查；无时间条件才并行全扫。
3. **分块并行读**：16MB 块、块边界对齐换行；候选行访问用顺序读游标 + 缓冲池，无随机 IO。
4. **编码**：GBK/UTF-8 自动探测；GBK 文件的关键字与正则按 GBK 字节匹配（预转换进 matcher），含 Unicode 范围类的正则不适用 GBK；高亮在 UTF-8 侧。
5. **防御上限**：单行 64KB 截断；结果上限 200 万条（超限截断）；zip 解压总量 8GB。
6. 正则一律 Go RE2（`regexp` 标准库），天然免疫灾难性回溯——禁止引入第三方正则库。
7. 无时间戳的行（多行堆栈）附属于上一条，不参与时间/级别过滤——这是预期行为不是 bug。
8. **zip 解压零残留**：解压产物落 `%TEMP%\log-hunter-unzip-*`（每 zip 一子目录），`RemoveFile` 清零引用删子目录、`ClearAll`/退出删根目录；二进制判定用内容（前 16KB 含 NUL）**不是扩展名**——轮转命名的日志（`.log.101`）必须能收。

## 编码约定

- 注释一律中文；复杂业务逻辑写详细「为什么」注释，简单代码不注释。
- 前端不引 UI 框架，组件手写；高亮必须走 `HighlightText.vue` 的分段渲染防 HTML 注入，严禁直接 v-html 拼接日志内容。标记色经 CSS 变量（`--hl-bg`/`--hl-fg`）由 `style.css` 的 `.mark-0~4` 驱动，不要在组件里硬编码色值。
- **颜色一律走 `style.css` token，双主题（夜巡 `:root` / 日间 `[data-theme='light']`）下严禁组件硬编码色值**。表面四级：`--bg`（页面）< `--bg-panel`（面板）< `--bg-elev`（组件）< `--bg-float`（浮层）——新增浮层（菜单/下拉）必须用 `--bg-float` + `--shadow-pop`；信号色板（`--lvl-N-fg/bg`、`.mark-N`）只在此处定义。
- 主题状态在 `App.vue`（三态：auto/light/dark，localStorage `lh-theme` + matchMedia），启动预设在 `main.ts`（防闪色），勿在别处操作 `data-theme`。
- 详情面板右键菜单（`ContextMenu.vue`）与格式标记槽位（`ContextPanel.vue` 内 5 槽）是纯前端状态，面板卸载即清；标记优先于查询高亮（重叠区间让位），该优先级在 `HighlightText.vue` 的区间裁剪逻辑里，改动保持。
- 大结果集靠虚拟滚动 + 分段续载，任何列表改动不得破坏这一点。

## 其他

- 远端仓库 `https://github.com/macaoyxx/log-hunter`（main 分支）；`build/bin`、`frontend/dist`、`node_modules`、`release/` 已在 `.gitignore`。
- **发版**走 `scripts/`（bash，跨平台）：`scripts/package.sh <x.y.z>` 仅本地打包（构建 ldflags 注入版本 → `release/<版本>/` 只留 zip）；`scripts/publish.sh <x.y.z>` 先打包再发布（推 tag + gh release create）。产物不进 git。
- **发布确认铁律（写死，不可绕过）**：任何发布动作（push tag / gh release create）前必须由用户在终端显式输入 yes 确认——publish.sh 内置确认门是流程的一部分，不得移除、不得新增跳过参数；**AI 也不得代替用户确认或绕过该门执行发布**。
- `frontend/package.json.md5` 是 Wails 的依赖指纹缓存，勿手工编辑。
- 换应用图标后 Windows 有系统缓存，旧图残留需重建快捷方式或重启资源管理器。
