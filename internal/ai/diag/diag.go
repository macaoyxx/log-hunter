// Package diag 内置日志分析专家的编排层（纯库，无 Wails 依赖）：
// agent 循环（function calling）、多轮会话状态、视觉中转路由、受控文档产出。
// 取证工具的执行器由绑定层注入（本包不 import logscan 的 Session——只读其报告类型），
// 依赖方向：diag → client / conf / logscan(类型) ，绑定层 → diag。
package diag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"log-hunter/internal/ai/client"
	"log-hunter/internal/ai/conf"
	"log-hunter/internal/logscan"
)

// ---- 事件 ----

// Event 推给 UI 的事件（绑定层转 Wails EventsEmit "ai:event"）。
// Seq 为轮内单调递增序号（每轮 Diagnose/Followup 入口重置）：事件桥不保证投递顺序，
// 前端按 Seq 连续应用、乱序缓存（见 AiPanel.onEvent），防流式文本交错
type Event struct {
	Kind   string `json:"kind"` // delta | reasoning | tool_call | tool_result | doc | vision
	Seq    int    `json:"seq"`
	Text   string `json:"text"`   // delta/reasoning 增量、tool 名、vision 转述、doc 文件名
	Detail string `json:"detail"` // tool_call 参数、tool_result 结果摘要、doc 绝对路径
}

// ---- 工具 ----

// ToolFunc 工具执行器：args 为模型给的 JSON 字符串，返回结果文本（回填给模型）
type ToolFunc func(ctx context.Context, args string) string

// Tool 注册的工具：定义（发给模型的 schema）+ 执行器（绑定层注入）
type Tool struct {
	Def  client.ToolDefinition
	Exec ToolFunc
}

// ---- 会话 ----

// Session 一次诊断会话（首轮 + 追问共享历史）。绑定层持有一个实例；
// busy 保证同一会话同时只有一个请求在途
type Session struct {
	mu        sync.Mutex
	busy      bool
	msgs      []client.Message // 完整历史（system + 首轮基线 + 对话 + 工具往返）
	Started   time.Time        // 会话创建时刻（报告目录名）
	Docs      []string         // 产出文档（文件名，展示用）
	reportDir string           // write_doc 落点（懒建）
	BaseTotal int              // 诊断时结果集 stats.Total 快照（绑定层做失效检测）
	cancel    context.CancelFunc

	grantedDirs map[string]bool // 会话级源码授权目录（用户「仅本次允许」）
	deniedDirs  map[string]bool // 会话内被拒目录（防授权骚扰：拒后不再询问）
	deniedFiles map[string]bool // 会话内被拒敏感文件（yml/env/json 等，拒后不再询问）
}

// Snapshot 锁内快照（外部读唯一正道：Docs/BaseTotal/msgs 均有并发写方——
// 曾有绑定方法裸读 Docs 构成 data race）
func (s *Session) Snapshot() (docs []string, baseTotal int, msgs []client.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	docs = append([]string(nil), s.Docs...)
	msgs = append([]client.Message(nil), s.msgs...)
	return docs, s.BaseTotal, msgs
}

// Reset 清空会话（新诊断或结果集变化失效时调用）
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = nil
	s.Docs = nil
	s.reportDir = ""
	s.BaseTotal = 0
	s.grantedDirs = nil
	s.deniedDirs = nil
	s.deniedFiles = nil
}

// Cancel 取消在途请求
func (s *Session) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// busy 进入临界区（返回 false = 已有请求在途）；done 收尾
func (s *Session) enter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

func (s *Session) leave() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

// ---- 基线输入（绑定层收集）----

// Base 首轮诊断基线：绑定层从 session 收集后传入（本包不触碰 logscan.Session）
type Base struct {
	FileList    string
	Stats       string
	TimeRange   string
	Cluster     *logscan.ClusterReport
	ResultTotal int // stats.Total 快照
}

// Report 单次请求的结果
type Report struct {
	Text string   // 最终回复全文（markdown）
	Docs []string // 本次会话累计产出文档
}

// ---- 编排器 ----

// Diagnoser 诊断编排器（绑定层持有一个实例）
type Diagnoser struct {
	Conf     *conf.Store      // 配置（端点/effort/预算）
	Tools    []Tool           // 取证工具（绑定层注入）
	Emit     func(Event)      // 事件出口（绑定层转 Wails 事件；nil 丢弃）
	DataDir  string           // reports 根目录（exe 同级 data；测试注入临时目录）
	MaxSteps int              // agent 步数上限（<=0 用默认 10）
	MaxImage int64            // 单图字节上限（<=0 用默认 5MB）
	Now      func() time.Time // 注入时钟（测试；nil 用 time.Now）

	seq atomic.Int64 // 事件序号（每轮入口重置；emit 内递增）

	// AskAuth 源码读取授权征询（绑定层注入：弹授权卡并阻塞等用户回执；nil=无通道一律拒绝）。
	// sensitive=true 为敏感文件逐次授权（授权卡无「记住」选项）。ctx 取消=用户停止，
	// 返回空 AuthResult 且调用方按「取消」处理（不登记拒绝）
	AskAuth func(ctx context.Context, target, reason string, sensitive bool) AuthResult
}

const (
	defaultMaxSteps = 16
	defaultMaxImage = 5 << 20
	// charsPerToken 中英混合粗估（中文 1.5~2 字符/token、英文 4 字符/token 的折中）
	charsPerToken = 3
	// budgetFactor 上下文组装占窗口的比例：留 30% 给回复与多轮历史
	budgetFactor = 7 / 10
	// historyFactor 多轮历史超预算阈值（3× 字符预算后截最早轮）
	historyFactor = 3
)

func (d *Diagnoser) emit(e Event) {
	e.Seq = int(d.seq.Add(1))
	if d.Emit != nil {
		d.Emit(e)
	}
}

func (d *Diagnoser) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Diagnoser) maxSteps() int {
	if d.MaxSteps > 0 {
		return d.MaxSteps
	}
	return defaultMaxSteps
}

// ---- 首轮诊断 ----

// Diagnose 首轮：system（专家人格）+ 基线上下文 → agent 循环。
// 会话已有历史时先 Reset（新诊断覆盖旧会话）
func (d *Diagnoser) Diagnose(ctx context.Context, s *Session, profileID, model, effort string, base Base) (Report, error) {
	if !s.enter() {
		return Report{}, errors.New("上一条请求仍在进行，请先取消或等待完成")
	}
	defer s.leave()
	d.seq.Store(0) // 轮内事件序号从 1 重新计（前端按序重组的基准）
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.cancel = cancel
	s.msgs = nil
	s.Docs = nil
	s.reportDir = ""
	s.BaseTotal = base.ResultTotal
	s.Started = d.now()
	s.mu.Unlock()

	c := d.Conf.Get()
	p := c.FindProfile(profileID)
	ep, err := c.Endpoint(profileID)
	if err != nil {
		return Report{}, err
	}
	cl := client.NewClient(ep)
	budget := c.ResolveCtx(p, model) * charsPerToken * budgetFactor

	s.mu.Lock()
	s.msgs = []client.Message{
		client.TextMessage("system", expertSystemPrompt),
		client.TextMessage("user", buildBaseMessage(base, budget)),
	}
	msgs := s.msgs
	s.mu.Unlock()

	text, err := d.agentLoop(ctx, cl, model, effort, &c, p, s, msgs)
	return d.report(s, text, err)
}

// Followup 追问：历史延续（切模型=换参重发，历史原样）。图片路由：
// 主模型 vision=true 直发 image_url；false 走视觉档案转述（未配视觉档案报错）。
// images 为 dataURL（data:image/…;base64,…）
func (d *Diagnoser) Followup(ctx context.Context, s *Session, profileID, model, effort, text string, images []string, resultTotal int) (Report, error) {
	if !s.enter() {
		return Report{}, errors.New("上一条请求仍在进行，请先取消或等待完成")
	}
	defer s.leave()
	d.seq.Store(0)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.cancel = cancel
	msgs := s.msgs
	s.mu.Unlock()
	if len(msgs) == 0 {
		// 无会话直接问：以纯问答开局（专家人格 + 用户消息，不带聚类基线）——
		// 「复制一段日志直接问」是合法用法；AI 仍可用工具自主取证
		msgs = []client.Message{
			client.TextMessage("system", expertSystemPrompt),
		}
		s.mu.Lock()
		s.msgs = msgs
		// 冷启动同样锚定结果集基线：后续结果变化时旧引用失效检测照常生效
		//（此前漏设导致 BaseTotal 恒 0，失效校验被永久绕开）
		s.BaseTotal = resultTotal
		s.mu.Unlock()
	}

	c := d.Conf.Get()
	p := c.FindProfile(profileID)
	ep, err := c.Endpoint(profileID)
	if err != nil {
		return Report{}, err
	}
	cl := client.NewClient(ep)

	// 组装本轮 user 消息：文本 +（按路由处理后的）图片
	parts := []client.ContentPart{client.TextPart(text)}
	for i, img := range images {
		if n := int64(len(img)); n > d.maxImageBytes() {
			return Report{}, fmt.Errorf("图片 %d 超过大小上限（%d MB）", i+1, d.maxImageBytes()>>20)
		}
		if c.ResolveVision(p, model) {
			parts = append(parts, client.ImagePart(img)) // 主模型多模态：直发
			continue
		}
		// 纯文本主模型：视觉档案中转（防瞎编三道闸之道三：转述推给 UI 供用户核对）
		desc, err := d.describeImage(ctx, &c, img)
		if err != nil {
			return Report{}, fmt.Errorf("视觉转述失败（可在设置中配置视觉档案，或换多模态主模型）：%w", err)
		}
		d.emit(Event{Kind: "vision", Text: desc})
		parts = append(parts, client.TextPart(fmt.Sprintf(visionPrefix, i+1)+desc))
	}
	s.mu.Lock()
	s.msgs = append(s.msgs, client.PartsMessage("user", parts...))
	msgs = s.msgs
	s.mu.Unlock()

	out, err := d.agentLoop(ctx, cl, model, effort, &c, p, s, msgs)
	return d.report(s, out, err)
}

// describeImage 视觉中转：视觉模型按转述规则读图
func (d *Diagnoser) describeImage(ctx context.Context, c *conf.Conf, dataURL string) (string, error) {
	ep, model, err := c.VisionEndpoint()
	if err != nil {
		return "", err
	}
	out, err := client.NewClient(ep).ChatOnce(ctx, model, []client.Message{
		client.TextMessage("system", visionSystemPrompt),
		client.PartsMessage("user", client.ImagePart(dataURL)),
	}, 4096)
	if err != nil {
		return "", err
	}
	return out, nil
}

// ---- agent 循环 ----

// agentLoop 核心循环：ChatStream → tool_calls 执行回填 → 续步，直到无工具调用的最终回复
// 或达步数上限。msgs 为发送快照（锁外取）；循环内更新 s.msgs
func (d *Diagnoser) agentLoop(ctx context.Context, cl *client.Client, model, effort string, c *conf.Conf, p *conf.Profile, s *Session, msgs []client.Message) (string, error) {
	maxOut := c.ResolveMaxOut(p, model)
	budget := c.ResolveCtx(p, model) * charsPerToken * budgetFactor
	for step := 0; step < d.maxSteps(); step++ {
		// 步数预算过半注入收敛提醒：真实模型倾向于把工具用满，不提醒就会
		// 到上限被截断（实测 GLM 十步取证不收尾）——余量 1/3 时提示基于已有证据收口
		if step > 0 && step == d.maxSteps()*2/3 {
			s.mu.Lock()
			// Name=sys-hint 为结构化标记：前端重建会话时据此过滤本条（不再依赖文案前缀契约）
			m := client.TextMessage("user",
				"（系统提示：取证步数预算将尽，请停止继续检索，基于已获得的证据直接输出最终报告）")
			m.Name = "sys-hint"
			s.msgs = append(s.msgs, m)
			msgs = s.msgs
			s.mu.Unlock()
		}
		send := s.trimHistory(budget) // 超预算截最早追问轮（轮边界安全）
		var stepErr error
		msg, err := cl.ChatStream(ctx, model, send, d.toolDefs(), effort, maxOut, client.StreamCallbacks{
			OnDelta:     func(t string) { d.emit(Event{Kind: "delta", Text: t}) },
			OnReasoning: func(t string) { d.emit(Event{Kind: "reasoning", Text: t}) },
			OnToolCall: func(tc client.ToolCall) {
				d.emit(Event{Kind: "tool_call", Text: tc.Function.Name, Detail: tc.Function.Arguments})
			},
		})
		if err != nil {
			stepErr = err
		}
		if stepErr != nil {
			return "", stepErr
		}
		s.mu.Lock()
		s.msgs = append(s.msgs, msg)
		s.mu.Unlock()
		if len(msg.ToolCalls) == 0 {
			if strings.TrimSpace(contentText(msg)) == "" {
				// 空回复（无正文无工具调用）：网关偶发——静默当最终回复会让流无声停止，
				// 用户无从分辨；报明确错误让红条可见，可重发推进
				return "", fmt.Errorf("模型返回了空回复（无正文/无工具调用，可能是网关或模型异常），请重新发送或换模型重试")
			}
			return contentText(msg), nil // 最终回复
		}
		// 执行全部工具调用并回填（write_doc 内置实现，其余走注册表）
		for _, tc := range msg.ToolCalls {
			result := d.execTool(ctx, s, tc)
			d.emit(Event{Kind: "tool_result", Text: tc.Function.Name, Detail: clip(result, 300)})
			s.mu.Lock()
			s.msgs = append(s.msgs, client.ToolMessage(tc.ID, tc.Function.Name, result))
			s.mu.Unlock()
		}
	}
	return "", fmt.Errorf("已达步数上限（%d 步），专家尚未收尾——可发送「继续/给出最终结论」推进", d.maxSteps())
}

// toolDefs 发给模型的工具 schema
func (d *Diagnoser) toolDefs() []client.ToolDefinition {
	defs := make([]client.ToolDefinition, 0, len(d.Tools)+2)
	for _, t := range d.Tools {
		defs = append(defs, t.Def)
	}
	defs = append(defs, writeDocTool.Def, readSourceTool.Def) // 内置：write_doc / read_source
	return defs
}

// execTool 执行一次工具调用：write_doc 本包实现；其余查注册表（未知名回填错误文本，
// 不中断循环——让模型看到错误自行调整）
func (d *Diagnoser) execTool(ctx context.Context, s *Session, tc client.ToolCall) string {
	name := tc.Function.Name
	if name == readSourceTool.Def.Function.Name {
		return d.readSourceExec(ctx, s, tc.Function.Arguments)
	}
	if name == writeDocTool.Def.Function.Name {
		var args struct {
			Filename string `json:"filename"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "参数错误: " + err.Error()
		}
		path, err := s.writeDoc(d, args.Filename, args.Content)
		if err != nil {
			return "写入失败: " + err.Error()
		}
		d.emit(Event{Kind: "doc", Text: filepath.Base(path), Detail: path})
		return "已保存: " + filepath.Base(path)
	}
	for _, t := range d.Tools {
		if t.Def.Function.Name == name {
			return t.Exec(ctx, tc.Function.Arguments)
		}
	}
	return fmt.Sprintf("未知工具 %q（可用：%s）", name, d.toolNames())
}

func (d *Diagnoser) toolNames() string {
	names := make([]string, 0, len(d.Tools)+1)
	for _, t := range d.Tools {
		names = append(names, t.Def.Function.Name)
	}
	names = append(names, writeDocTool.Def.Function.Name, readSourceTool.Def.Function.Name)
	return strings.Join(names, ", ")
}

// report 收尾：错误也返回已产出的文档清单
func (d *Diagnoser) report(s *Session, text string, err error) (Report, error) {
	s.mu.Lock()
	docs := append([]string(nil), s.Docs...)
	s.cancel = nil
	s.mu.Unlock()
	if err != nil {
		return Report{Docs: docs}, err
	}
	return Report{Text: text, Docs: docs}, nil
}

// trimHistory 多轮历史超预算截断（锁内读 msgs 返回发送快照）。
// 截断点必须落在 user 消息边界——OpenAI 协议要求 tool 消息紧跟带 tool_calls 的
// assistant 消息，从中间切断会 400。保留：system + 首轮基线 + 尾部若干完整轮
func (s *Session) trimHistory(charsBudget int) []client.Message {
	limit := charsBudget * historyFactor
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, m := range s.msgs {
		total += msgChars(m)
	}
	if total <= limit || len(s.msgs) <= 2 {
		return s.msgs
	}
	// 轮边界：首轮 user（index 1）之后的每个 user 消息为追问轮起点
	roundStarts := []int{1}
	for i := 2; i < len(s.msgs); i++ {
		if s.msgs[i].Role == "user" {
			roundStarts = append(roundStarts, i)
		}
	}
	// 从最早追问轮起整轮丢弃，直到预算内（至少保留最后一轮）
	for ri := 1; ri < len(roundStarts)-1; ri++ {
		cut := s.msgs[roundStarts[ri]:roundStarts[ri+1]]
		cutLen := 0
		for _, m := range cut {
			cutLen += msgChars(m)
		}
		if total-cutLen <= limit || ri == len(roundStarts)-2 {
			kept := append([]client.Message{}, s.msgs[:1]...)
			kept = append(kept, client.TextMessage("user", "（此前的对话因长度超限已省略，诊断基线见上）"))
			kept = append(kept, s.msgs[roundStarts[ri]:]...)
			s.msgs = kept
			return kept
		}
		total -= cutLen
	}
	return s.msgs
}

// msgChars 消息体量粗估（文本 + 工具参数）
func msgChars(m client.Message) int {
	n := 0
	if s, ok := m.Content.(string); ok {
		n += len(s)
	}
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Arguments) + len(tc.Function.Name)
	}
	return n
}

// ---- 受控文档产出 ----

// writeDoc 落盘到本次会话报告目录：文件名清洗（去路径分隔与控制字符，白名单外替换），
// 同名自动加序号；目录锁内懒建（data/reports/diag-<时间戳>/）
func (s *Session) writeDoc(d *Diagnoser, name, content string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := sanitizeFilename(name)
	if clean == "" {
		clean = "report.md"
	}
	if s.reportDir == "" {
		root := filepath.Join(d.DataDir, "reports")
		s.reportDir = filepath.Join(root, "diag-"+s.Started.Format("20060102-150405"))
		if err := os.MkdirAll(s.reportDir, 0o755); err != nil {
			return "", err
		}
	}
	final := clean
	for i := 2; ; i++ {
		p := filepath.Join(s.reportDir, final)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			s.Docs = append(s.Docs, final)
			return p, os.WriteFile(p, []byte(content), 0o644)
		}
		final = fmt.Sprintf("%s(%d)%s", trimExt(clean), i, filepath.Ext(clean))
	}
}

// sanitizeFilename 文件名清洗：路径分隔/控制字符/Windows 保留字符全部替换为 _，
// 长度封顶 80——防路径穿越与非法名（write_doc 的落点安全边界之一，之二在目录锁死）
func sanitizeFilename(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range name {
		switch {
		case unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
		n++
		if n >= 80 {
			break
		}
	}
	return strings.TrimRight(strings.TrimSpace(b.String()), " .") // Windows 结尾不能是点/空格
}

func trimExt(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func (d *Diagnoser) maxImageBytes() int64 {
	if d.MaxImage > 0 {
		return d.MaxImage
	}
	return defaultMaxImage
}

// contentText 消息文本内容（Content 为 any：string 或多模态分段）
func contentText(m client.Message) string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	return ""
}
