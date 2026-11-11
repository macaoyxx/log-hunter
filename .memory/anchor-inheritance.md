# 时间锚点继承语义（四处同源）

## 是什么 / 规则

无时间戳的行（堆栈/SQL 续行）的「有效时间」按锚点继承：向前继承同文件最近一条非零时间戳；文件开头无前锚点继承本文件第一条非零（firstTS）；整文件无时间戳的行无锚点（直方图计入 NoTS 不进桶，合并排序置尾 mergeKeyTail）。

**该语义现有四处实现，改语义必须全部同步改：**

| 处 | 位置 | 形态 |
|---|---|---|
| 时间/级别筛选 | `internal/logscan/indexer.go` `filterCandidates` | 索引侧逐行判断是否落在时间窗 |
| 合并排序 key | `internal/logscan/merge.go` `sortMerged` | 双指针（scanned/anchor）算排序 key |
| 直方图聚合 | `internal/logscan/histogram.go` `aggIndex`（零物化）+ `anchorState.effective`（物化结果） | 分桶 |
| 时刻跳转定位 | `internal/logscan/histogram.go` `IndexByTime`（物化分支复用 anchorState） | 找 ≥ns 最早行 |

两种实现形态的差异：

- **索引侧**（aggIndex）：单文件行号连续，`lastTS` 单变量推进即可
- **结果数组侧**（anchorState.effective）：合并序下 FileID 不连续，「序列向前继承」依赖 sortMerged 稳定排序保证续行紧贴主行不拆散；跨文件边界/序列开头回退 firstTS（按文件懒算缓存 map，含 0 值）

## 为什么这么做

筛选、排序、统计分布、跳转定位对「续行属于哪个时刻」必须给同一答案——否则筛进来的行在直方图里对不上、跳转落点与筛出口径漂移，四者是同一产品语义的四个投影。

## 踩坑

- `sortMerged`/`filterCandidates` 的锚点扫描必须「进度/锚点双指针」（进度每行无条件推进）——只在遇非零时推进会遇大段无时间区退化成每行重扫（历史事故：实测 782 亿次内层扫描、46.5s 卡死）
- 零物化 `IndexByTime` 对 `idx.ts` 二分假设时间单调，但续行 `ts=0` 打破单调使落点偏后（stdLog 单测逮住过：应命中下标 2 返回 5），已加 1024 行向前窗口校正；时间回拨文件仍可能偏前，属声明的近似语义

## 相关

[时间直方图](.memory/time-histogram.md)、[会话并发铁律](.memory/session-concurrency.md)
