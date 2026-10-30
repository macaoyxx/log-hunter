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
app.go              Wails 绑定层：包装 logscan.Session，进度事件、系统对话框
main.go             Wails 入口（窗口/拖拽/主题配置）
frontend/src/       Vue 3 界面（手写组件，无 UI 框架/无 router/无状态管理库）
cmd/logscan-bench/  性能验收基准
tools/genicon/      图标生成
```

- 引擎层（logscan）↔ 绑定层（app.go）↔ 前端的依赖只能单向向前。进度回调用函数参数传入（`func(p logscan.XxxProgress)`），由 app.go 转成 Wails 事件，引擎本身不感知事件。
- 前端经 `frontend/wailsjs/go/main/App` 调后端（`wails dev`/`build` 自动重新生成绑定）；事件监听 `files:progress` / `query:progress` / `wails:file-drop`，卸载时 `EventsOff`。

## 改核心引擎前必读的设计约束

这些是性能设计的一部分，改动前先理解动机（README「设计要点」）：

1. **查询与结果构建分离**：查询只收集命中偏移；预览文本由前端按窗口经 `GetResults(offset, limit)` 惰性拉取。不要在查询阶段构建结果文本。
2. **元数据索引而非全文索引**：时间/级别走内存索引（偏移/时间戳/级别平行数组）直查；无时间条件才并行全扫。
3. **分块并行读**：16MB 块、块边界对齐换行；候选行访问用顺序读游标 + 缓冲池，无随机 IO。
4. **编码**：GBK/UTF-8 自动探测；GBK 文件的关键字与正则按 GBK 字节匹配（预转换进 matcher），含 Unicode 范围类的正则不适用 GBK；高亮在 UTF-8 侧。
5. **防御上限**：单行 64KB 截断；结果上限 200 万条（超限截断）。
6. 正则一律 Go RE2（`regexp` 标准库），天然免疫灾难性回溯——禁止引入第三方正则库。
7. 无时间戳的行（多行堆栈）附属于上一条，不参与时间/级别过滤——这是预期行为不是 bug。

## 编码约定

- 注释一律中文；复杂业务逻辑写详细「为什么」注释，简单代码不注释。
- 前端不引 UI 框架，组件手写；高亮必须走 `HighlightText.vue` 的分段渲染防 HTML 注入，严禁直接 v-html 拼接日志内容。
- 大结果集靠虚拟滚动 + 分段续载，任何列表改动不得破坏这一点。

## 其他

- 远端仓库 `https://github.com/macaoyxx/log-hunter`（main 分支）；`build/bin`、`frontend/dist`、`node_modules`、`release/` 已在 `.gitignore`。
- **发版**走根目录 `release.ps1 -Version <x.y.z>`：构建 → `release/<版本>/` 拷 exe（临时文件）→ 压 zip → 删 exe，目录内最终只留 zip；产物不进 git，经 GitHub Release 上传。版本号用 git tag（如 `v0.0.1`）。
- `frontend/package.json.md5` 是 Wails 的依赖指纹缓存，勿手工编辑。
- 换应用图标后 Windows 有系统缓存，旧图残留需重建快捷方式或重启资源管理器。
