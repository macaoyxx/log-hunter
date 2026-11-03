# 引擎共享状态并发铁律（logscan.Session）

## 规则

`Session` 的**结果状态五字段**——`results / stats / mode / browseFiles / browsePrefix`——任何**读或写**都必须持 `s.mu`（读写锁）：

- 读者：`GetResults`、`fetchRows`（约定锁内调用）、`ExportResults` —— 持 `RLock`
- 写者：`RunQuery`、`SetMerged`、`RemoveFile`、`ClearAll` —— 必须在 `Unlock()` **之前**完成赋值

锁序恒为 `opMu → mu`，不得反序。

## 为什么（踩坑：崩溃级数据竞争）

v0.0.13 加固前，`RemoveFile`/`ClearAll` 在 `s.mu.Unlock()` **之后**裸写这五个字段（`session.go` 原 :379-383/:413-417），而 `GetResults` 持 `RLock` 并发读——Go 切片头（ptr/len/cap）非原子写与并发读交叉可读到撕裂 slice 导致越界崩溃；`mode` 与 `results` 不一致还会按 virtual 模式翻译已置 nil 的 `browsePrefix`。Wails 每个绑定在独立 goroutine 执行，前端「勾选即查」使 RemoveFile/ClearAll 高频化，竞争实际可触发。

另两个同批修复的并发点：扫描进度节流 `lastEmit` 被 worker 并发调用（改 `atomic.Int64` CAS）；并发拖入同路径的去重检查（RLock）到落位（Lock）之间隔完整索引（TOCTOU），锁内复查防双份索引。

## 关键文件

- `internal/logscan/session.go` —— `RemoveFile`/`ClearAll`（锁内置空，注释「结果状态必须在锁内」）、`lastEmit`（atomic.Int64）、`addOne` 尾部（TOCTOU 复查）
- 前端自动重查链路：`App.vue` 的 `toggleSelect`/`toggleAllFiles`/`addFiles`（`!querying` 防抢跑，恢复勾选**不设 stats 守卫**——清空已把 stats 置空，再挡就永远查不了）

## 相关

[结果表画布宽与列宽机制](.memory/result-canvas-layout.md)
