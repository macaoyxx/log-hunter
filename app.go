package main

import (
	"context"
	"fmt"
	"time"

	"log-hunter/internal/logscan"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 应用结构：Wails 绑定层，包装 logscan.Session
type App struct {
	ctx     context.Context
	session *logscan.Session
}

// NewApp 创建应用实例
func NewApp() *App {
	return &App{session: logscan.NewSession()}
}

// startup Wails 生命周期：保存上下文
func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// shutdown 退出时释放全部资源
func (a *App) shutdown(ctx context.Context) { a.session.ClearAll() }

// AddFilesResult 批量添加文件的结果
type AddFilesResult struct {
	Files    []*logscan.FileEntry `json:"files"`
	Errors   []string             `json:"errors"`
	Notices  []string             `json:"notices"`  // 非错误提示（zip 内二进制/加密等跳过项）
	AllFiles []*logscan.FileEntry `json:"allFiles"` // 当前会话全部文件
}

// AddFiles 拖拽添加文件：探测格式并建索引（.zip 自动解压后加载其中文本文件），
// 进度经 "files:progress" 事件推送
func (a *App) AddFiles(paths []string) (*AddFilesResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("没有文件")
	}
	files, errs, notices := a.session.AddFiles(a.ctx, paths, func(p logscan.FileProgress) {
		runtime.EventsEmit(a.ctx, "files:progress", p)
	})
	return &AddFilesResult{Files: files, Errors: errs, Notices: notices, AllFiles: a.session.Files()}, nil
}

// RemoveFile 移除单个文件
func (a *App) RemoveFile(id int64) { a.session.RemoveFile(id) }

// ClearAll 一键清空全部文件与结果
func (a *App) ClearAll() { a.session.ClearAll() }

// ListFiles 当前文件列表
func (a *App) ListFiles() []*logscan.FileEntry { return a.session.Files() }

// QuerySpecJSON 前端查询条件（时间为 datetime-local 字符串，空串不限）
type QuerySpecJSON struct {
	FileIDs    []int64  `json:"fileIds"`
	Keywords   []string `json:"keywords"`
	Excludes   []string `json:"excludes"`
	Regex      string   `json:"regex"`
	KeywordAnd bool     `json:"keywordAnd"`
	TSStart    string   `json:"tsStart"` // "2006-01-02T15:04[:05]"
	TSEnd      string   `json:"tsEnd"`
	Levels     []int8   `json:"levels"`
}

// parseLocalTime 解析 datetime-local 字符串为 unixNano；空串返回 0
func parseLocalTime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UnixNano(), nil
		}
	}
	return 0, fmt.Errorf("无法解析时间: %q", s)
}

// Query 执行查询（条件全空 = 浏览全部行），进度经 "query:progress" 事件推送
func (a *App) Query(spec *QuerySpecJSON) (*logscan.QueryStats, error) {
	if spec == nil || len(spec.FileIDs) == 0 {
		return nil, fmt.Errorf("请先选择要查询的文件")
	}
	start, err := parseLocalTime(spec.TSStart)
	if err != nil {
		return nil, err
	}
	end, err := parseLocalTime(spec.TSEnd)
	if err != nil {
		return nil, err
	}
	if start > 0 && end > 0 && end < start {
		return nil, fmt.Errorf("结束时间不能早于开始时间")
	}
	s := &logscan.QuerySpec{
		FileIDs: spec.FileIDs, Keywords: spec.Keywords, Excludes: spec.Excludes,
		Regex: spec.Regex, KeywordAnd: spec.KeywordAnd,
		TSStart: start, TSEnd: end, Levels: spec.Levels,
	}
	return a.session.RunQuery(a.ctx, s, func(p logscan.QueryProgress) {
		runtime.EventsEmit(a.ctx, "query:progress", p)
	})
}

// GetResults 分页取结果窗口（虚拟滚动用）
func (a *App) GetResults(offset, limit int) []logscan.ResultRow {
	return a.session.GetResults(offset, limit)
}

// GetContext 取指定行的上下文
func (a *App) GetContext(fileID, offset int64, before, after int64) ([]logscan.ContextRow, error) {
	return a.session.GetContext(fileID, offset, before, after)
}

// ValidateRegex 校验正则；返回空串表示合法，否则为错误说明
func (a *App) ValidateRegex(s string) string {
	if s == "" {
		return ""
	}
	if _, err := logscan.CompileRegexForValidation(s); err != nil {
		return err.Error()
	}
	return ""
}

// ExportResults 导出当前结果（format: txt | csv），返回导出条数
func (a *App) ExportResults(path, format string) (int, error) {
	if path == "" {
		return 0, fmt.Errorf("未选择保存位置")
	}
	return a.session.ExportResults(path, format)
}

// SaveFileDialog 导出用：弹出系统保存对话框，返回所选路径（取消返回空串）
func (a *App) SaveFileDialog(defaultName string, ext string) string {
	p, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Filters: []runtime.FileFilter{
			{DisplayName: ext + " 文件", Pattern: "*." + ext},
		},
	})
	if err != nil {
		return ""
	}
	return p
}

// PickFiles 弹系统多选文件对话框，返回所选路径（取消返回空数组）
func (a *App) PickFiles() []string {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择日志文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "日志文件", Pattern: "*.log;*.txt;*.out;*.log.*;*.trace;*.zip"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return []string{}
	}
	return paths
}

// Cancel 取消进行中的加载或查询
func (a *App) Cancel() { a.session.CancelAll() }
