package diag

import (
	"fmt"
	"strings"
	"time"

	"log-hunter/internal/logscan"
)

// expertSystemPrompt 内置日志分析专家人格（用户零配置）。
// 证据纪律是人格核心：强制引用、三态标注、证据不足明说、视觉转述降权
const expertSystemPrompt = `你是 LogHunter 内置的日志分析专家，精通跨层排查：Java 异常堆栈链、慢 SQL、GC/OOM、线程与并发竞态、网络与超时、依赖级联故障。

# 铁律（违反即分析失败）
1. 每个结论必须挂证据：「[行 N]」（N 为当前结果集的全局行号）或「[文件名:行号]」（结果集外的行）。不允许无证据的断言。
2. 严禁编造行号、文件名、数字。引用的行号必须来自我提供给你的上下文或你调用工具亲眼看到的返回。
3. 严格区分并显式标注三态：【事实】= 日志中可见；【推断】= 基于经验的判断（说明依据）；【待验证】= 需要用户补充确认。不许把推断包装成事实。
4. 图片内容来自视觉模型转述，不是事实：转述可能不完整或有误，与日志证据冲突时一律以日志为准。
5. 证据不足以定位根因时，明确说「证据不足，需要 X」，并给出获取 X 的建议——这是合格答案，硬编结论才是失败。

# 工作方式
- 我会先给你聚类摘要（错误有几种、各多少条、代表行）。
- 对可疑组用 search / get_context / get_line 深挖取证，不要只凭摘要下结论；search 最多返回 50 行，需要更多时用更精确的关键字。
- 需要看全局时间分布用 histogram，看全局错误分布用 cluster。
- 分析结果直接在回复中完整输出（用户在面板阅读）。write_doc 工具仅在用户明确要求「生成文档/保存报告」时才可使用——禁止主动落盘。

# 最终报告格式（markdown）
## 概览
仅【事实】：现象、规模、时间范围。
## 根因假设
按置信度降序排列，每条含：结论、支持证据（行引用列表）、反对证据（若有）、置信度（高/中/低）。
## 证据链
按时间线串起关键行（每行带引用）。
## 影响范围
## 建议操作
每条可执行；涉及查询的给出一行「查询: <关键字或正则>」（用户可一键执行）。
全程使用中文。`

// visionSystemPrompt 视觉中转转述规则——防瞎编三道闸之一：
// 只忠实提取、看不清必须说看不清、严禁推测补全
const visionSystemPrompt = `你唯一的任务：忠实转述图片内容，供日志排查使用。规则：
- 提取所有可见文字（OCR 原样保留，不要总结改写，不要纠正其中看似错误的内容）；
- 图表：读出坐标轴含义、序列名、关键数值、明显趋势（何时上升/下降/突变）；
- 架构/流程图：列出节点名称与连线关系；
- 截图报错：逐字转录错误文本；
- 看不清、被遮挡、无法确定的部分：明确标注「（看不清）」，严禁凭常识猜测补全。
输出中文，按图内区域分节描述。`

// visionPrefix 转述进入主对话时的来源标记——防瞎编三道闸之二：
// 主模型必须知道这是二手转述而非事实
const visionPrefix = "[图片 %d 视觉转述——非原图，可能不完整或有误，与日志证据冲突时以日志为准]\n"

// buildBaseMessage 首轮 user 消息：文件清单 + 统计 + 聚类摘要（按预算弹性选组数与样本长度）。
// charsBudget 为字符预算（token 预算 × 3 字符/token × 70% 余量系数，粗估口径）
func buildBaseMessage(b Base, charsBudget int) string {
	var w strings.Builder
	w.WriteString("# 诊断基线（当前结果集）\n\n")
	w.WriteString("## 文件清单\n")
	w.WriteString(b.FileList)
	w.WriteString("\n\n## 命中统计\n")
	w.WriteString(b.Stats)
	if b.TimeRange != "" {
		w.WriteString("\n时间范围：")
		w.WriteString(b.TimeRange)
	}
	w.WriteString("\n\n# 错误聚类摘要\n")
	if b.Cluster == nil || len(b.Cluster.Groups) == 0 {
		w.WriteString("（无聚类结果）\n")
	} else {
		fmt.Fprintf(&w, "共 %d 组模板（结果集 %s 行，参与聚类 %s 行%s）。每组给出归一模板（<ts>/<n>/<hex> 为易变项占位）、代表行与锚点。\n",
			len(b.Cluster.Groups), humanInt(b.Cluster.TotalRows), humanInt(b.Cluster.Participated),
			sampledNote(b.Cluster.Sampled))
		w.WriteString("锚点口径：「[行 N]」= 当前结果集全局行号；「文件名:行号」= 文件内原始行号。\n\n")
		n := pickGroupCount(charsBudget)
		sampleLen := pickSampleLen(charsBudget)
		for i, g := range b.Cluster.Groups {
			if i >= n {
				fmt.Fprintf(&w, "（其余 %d 组略——需要时用 cluster 工具查看）\n", len(b.Cluster.Groups)-n)
				break
			}
			writeGroup(&w, i+1, g, sampleLen)
		}
	}
	return w.String()
}

// writeGroup 单组摘要：计数/级别/时间范围/首行锚点 + 模板 + 样本
func writeGroup(w *strings.Builder, no int, g logscan.ClusterGroup, sampleLen int) {
	fmt.Fprintf(w, "## 组 %d：%s 条", no, humanInt(g.Count))
	if lv := logscan.Level(g.Level); lv >= 0 {
		fmt.Fprintf(w, " · %s", lv)
	}
	if g.FirstTS > 0 {
		fmt.Fprintf(w, " · %s ~ %s", fmtTime(g.FirstTS), fmtTime(g.LastTS))
	}
	if g.FirstIdx >= 0 {
		fmt.Fprintf(w, " · 首行 [行 %d]", g.FirstIdx+1)
	}
	w.WriteString("\n模板：")
	w.WriteString(g.Template)
	w.WriteString("\n")
	for _, s := range g.Samples {
		anchor := fmt.Sprintf("[文件 %s:%d]", s.File, s.LineNo)
		if s.Idx >= 0 {
			anchor = fmt.Sprintf("[行 %d]", s.Idx+1)
		}
		fmt.Fprintf(w, "  %s %s\n", anchor, clip(s.Text, sampleLen))
	}
	w.WriteString("\n")
}

// pickGroupCount 按预算分档组数：小窗 20 组起步，大窗 50 组封顶
func pickGroupCount(charsBudget int) int {
	switch {
	case charsBudget >= 300_000:
		return 50
	case charsBudget >= 150_000:
		return 40
	case charsBudget >= 60_000:
		return 30
	default:
		return 20
	}
}

// pickSampleLen 每条代表行的文本长度：小窗 200 字，大窗 600 字
func pickSampleLen(charsBudget int) int {
	if charsBudget >= 150_000 {
		return 600
	}
	return 200
}

func sampledNote(sampled bool) string {
	if sampled {
		return "，超采样上限已均匀采样，组内计数为采样口径"
	}
	return ""
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func humanInt(n int) string {
	s := fmt.Sprintf("%d", n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func fmtTime(unixNano int64) string {
	return time.Unix(0, unixNano).Format("15:04:05")
}
