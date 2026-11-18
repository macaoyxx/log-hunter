package diag

import (
	"log-hunter/internal/ai/client"
)

// 取证工具的参数 schema 与描述（执行器由绑定层注入——本包不触碰 logscan.Session，
// 影子查询的调用在 app.go 侧包装）。级别枚举：0=TRACE 1=DEBUG 2=INFO 3=WARN 4=ERROR 5=FATAL。
// write_doc 为本包内置实现（见 diag.go execTool），不进注入清单

// Toolset 构造取证工具集（七个只读工具，全部 nil 的项跳过——便于测试按需注入）
func Toolset(search, getContext, getLine, histogram, cluster, getStack, follow ToolFunc) []Tool {
	var out []Tool
	add := func(def client.ToolDefinition, fn ToolFunc) {
		if fn != nil {
			out = append(out, Tool{Def: def, Exec: fn})
		}
	}
	add(searchTool, search)
	add(getContextTool, getContext)
	add(getLineTool, getLine)
	add(histogramTool, histogram)
	add(clusterTool, cluster)
	add(getStackTool, getStack)
	add(followTool, follow)
	return out
}

var searchTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "search",
		Description: "在全部已加载日志中搜索（不限于当前结果集）。返回命中总数 + 命中行（文件名:行号 + 原文 + 时间），最多 limit 条（默认 30，上限 50）——总数大时请收窄条件而不是翻页。需要看某行前后文用 get_context，看整段异常堆栈用 get_stack。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword":   map[string]any{"type": "string", "description": "关键字（包含匹配，多个空格分隔为 OR）"},
				"regex":     map[string]any{"type": "string", "description": "正则（Go RE2 语法，与 keyword 二选一或组合）"},
				"levels":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "级别过滤（0=TRACE..5=FATAL）"},
				"timeStart": map[string]any{"type": "string", "description": "起始时间，格式 2024-01-15 10:23:45"},
				"timeEnd":   map[string]any{"type": "string", "description": "结束时间"},
				"limit":     map[string]any{"type": "integer", "description": "返回条数上限（默认 30，最大 50）"},
				"files":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "限定文件（文件名或基名，如 [\"app1.log\"]；缺省=全部文件）"},
			},
		},
	},
}

var getContextTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "get_context",
		Description: "取指定文件指定行的上下文（前后各 N 行，默认 20，上限 50）。适合看堆栈主行前后的关联日志。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file":   map[string]any{"type": "string", "description": "文件名（search 结果中的文件名）"},
				"line":   map[string]any{"type": "integer", "description": "行号（1-based）"},
				"before": map[string]any{"type": "integer", "description": "往前取的行数（默认 20，上限 50）"},
				"after":  map[string]any{"type": "integer", "description": "往后取的行数（默认 20，上限 50）"},
			},
			"required": []string{"file", "line"},
		},
	},
}

var getLineTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "get_line",
		Description: "读指定文件指定行的完整原文（不截断）。超长行（SQL dump 等）用它看全文。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{"type": "string", "description": "文件名"},
				"line": map[string]any{"type": "integer", "description": "行号（1-based）"},
			},
			"required": []string{"file", "line"},
		},
	},
}

var histogramTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "histogram",
		Description: "当前结果集的时间分布（60 桶，各级别计数）——看故障爆发时段、是否周期性。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

var clusterTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "cluster",
		Description: "对全部已加载日志（可按级别过滤）做全局错误聚类——当前结果集之外的全局视角。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{
				"levels": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "级别过滤（如 [4,5] 只看 ERROR/FATAL），缺省=全部"},
			},
		},
	},
}

// getStackTool 异常块整块读取：主行带时间戳+续行无时间戳是堆栈/SQL 块的天然形态，
// 一次给全块（替代 3~5 次 get_context 拼接——碎片拼图反而容易牵强附会）
var getStackTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "get_stack",
		Description: "读取完整异常堆栈块：给异常主行（或块内任意行）的文件名+行号，自动向前定位主行、向后收齐全块续行（Caused by 嵌套、帧列表一次给全）。排查异常优先用它而不是反复 get_context。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{"type": "string", "description": "文件名（search 结果中的文件名）"},
				"line": map[string]any{"type": "integer", "description": "异常主行或块内任意行的行号（1-based）"},
			},
			"required": []string{"file", "line"},
		},
	},
}

// followTool 跨文件关联追踪：请求 ID / traceId / 线程名 / 订单号等任意标识，
// 一次拉全所有文件的相关行并按时间合并成单一时间线（跨节点交叉排查的核心动线）
var followTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "follow",
		Description: "按关联标识跨全部文件拉取完整链路：给请求ID / traceId / 线程名 / 订单号等能把多文件串起来的值，返回所有相关行（按时间合并排序）+ 各文件命中数。跨节点/跨服务排障首选；标识太宽会截断（500 行上限），请换更精确的值。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "关联标识（整串匹配，不拆词）"},
			},
			"required": []string{"id"},
		},
	},
}

// findFilesTool 授权目录内文件定位（内置实现，执行在 source.go）：堆栈指向类名时
// 先定位文件路径再 read_source——只在授权范围内搜索，不扩权不提示未授权目录存在
var findFilesTool = Tool{
	Def: client.ToolDefinition{
		Type: "function",
		Function: client.FunctionSpec{
			Name:        "find_files",
			Description: "在已授权的目录内按文件名查找文件（支持 * 通配或片段匹配，大小写不敏感），返回最多 50 个路径。日志堆栈指向类名/模块时先用它定位源码文件，再 read_source 读取。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "文件名或片段（如 PayService、*.yml、FlowTask）"},
				},
				"required": []string{"pattern"},
			},
		},
	},
}

// readSourceTool 源码读取（内置实现，执行在 source.go）：日志堆栈指向代码时请求读源码；
// 首次读取需用户授权（授权卡：允许并记住/仅本次/拒绝），拒绝后本会话不再询问
var readSourceTool = Tool{
	Def: client.ToolDefinition{
		Type: "function",
		Function: client.FunctionSpec{
			Name:        "read_source",
			Description: "读文件片段（源码/文档/配置等，日志堆栈指向的类、项目文档都算）。目录授权为整棵子树（授权一次全子树免问）。首次访问会向用户请求目录授权（reason 必填说明动机）；用户拒绝后本会话内不得再请求，应基于日志继续。yml/env/json/密钥类敏感文件每次读取均需单独授权（无记住选项），请一次读全所需行段减少打扰。引用文件用 文件名:行号。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "源码文件绝对路径"},
					"reason":    map[string]any{"type": "string", "description": "为什么需要读这个文件（展示给用户做授权决定）"},
					"startLine": map[string]any{"type": "integer", "description": "起始行号（读取该行段±50行）"},
					"endLine":   map[string]any{"type": "integer", "description": "结束行号"},
				},
				"required": []string{"path", "reason"},
			},
		},
	},
}

// writeDocTool 受控产出（内置实现，schema 在此、执行在 Session.writeDoc）
var writeDocTool = Tool{
	Def: client.ToolDefinition{
		Type: "function",
		Function: client.FunctionSpec{
			Name:        "write_doc",
			Description: "把指定内容保存为本地文档（markdown）。仅当用户明确要求「生成文档/保存报告」时才可调用——分析结果默认直接在回复中输出，禁止主动落盘。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{"type": "string", "description": "文件名（仅文件名不含路径，如 report.md）"},
					"content":  map[string]any{"type": "string", "description": "文档全文（markdown）"},
				},
				"required": []string{"filename", "content"},
			},
		},
	},
}
