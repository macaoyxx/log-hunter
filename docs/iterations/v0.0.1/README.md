# v0.0.1 — 建仓与发版机制
- 状态：🟢 已上线
- 起止：2026-10-30 ~ 2026-10-30
- 相关提交：
  - 37d1e99 全模块 - v0.0.1 - 新增初始版本入库
  - bdf3207 发版脚本 - v0.0.1 - 修复中文注释乱码致脚本无法执行

## 目标（为什么做）
项目代码此前仅存本地（无版本控制）。建立 GitHub 远端仓库并落地 v0.0.1 版本基线，同时固化「release/<版本>/ 发包」机制，让后续每次发版一条命令完成。

## 范围（做了什么，清单）
- `git init` + 关联远端 `https://github.com/macaoyxx/log-hunter`（main 分支），首次提交推送
- `.gitignore` 追加 `release/`（产物不进 git，经 GitHub Release 发布）
- 新增 `release.ps1`：`-Version` 参数（三段版本号校验）→ wails build → 拷 exe 到 `release/<版本>/`（临时文件）→ 压缩 `log-hunter-v<版本>-windows-amd64.zip` → 删 exe，目录内只留 zip
- README「构建与开发」、AGENTS.md 补发版流程说明
- 打 tag `v0.0.1` 并推送；GitHub Release 上传 zip 产物

## 影响面
- 无代码逻辑改动，不影响运行行为
- 新增发版约定：版本号 = git tag（`v<x.y.z>`）；产物命名 `log-hunter-v<版本>-windows-amd64.zip`

## 结果
- 远端 main 分支 + tag v0.0.1 就绪；`release/v0.0.1/` 下仅保留 zip
- 验证方式：push/tag 推送成功、release.ps1 执行后目录内容检查、GitHub Release 页面可见产物
