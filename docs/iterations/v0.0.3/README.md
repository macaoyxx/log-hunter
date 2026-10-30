# v0.0.3 — 头部菜单与配置持久化
- 状态：🟢 已上线
- 起止：2026-10-30 ~ 2026-10-30
- 相关提交：
  - 03a81be 文档 - 新增记忆目录沉淀 Wails 拖放陷阱
  - ed90c3d 应用层 - 新增配置持久化与版本检查后端
  - 7f9aa8e 界面 - 新增头部菜单关于弹窗与持久化接入
  - e3b1d2d 发版脚本 - 调整压缩为 Go 标准库跨平台实现

## 目标（为什么做）
①头部缺功能入口：需要「检查更新」（对比 GitHub Release）与「关于」（含版本号）；②查询历史仅会话内存在，重启即丢；③主题偏好此前仅 localStorage，需文件级持久化。历史与主题统一存 exe 同级 `data/config.json`（用户定 JSON 方案不用 SQLite）。标题栏保留系统原生（颜色随系统，Wails 无运行时 API，属已接受限制并在 README 注明）。

## 范围（做了什么，清单）
- **头部菜单**：品牌栏 ≡ 按钮 → 浮层下拉（检查更新 / 关于）；检查更新在浮层内呈现结果态（正在检查/已是最新/发现新版本可打开发布页/网络失败）
- **关于弹窗**：自绘居中浮层（版本号 + 简介 + 仓库地址）
- **版本号注入**：main.go `var version = "dev"`，release.ps1 经 `-ldflags "-X main.version=$Version"` 注入，dev 构建显示 dev
- **检查更新**：GET GitHub Releases latest API（5s 超时），语义化版本比较
- **appconf 持久化**：`internal/appconf` 纯库——`data/config.json`（Theme + History[]，上限 20），原子写（tmp+rename）；app.go 绑定 GetConfig/SetTheme/SaveHistory
- **前端接入**：主题以 config.json 为权威（localStorage 作第一道防闪与迁移源，双写）；QueryBar 挂载载入历史、查询后保存
- **发版脚本重构**：release.ps1 拆为 `scripts/package.sh`（仅本地打包）与 `scripts/publish.sh`（先打包→**写死确认门**（人工输入 yes）→推 tag + gh release create，gh 不可用降级手动指引），bash 跨平台；关于弹窗图标换真实应用图标、开发版版本号默认 `<迭代号>-dev`；顺手修复 bench 工具未跟上 AddFiles 三返回值的编译错误

## 影响面
无接口破坏性变更；新增 appconf 包与 4 个绑定方法。

## 结果
- 分支 v0.0.3 推送，scripts/publish.sh 经用户确认后打 tag v0.0.3 发布 GitHub Release
- 验证：go test 全绿（appconf 4 用例：往返/去重上限/损坏回落/副本隔离）；wails build 通过；GUI 手测（菜单检查更新/关于含图标与版本号/历史与主题重启保留）；脚本端到端实测（package.sh 全平台压缩、publish.sh 确认门取消与重发防护路径）
- 评审修复：tag 重发防护、检查更新防抖、脚本 executable bit、Wails 调用 catch 补齐；实测根因修复 powershell 静默失败（终以 Go 标准库压缩根治）
- 提交边界备注：发版脚本重构因 executable bit 预暂存并入首笔文档提交（03a81be），与原拆分方案略有出入
