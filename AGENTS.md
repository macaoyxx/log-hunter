# LogHunter — 工作区指南

本地大日志（GB 级）快速排查工具，Wails v2 桌面应用（Windows 单文件 exe）：Go 1.26 后端 + Vue 3 / TypeScript / Vite 前端。纯本地运行，索引全内存、不落盘。详细设计见 `README.md`。

## 常用命令

```bash
wails dev                    # 开发模式（热更新）
wails build                  # 产出 build/bin/log-hunter.exe
go test ./internal/...       # 单测（logscan 引擎 + update 更新器 + appconf 配置）
go run ./cmd/logscan-bench -size 1024   # 性能基准（生成 1GB 日志并计时）
go run ./cmd/stress-bench -mode multi   # 压力基准（20GB 双模式：单文件/10 文件，耗时+内存采样，数据可复用）
go run ./tools/genicon       # 重新生成应用图标（build/appicon.png + icon.ico）
cd frontend && npm run build # 前端类型检查（vue-tsc --noEmit）+ 构建
```

新迭代开始时：切分支 `v<版本号>`、建迭代文档、并 bump `main.go` 的 `var version = "<版本号>-dev"`（发版由 release 脚本 ldflags 覆盖为正式号）。

前置：Go 1.25+、Node 18+、Wails CLI（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）。

## 架构与分层边界

```
internal/logscan/   核心引擎 —— 纯库，严禁 import Wails 或任何 UI 依赖
├── merge.go        时间合并排序（锚点 key+索引置换）与分组顺序读
├── resultlimit.go  结果上限内存自适应（Windows 物理内存查询）
├── unzip.go        zip 解压（内容级二进制校验、嵌套 zip 递归≤3 层、临时目录生命周期与启动清扫）
internal/appconf/   应用配置持久化 —— 纯库：data/config.json（主题+查询历史），不含 UI
internal/update/    应用内自动更新 —— 纯库：下载（进度回调）/sha256 校验/解压/rename 自替换
app.go              Wails 绑定层：包装 logscan.Session，进度事件、系统对话框、剪贴板、更新编排
main.go             Wails 入口（窗口/拖拽/主题配置/启动埋点）
frontend/src/       Vue 3 界面（手写组件，无 UI 框架/无 router/无状态管理库）
cmd/logscan-bench/  性能验收基准
cmd/stress-bench/   20GB 压力基准（双模式，全环节耗时+内存采样）
tools/              辅助工具（纯 Go，不进产物）：genicon 图标生成 / packzip 跨平台压缩（发版脚本用）
scripts/            发版脚本（bash 跨平台）：package.sh 仅本地打包（附 sha256）/ publish.sh 带确认门发布
docs/design.md      技术设计基线（架构/算法/实测数据）——改引擎前先读
docs/iterations/    迭代归档（每版本一目录，收尾规范见全局 AGENTS.md 迭代文档规范）
```

- 引擎层（logscan）↔ 绑定层（app.go）↔ 前端的依赖只能单向向前。进度回调用函数参数传入（`func(p logscan.XxxProgress)`），由 app.go 转成 Wails 事件，引擎本身不感知事件。
- 前端经 `frontend/wailsjs/go/main/App` 调后端（`wails dev`/`build` 自动重新生成绑定）；**生成绑定一律返回 Promise（含单值返回），前端一律 .then/await**，fire-and-forget 调用记得 `.catch(() => {})`。事件监听 `files:progress` / `query:progress`，卸载时 `EventsOff`。
- **文件拖放必须走 `OnFileDrop(cb, false)` 注册**（App.vue），直接 `EventsOn('wails:file-drop')` 链路不通（曾坏两个版本），机制详见 `.memory/wails-dragdrop.md`。

## 改核心引擎前必读的设计约束

这些是性能设计的一部分，改动前先理解动机（详细原理与实测数据见 `docs/design.md`）：

1. **查询与结果构建分离**：查询只收集命中偏移；预览文本由前端按窗口经 `GetResults(offset, limit)` 惰性拉取。不要在查询阶段构建结果文本。
2. **元数据索引而非全文索引**：时间/级别走内存索引（偏移/时间戳/级别平行数组，17B/行）直查；无时间条件才并行全扫。级别计数/最长行在 `buildIndex` 顺带预计算——**不要在查询期重扫全行算统计**。
3. **结果三态**（session.mode）：`modeQuery`（条件查询物化）/ `modeBrowseVirtual`（无条件+平铺的全览**零物化**——不存结果数组，GetResults 按前缀和二分从索引现翻译，永不截断）/ `modeBrowseMat`（全览切合并时按需物化）。零物化的翻译与排序 key 计算都依赖「结果按文件连续、行号升序」的平铺序假设——破坏该假设的改动会引入错乱。
4. **时间锚点继承是统一语义**：无时间戳的行（堆栈/SQL 续行）向前继承同文件上一条非零时间戳——`filterCandidates` 的时间筛选与 `sortMerged` 的排序 key 用同一规则，两处必须一起改。锚点扫描必须「进度/锚点双指针」（进度每行无条件推进）——只在遇非零时推进会遇大段无时间区退化成每行重扫（实测 782 亿次内层扫描、46.5s 卡死）。
5. **排序一律排索引 + 置换环**（`merge.go` 的 `permuteStable`）：`sort.Stable` 的 Swap 是整结构体交换（64B/次），百万级数据慢一个数量级；置换环应用时注意 order 是「位置→原下标」语义、循环需要其逆置换。`groupedRead` 保证合并序下窗口按 (FileID,Offset) 分组顺序读（防随机 IO），平铺序下是稳定 no-op。
6. **分块并行读**：16MB 块、块边界对齐换行；候选行访问用顺序读游标 + 缓冲池，无随机 IO；`buildIndex` 合并期逐块释放块结果（防双份 GB 级并存）；多文件加载 4 路并行、完成后按拖入序重排。
7. **内存治理**：结果上限按物理内存自适应（`resultlimit.go`，200 万保底~2000 万封顶）；大结果被小结果替换时延迟 1s 异步 `debug.FreeOSMemory`（立刻跑会把全局 STW 计入紧邻查询）；**禁止 `debug.SetMemoryLimit`**——软上限让 GC 满负荷，20GB 全扫实测 7.8s→25s（反例已记入 design.md）。
8. **编码**：GBK/UTF-8 自动探测；GBK 文件的关键字与正则按 GBK 字节匹配（预转换进 matcher），含 Unicode 范围类的正则不适用 GBK；高亮在 UTF-8 侧。
9. **防御上限**：单行显示 64KB 截断（`capLine`）；zip 解压总量 8GB；**复制整行走后端**（`CopyLineText`/`CopyText` 直写系统剪贴板）——前端剪贴板 API（navigator.clipboard / 前端 ClipboardSetText）对大文本会截断，禁止前端自行写剪贴板。
10. 正则一律 Go RE2（`regexp` 标准库），天然免疫灾难性回溯——禁止引入第三方正则库。
11. **zip 解压零残留**：解压产物落 `%TEMP%\log-hunter-unzip-*`（每 zip 一子目录），`RemoveFile` 清零引用删子目录并同步清去重登记（删光后重拖可加载）、`ClearAll`/退出删根目录（失败重试 3 次）；崩溃残留由启动 `SweepStaleTemp` 清扫（目录年龄 >1h + 锁文件探针双护栏防误删活实例，幂等重试）；二进制判定用内容（前 16KB 含 NUL）**不是扩展名**——轮转命名的日志（`.log.101`）必须能收；嵌套 zip 按内容魔数（PK 头）递归解压，限 3 层，8GB 上限全树共享（含内层 scratch），每层独立 zip-<seq> 子目录随移除隔离清理。

## 编码约定

- 注释一律中文；复杂业务逻辑写详细「为什么」注释，简单代码不注释。
- 前端不引 UI 框架，组件手写；高亮必须走 `HighlightText.vue` 的分段渲染防 HTML 注入，严禁直接 v-html 拼接日志内容。标记色经 CSS 变量（`--hl-bg`/`--hl-fg`）由 `style.css` 的 `.mark-0~4` 驱动，不要在组件里硬编码色值。
- **颜色一律走 `style.css` token，双主题（夜巡 `:root` / 日间 `[data-theme='light']`）下严禁组件硬编码色值**。表面四级：`--bg`（页面）< `--bg-panel`（面板）< `--bg-elev`（组件）< `--bg-float`（浮层）——新增浮层（菜单/下拉）必须用 `--bg-float` + `--shadow-pop`；信号色板（`--lvl-N-fg/bg`、`.mark-N`）只在此处定义。
- 主题状态在 `App.vue`（三态：auto/light/dark，localStorage `lh-theme` + matchMedia），启动预设在 `main.ts`（防闪色），勿在别处操作 `data-theme`。
- 详情面板右键菜单（`ContextMenu.vue`）与格式标记槽位（`ContextPanel.vue` 内 5 槽）是纯前端状态，面板卸载即清；标记优先于查询高亮（重叠区间让位），该优先级在 `HighlightText.vue` 的区间裁剪逻辑里，改动保持。
- 大结果集靠虚拟滚动 + 分段续载，任何列表改动不得破坏这一点。行渲染用**槽位复用**（`v-for` 的 key 是段内槽位号而非全局行号，内容经 `visibleRows` computed 驱动）——改成全局行号作 key 会退化成滚动时逐行挂载/卸载；无高亮条件时走纯文本插值快速路径（跳过 HighlightText 实例化）。
- 结果表横向画布宽度封顶 `MAX_CANVAS_W`（20 万 px）——极长行（如整行 SQL dump 数十万字节）按 12px/rune 撑开会是数百万 px 的超大层，WebView 光栅化直接卡死界面（v0.0.5 实测 75 万 rune → 901 万 px 全界面卡）；超长行看全文走双击行的上下文面板。

## 其他

- 远端仓库 `https://github.com/macaoyxx/log-hunter`（main 分支）；`build/bin`、`frontend/dist`、`node_modules`、`release/` 已在 `.gitignore`。
- **发版脚本**（bash 跨平台，产物不进 git）：`scripts/package.sh <x.y.z>` 仅本地打包（ldflags 注入版本，Go 标准库压缩，`release/<版本>/` 只留 zip）；`scripts/publish.sh <x.y.z>` 先打包再发布，内置终端确认门。
- **发布铁律（用户定版，2026-10-31 终版）**：发布必须以**用户的显式发布指令**为前提（对话中说「发布/发版」即为启动信号），**无指令绝不得自行发布**。分工固定：AI 执行发布准备全程——迭代收尾 → 合并 main（ff-only）→ push main，完成后**指引停下**；**最终发布动作（push tag / gh release create）必须用户本人跑 `./scripts/publish.sh <版本>` 在终端输入 yes 确认**——AI 绝不代跑最终发布、绝不代答确认门（管道喂 yes 同属代答，禁止）。
- **Release Notes 必须列内容要点**：发布说明由 `publish.sh` 自动提取迭代 README 的「范围」节生成（本版变更要点清单），**不得只写版本号 + 文档链接**——Release 页要一眼看清本版改了什么；因此迭代 README 的「范围」节按发布说明口径写精炼要点。
- gh CLI 已装（`C:\Program Files\GitHub CLI`，Git Bash 需 `export PATH` 追加）并登录 macaoyxx。
- **GitHub 直连间歇抖动**：git push / gh 命令偶发 connection reset，重试即可，勿当成配置问题。
- `frontend/package.json.md5` 是 Wails 的依赖指纹缓存，勿手工编辑。
- 换应用图标后 Windows 有系统缓存，旧图残留需重建快捷方式或重启资源管理器。
