package main

// AI 诊断绑定层：薄胶水——工具执行器注入（包装 logscan 影子查询）、diag 事件转
// Wails 事件、配置接口。所有逻辑在 internal/ai/*（client/conf/diag）与 logscan，
// 本文件只做参数转换与转发（依赖单向：main → diag → client/conf/logscan）

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"log-hunter/internal/ai/client"
	"log-hunter/internal/ai/conf"
	"log-hunter/internal/ai/diag"
	"log-hunter/internal/logscan"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// initAI 组装 AI 诊断设施（NewApp 调用；Emit 闭包引用 a.ctx，startup 前不会被触发）。
// 配置存储打开失败保持 nil——各绑定方法经 aiReady 保护
func (a *App) initAI(dataDir string) {
	aiConf, err := conf.Open()
	if err != nil {
		return
	}
	a.aiConf = aiConf
	a.aiSess = &diag.Session{}
	a.aiAuthPending = make(map[string]chan diag.AuthResult)
	a.aiDiag = &diag.Diagnoser{
		Conf:    aiConf,
		Tools:   a.aiToolset(),
		Emit:    func(e diag.Event) { runtime.EventsEmit(a.ctx, "ai:event", e) },
		DataDir: dataDir,
		AskAuth: a.askSourceAuth, // 源码读取授权：弹卡并挂起等用户回执
	}
}

// authWaitTimeout 单次授权征询的兜底等待上限（正常路径由 AiDiagnose/Followup 的
// 10 分钟 ctx 先到；此处防 Background ctx 的调用方无限挂起）
const authWaitTimeout = 15 * time.Minute

// askSourceAuth 阻塞授权（编程 agent 标准模式）：注册回执通道 → 弹授权卡 →
// 挂起等用户点击，回执到达后 read_source 立即继续执行。
// 曾误诊「阻塞会死锁绑定队列」改非阻塞（发卡即返回未授权），实测两处皆坏：
// 模型收到「稍后重试」提示后直接收尾放弃（点击授权已无下文）；且敏感文件
// 路径把空回执当拒绝、卡未弹出已判死。真实机制：Wails 每条绑定调用经
// `go dispatchMessage` 独立 goroutine 派发（v2.17 frontend.go:762），
// 诊断在途时 CancelAi 一直可用即反证——阻塞等待完全可行
func (a *App) askSourceAuth(ctx context.Context, target, reason string, sensitive bool) diag.AuthResult {
	id := "auth-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ch := make(chan diag.AuthResult, 1) // 带缓冲：回执先到/等待方退出都不阻塞投递方
	a.aiAuthMu.Lock()
	a.aiAuthPending[id] = ch
	a.aiAuthMu.Unlock()
	defer func() {
		a.aiAuthMu.Lock()
		delete(a.aiAuthPending, id)
		a.aiAuthMu.Unlock()
	}()
	runtime.EventsEmit(a.ctx, "ai:auth", map[string]any{
		"id": id, "path": target, "reason": reason, "sensitive": sensitive})
	select {
	case r := <-ch:
		return r
	case <-ctx.Done(): // 用户点了停止/整轮超时：按取消处理（diag 侧不登记拒绝）
		return diag.AuthResult{}
	case <-time.After(authWaitTimeout): // 用户长时间未响应：按拒绝处理
		return diag.AuthResult{}
	}
}

// AiAuthRespond 授权卡回执（前端按钮调用）：把用户决定投递给挂起中的 read_source。
// 授权集的更新（会话 grant/deny、remember 持久化 ai.json）在 diag 层锁内完成——
// 本方法只做回执转发；找不到等待方（本轮已取消/超时结束）返回 false
func (a *App) AiAuthRespond(reqID string, allow, remember bool) bool {
	a.aiAuthMu.Lock()
	ch := a.aiAuthPending[reqID]
	a.aiAuthMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- diag.AuthResult{Allow: allow, Remember: remember}:
		return true
	default: // 通道已占用（重复点击）：按已送达处理
		return true
	}
}

// aiReady AI 设施可用性（配置存储打开失败时全部入口拒绝而非 panic）
func (a *App) aiReady() error {
	if a.aiConf == nil || a.aiDiag == nil {
		return fmt.Errorf("AI 配置存储不可用")
	}
	return nil
}

// ---------- 配置 ----------

// GetAiConfig AI 诊断配置（档案/默认选择/effort/全局默认）
func (a *App) GetAiConfig() conf.Conf {
	if a.aiConf == nil {
		return conf.Conf{}
	}
	return a.aiConf.Get()
}

// SetAiConfig 保存配置；新增模型条目（ctx/maxOut 全零）按内置能力表预填，已有条目不动
func (a *App) SetAiConfig(c conf.Conf) error {
	if err := a.aiReady(); err != nil {
		return err
	}
	for pi := range c.Profiles {
		for mi := range c.Profiles[pi].Models {
			m := &c.Profiles[pi].Models[mi]
			if m.Ctx == 0 && m.MaxOut == 0 {
				// 命中能力表才填确定数字；未命中只勾 vision，数字留空走全局默认
				if caps, hit := conf.LookupCaps(m.Name); hit {
					m.Ctx, m.Vision, m.MaxOut = caps.Ctx, caps.Vision, caps.MaxOut
				} else {
					m.Vision = caps.Vision
				}
			}
		}
	}
	return a.aiConf.Set(c)
}

// AiPullModels 拉取端点模型列表 + 能力预填，合并进指定档案（同名已有条目保留用户值）
// 并持久化，返回该档案合并后的模型列表
func (a *App) AiPullModels(profileID string) ([]conf.Model, error) {
	c := a.aiConf.Get()
	p := c.FindProfile(profileID)
	if p == nil || p.BaseURL == "" {
		return nil, fmt.Errorf("档案未配置端点地址")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	ids, err := client.NewClient(client.Endpoint{BaseURL: p.BaseURL, APIKey: p.APIKey}).
		ListModels(ctx)
	if err != nil {
		return nil, err
	}
	// 合并：已有条目原位保留，新名追加（预填能力）
	have := make(map[string]bool, len(p.Models))
	for _, m := range p.Models {
		have[m.Name] = true
	}
	for _, id := range ids {
		if have[id] {
			continue
		}
		m := conf.Model{Name: id}
		if caps, hit := conf.LookupCaps(id); hit {
			m.Ctx, m.Vision, m.MaxOut = caps.Ctx, caps.Vision, caps.MaxOut
		} else {
			m.Vision = caps.Vision // 数字留空 = 全局默认
		}
		p.Models = append(p.Models, m)
	}
	if err := a.aiConf.Set(c); err != nil {
		return nil, err
	}
	return p.Models, nil
}

// TestAiConn 验证档案连通与鉴权：向指定模型发一条极小请求，返回模型回复
func (a *App) TestAiConn(profileID, model string) (string, error) {
	c := a.aiConf.Get()
	ep, err := c.Endpoint(profileID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	out, err := client.NewClient(ep).ChatOnce(ctx, model,
		[]client.Message{client.TextMessage("user", "回复两个字：正常")}, 16)
	if err != nil {
		return "", err
	}
	return out, nil
}

// ---------- 聚类 ----------

// ClusterResults 当前结果集聚类，进度经 "cluster:progress" 事件推送
func (a *App) ClusterResults() (*logscan.ClusterReport, error) {
	if a.session.Stats().Total == 0 {
		return nil, fmt.Errorf("当前没有查询结果，请先查询或全览")
	}
	return a.session.ClusterResults(func(p logscan.ClusterProgress) {
		runtime.EventsEmit(a.ctx, "cluster:progress", p)
	}), nil
}

// ---------- 诊断 / 追问 ----------

// AiReportOut 单次诊断/追问的返回（前端展示）
type AiReportOut struct {
	Text string   `json:"text"`
	Docs []string `json:"docs"`
}

// AiDiagnose 首轮诊断（profileID+model 由前端显式传入——会话中切模型即换参重发）
func (a *App) AiDiagnose(profileID, model, effort string) (AiReportOut, error) {
	c := a.aiConf.Get()
	if !c.Configured() {
		return AiReportOut{}, fmt.Errorf("AI 未配置，请先在设置中添加端点档案")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	base := a.collectBase()
	rep, err := a.aiDiag.Diagnose(ctx, a.aiSess, profileID, model, effort, base)
	return AiReportOut{Text: rep.Text, Docs: rep.Docs}, err
}

// AiFollowup 追问（可带图片 dataURL）；结果集变化（重查/切视图）后旧会话的行号引用
// 已失准，直接拒绝并要求重新诊断
func (a *App) AiFollowup(profileID, model, effort, text string, images []string) (AiReportOut, error) {
	// 结果集变化校验：冷启动会话在 diag.Followup 内锚定基线后同样受检
	//（BaseTotal==0 且无会话时 diag 层放行纯问答开局并设基线）
	if _, baseTotal, msgs := a.aiSess.Snapshot(); len(msgs) > 0 || baseTotal != 0 {
		if total := a.session.Stats().Total; total != baseTotal {
			return AiReportOut{}, fmt.Errorf("结果集已变化（%d → %d 行），旧引用会指错位置——请重新诊断", baseTotal, total)
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	rep, err := a.aiDiag.Followup(ctx, a.aiSess, profileID, model, effort, text, images, a.session.Stats().Total)
	return AiReportOut{Text: rep.Text, Docs: rep.Docs}, err
}

// CancelAi 取消在途的 AI 请求（断整个 agent 循环）
func (a *App) CancelAi() { a.aiSess.Cancel() }

// AiResetSession 清空上下文重新开始（上下文被污染/换问题重开）：停止在途请求并清会话
func (a *App) AiResetSession() {
	a.aiSess.Cancel()
	a.aiSess.Reset()
}

// AiSessionInfo 会话状态（前端重开面板时恢复历史：产出文档、基线、消息快照）。
// 一律走 Snapshot 锁内快照——Docs/msgs 有并发写方，裸读是 data race
func (a *App) AiSessionInfo() map[string]any {
	docs, baseTotal, msgs := a.aiSess.Snapshot()
	return map[string]any{
		"docs":      docs,
		"baseTotal": baseTotal,
		"msgs":      msgs,
	}
}

// collectBase 从 session 收集诊断基线（文件清单/统计/时间范围/聚类报告）
func (a *App) collectBase() diag.Base {
	stats := a.session.Stats()

	var fb strings.Builder
	for _, e := range a.session.Files() {
		fmt.Fprintf(&fb, "- %s：%s，%s 行", e.Name, humanBytes(e.Size), humanCount(e.LineCnt))
		if e.TSMin > 0 {
			fmt.Fprintf(&fb, "，%s ~ %s",
				time.Unix(0, e.TSMin).Format("2006-01-02 15:04:05"),
				time.Unix(0, e.TSMax).Format("2006-01-02 15:04:05"))
		}
		fb.WriteString("\n")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "共 %s 条命中", humanCount(int64(stats.Total)))
	if stats.Truncated {
		fmt.Fprintf(&sb, "（已达上限 %s 截断）", humanCount(int64(stats.ResultLimit)))
	}
	for lv, n := range stats.LevelCounts {
		if n > 0 {
			fmt.Fprintf(&sb, "，%s %s", logscan.Level(lv), humanCount(int64(n)))
		}
	}

	// 时间范围：首尾结果行（零物化大结果也只取两行，代价可忽略）
	var timeRange string
	if stats.Total > 0 {
		first := a.session.GetResults(0, 1)
		last := a.session.GetResults(stats.Total-1, 1)
		if len(first) > 0 && len(last) > 0 && first[0].TS > 0 && last[0].TS > 0 {
			timeRange = time.Unix(0, first[0].TS).Format("15:04:05") +
				" ~ " + time.Unix(0, last[0].TS).Format("15:04:05.000")
		}
	}

	return diag.Base{
		FileList:    fb.String(),
		Stats:       sb.String(),
		TimeRange:   timeRange,
		Cluster:     a.session.ClusterResults(nil),
		ResultTotal: stats.Total,
	}
}

// ---------- 取证工具执行器（包装 logscan 影子查询，注入 diag）----------

// aiToolset 构造五个只读取证工具（agent 循环经 diag 调用）
func (a *App) aiToolset() []diag.Tool {
	return diag.Toolset(a.toolSearch, a.toolGetContext, a.toolGetLine, a.toolHistogram, a.toolCluster)
}

func (a *App) toolSearch(ctx context.Context, args string) string {
	var p struct {
		Keyword   string `json:"keyword"`
		Regex     string `json:"regex"`
		Levels    []int8 `json:"levels"`
		TimeStart string `json:"timeStart"`
		TimeEnd   string `json:"timeEnd"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "参数错误: " + err.Error()
	}
	if p.Keyword == "" && p.Regex == "" && len(p.Levels) == 0 {
		return "请至少给 keyword / regex / levels 之一"
	}
	start, err := parseAgentTime(p.TimeStart)
	if err != nil {
		return err.Error()
	}
	end, err := parseAgentTime(p.TimeEnd)
	if err != nil {
		return err.Error()
	}
	rows, err := a.session.SideQuery(ctx, &logscan.QuerySpec{
		Keywords: strings.Fields(p.Keyword), Regex: p.Regex, Levels: p.Levels,
		TSStart: start, TSEnd: end,
	}, p.Limit)
	if err != nil {
		return "搜索失败: " + err.Error()
	}
	return formatSideRows(rows, p.Limit)
}

func (a *App) toolGetContext(ctx context.Context, args string) string {
	var p struct {
		File   string `json:"file"`
		Line   int64  `json:"line"`
		Before int64  `json:"before"`
		After  int64  `json:"after"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "参数错误: " + err.Error()
	}
	rows, err := a.session.SideContext(p.File, p.Line, p.Before, p.After)
	if err != nil {
		return "读取失败: " + err.Error()
	}
	return formatSideRows(rows, 0)
}

func (a *App) toolGetLine(ctx context.Context, args string) string {
	var p struct {
		File string `json:"file"`
		Line int64  `json:"line"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "参数错误: " + err.Error()
	}
	row, err := a.session.SideLineText(p.File, p.Line)
	if err != nil {
		return "读取失败: " + err.Error()
	}
	return fmt.Sprintf("%s:%d 完整原文：\n%s", row.File, row.LineNo, row.Text)
}

func (a *App) toolHistogram(ctx context.Context, args string) string {
	h := a.session.TimeHistogram(60)
	if h == nil || !h.HasData {
		return "当前结果集无时间分布（行均无时间戳）"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "时间范围 %s ~ %s（%d 桶 × %s）：\n",
		time.Unix(0, h.Start).Format("15:04:05"),
		time.Unix(0, h.Start+int64(len(h.Buckets))*h.Step).Format("15:04:05"),
		len(h.Buckets), time.Duration(h.Step))
	for i, bk := range h.Buckets {
		total := 0
		for _, n := range bk {
			total += int(n)
		}
		if total == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s  总 %s",
			time.Unix(0, h.Start+int64(i)*h.Step).Format("15:04:05"), humanCount(int64(total)))
		for lv := int(logscan.LevelWarn); lv <= int(logscan.LevelFatal); lv++ {
			if n := bk[lv]; n > 0 { // 只报 WARN+（INFO 噪声不进摘要）
				fmt.Fprintf(&b, "  %s %s", logscan.Level(lv), humanCount(int64(n)))
			}
		}
		b.WriteString("\n")
	}
	if h.NoTS > 0 {
		fmt.Fprintf(&b, "另有 %s 行无时间戳未入桶\n", humanCount(h.NoTS))
	}
	return b.String()
}

func (a *App) toolCluster(ctx context.Context, args string) string {
	var p struct {
		Levels []int8 `json:"levels"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "参数错误: " + err.Error()
	}
	rep, err := a.session.SideCluster(ctx, nil, p.Levels, nil)
	if err != nil {
		return "聚类失败: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "全局聚类：%s 组（参与 %s 行）\n", humanCount(int64(len(rep.Groups))), humanCount(int64(rep.Participated)))
	for i, g := range rep.Groups {
		if i >= 30 {
			fmt.Fprintf(&b, "…（其余 %d 组略）\n", len(rep.Groups)-i)
			break
		}
		fmt.Fprintf(&b, "组%d %s 条", i+1, humanCount(int64(g.Count)))
		if lv := logscan.Level(g.Level); lv >= 0 {
			fmt.Fprintf(&b, " %s", lv)
		}
		fmt.Fprintf(&b, "：%s\n", clipTool(g.Template, 200))
	}
	return b.String()
}

// ---------- 小工具 ----------

// parseAgentTime agent 给的时间（宽松：空格分隔的时间也收）
func parseAgentTime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	nano, err := parseLocalTime(s)
	if err == nil && nano > 0 {
		return nano, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t.UnixNano(), nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t.UnixNano(), nil
	}
	return 0, fmt.Errorf("无法解析时间: %q（格式 2024-01-15 10:23:45）", s)
}

// formatSideRows 影子查询结果 → 给模型的文本（文件名:行号 时间 [级别] 文本）
func formatSideRows(rows []logscan.SideRow, limit int) string {
	if len(rows) == 0 {
		return "没有命中（可放宽条件或换关键字）"
	}
	var b strings.Builder
	if limit > 0 && len(rows) >= limit {
		fmt.Fprintf(&b, "已返回前 %d 条（更多命中未展示，请用更精确的条件）：\n", len(rows))
	}
	for _, r := range rows {
		ts := ""
		if r.TS > 0 {
			ts = time.Unix(0, r.TS).Format("15:04:05.000") + " "
		}
		lv := ""
		if l := logscan.Level(r.Level); l >= 0 {
			lv = "[" + l.String() + "] "
		}
		fmt.Fprintf(&b, "%s:%d %s%s%s\n", r.File, r.LineNo, ts, lv, r.Text)
	}
	return b.String()
}

func clipTool(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func humanCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

// AiResolveRef 按文件名+行号解析为 fileId+offset（AI 引用「[文件:行]」点击时
// 开上下文面板用——面板只认 fileId+offset，名字→坐标的解析在后端）
func (a *App) AiResolveRef(fileName string, line int64) (logscan.SideRow, error) {
	return a.session.SideLineText(fileName, line)
}
