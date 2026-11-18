package diag

import (
	"log-hunter/internal/ai/client"
)

// 取证工具的参数 schema 与描述（执行器由绑定层注入——本包不触碰 logscan.Session，
// 影子查询的调用在 app.go 侧包装）。级别枚举：0=TRACE 1=DEBUG 2=INFO 3=WARN 4=ERROR 5=FATAL。
// write_doc 为本包内置实现（见 diag.go execTool），不进注入清单

// Toolset 构造取证工具集（五个只读工具，全部 nil 的项跳过——便于测试按需注入）
func Toolset(search, getContext, getLine, histogram, cluster ToolFunc) []Tool {
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
	return out
}

var searchTool = client.ToolDefinition{
	Type: "function",
	Function: client.FunctionSpec{
		Name:        "search",
		Description: "在全部已加载日志中搜索（不限于当前结果集）。返回命中的行（文件名:行号 + 原文 + 时间），最多 limit 条（默认 30，上限 50）。需要看某行前后文用 get_context。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword":   map[string]any{"type": "string", "description": "关键字（包含匹配，多个空格分隔为 OR）"},
				"regex":     map[string]any{"type": "string", "description": "正则（Go RE2 语法，与 keyword 二选一或组合）"},
				"levels":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "级别过滤（0=TRACE..5=FATAL）"},
				"timeStart": map[string]any{"type": "string", "description": "起始时间，格式 2024-01-15 10:23:45"},
				"timeEnd":   map[string]any{"type": "string", "description": "结束时间"},
				"limit":     map[string]any{"type": "integer", "description": "返回条数上限（默认 30，最大 50）"},
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
			"type": "object",
			"properties": map[string]any{
				"levels": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "级别过滤（如 [4,5] 只看 ERROR/FATAL），缺省=全部"},
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
