# AI 取证工具：上限体系、阶段可视化与 E2E 回归

## 规则

### 工具上限防护栏（改数值/加工具前先对照）
| 工具 | 上限 | 语义 |
|---|---|---|
| search | 返回 50 行；总数计数封顶 1000（标「≥」） | 规模感与全扫代价的取舍：精确计数要 20GB 级全扫 |
| follow | 500 行 | 宽标识（通用线程名）防灌爆，截断提示换精确标识 |
| get_stack | 200 行 / 64KB | 异常块整块读取，超大块提示 get_context 续读 |
| get_context | 前后各 50 行 | 上下文窗口保护 |
| read_source | 8MB 拒读 / 2MB 截断 / 默认 400 行（±50）/ 目录清单 200 项 | 分段读取是正常用法 |
| find_files | 200 路径 + 截断附目录分布（计数降序、并列路径字典序） | 路径便宜视野给足；分布才是导航信息 |
| cluster / histogram | 30 组 / 60 桶 | 全局视角够看 |

**设计原则**：所有截断必须显式标注 + 留继续手段 + 聚合信息（聚类/直方图/总数）不受行数上限影响——上限影响穷尽性不影响正确性，看不到的不许下结论（prompt 铁律 6 要求报告声明取证范围）。

### 等待阶段标注（AiPanel）
- 只在「看不见动静」时标注：`取证中 · 工具名`（最后一个未完成调用，FIFO=真实执行序）/ `模型思考中`
- 正文在流时**不显示标签**（文字自证）；思考块流式展开「思考中…」、轮末自动收起转「思考过程」

### 授权三态与基线重锚
- 授权回执三态：**点击**（允许/记住/拒绝）/ **取消**（停止或整轮超时：不登记，没做决定不算数）/ **未响应**（15 分钟兜底：NoAnswer 如实话术，仍登记本会话不再询问防反复重试）
- 冷启动会话（拖文件夹分析源码开局，BaseTotal==0）：结果集出现是「锚定」不是「变化」——放行并在 Followup 重锚基线；有基线后才拦截变化

### 真模型 E2E 回归
- `LIVE_E2E=1 LH_AI_JSON=build/bin/data/ai.json LH_TARGET_DIR=<目录> go test ./internal/ai/diag -run TestLiveAgentSource -v -timeout 12m`
- ai.json **副本**开仓授权（用户原件不动）；断言 FIFO 零残留、报告非空；默认 skip 不碍常规回归

## 关键文件
- `internal/ai/diag/source.go`（read_source/find_files/授权三态）—— 上限常量与授权语义
- `internal/logscan/cluster.go`（SideQuery/SideStack/SideFollow）—— 影子查询与各上限
- `internal/ai/diag/prompt.go` —— 铁律 6（取证范围声明）与报告「取证范围」节
- `internal/ai/diag/live_e2e_test.go` —— 真模型回归装置
- `frontend/src/components/AiPanel.vue`（applyEvt/ai-wait-v2）—— 阶段标注与思考块

## 为什么这么做（踩坑）

- **工具设计方法论**：给 agent 的工具要「一次给全证据块」——get_stack 整块堆栈、follow 全链路，
  替代多次 get_context 拼接：碎片拼图既慢又容易牵强附会；并行取证提示（一回复批量发调用）
  实测 GLM-5.3 一次批量 3 个 read_source，省 2 个往返
- **阶段标注的三个被否形态**：①「第 N 步」循环号与「取证 N 步」调用数单位撞名，并行批量后
  必然对不上（18 vs 11）；②「等待首字 X 秒」纯倒计时无信息量；③「输出报告中」正文就在流，
  标签是废话。正解=只在无可见动静时标注 + outputting 需在 reasoning/tool_call 到达时复位
  （跨步残留会让「模型思考中」失显）
- **思考块标题写死「思考中…」**：轮结束后永远显示在思考的假象——标题随 streaming 态切换 +
  轮末自动收起，用户开合经 @toggle 回写（否则与 :open 绑定打架）
- **真模型动线测不了**的教训：单测/构建全绿仍被用户抓出三处动线 bug（配对卡死/单位撞名/标签
  假象）——UI 无法本地起端点的部分必须靠 E2E 装置兜底，改 agent 动线后跑一次

## 相关
[AI 专家面板契约](.memory/ai-agent-panel.md)（配对协议与面板生命周期）
