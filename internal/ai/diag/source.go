package diag

// 目录访问授权（human-in-the-loop）：日志堆栈指向代码、或需要读项目文档/配置时，专家可请求读文件——
// 无授权时经 AskAuth（绑定层注入：弹授权卡并阻塞等用户回执，点击后本次调用继续）征得同意。
// 安全边界：只读、目录级授权、会话内拒绝记忆（拒后不得纠缠）、穿越授权目录一律拒绝。
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AuthResult 授权回执（AskAuth 返回）：Allow=放行；Remember=用户选择持久记住
type AuthResult struct {
	Allow    bool
	Remember bool
}

const (
	srcMaxBytes = 2 << 20 // 单文件读取上限 2MB（超出截断并告知）
	srcMaxLines = 400     // 默认读取行数（给了行号则取行段 ±50 上下文）
	srcCtxLines = 50
)

// readSourceExec read_source 工具执行（execTool 内置 dispatch）：
// 授权检查 → （未授权时）AskAuth 阻塞征询（挂起等用户点授权卡，回执后继续）→
// 读文件片段（带行号输出，供 path:line 引用）
func (d *Diagnoser) readSourceExec(ctx context.Context, s *Session, args string) string {
	var p struct {
		Path      string `json:"path"`
		Reason    string `json:"reason"`
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "参数错误: " + err.Error()
	}
	if p.Path == "" {
		return "缺少 path 参数"
	}
	clean := filepath.Clean(p.Path)
	d.emit(Event{Kind: "tool_result", Text: "read_source", Detail: "开始读取 " + clean})

	// 先取形态：目录→授权目标为自身（曾错授权到父目录，导致子目录反复弹卡）；
	// 文件→授权目标为所在目录
	info, err := os.Stat(clean)
	if err != nil {
		return "读取失败: " + err.Error()
	}
	target := filepath.Dir(clean)
	if info.IsDir() {
		target = clean
	}

	// 会话内已拒绝：直接拒绝并明确告知不得重复请求（防授权骚扰）
	if s.isDeniedDir(target) {
		return "用户此前已拒绝读取该目录，请基于日志继续分析，不要重复请求"
	}
	if !s.isGrantedDir(target, d) {
		if d.AskAuth == nil {
			return "当前环境无授权通道，无法读取文件"
		}
		// 阻塞征询（编程 agent 标准模式）：工具调用挂起等用户点击授权卡，回执后
		// 本次调用直接继续读取。曾改「发卡即返回提示模型稍后重试」，模型实测
		// 直接收尾放弃（用户点授权已无下文），不可用
		d.emit(Event{Kind: "tool_result", Text: "read_source", Detail: "等待用户授权…（授权卡已弹出，点击后自动继续读取）"})
		r := d.AskAuth(ctx, target, p.Reason, false)
		if ctx.Err() != nil {
			// 本轮被停止/整轮超时：不是用户拒绝——不登记拒绝，避免下轮误判
			return "本轮已取消（未完成授权），如需读取请在下一轮重试"
		}
		if !r.Allow {
			s.denyDir(target)
			return "用户拒绝了对该目录的访问（本会话内不再询问）。请基于日志继续分析，不要重复请求该目录"
		}
		s.grantDir(target)
		if r.Remember && d.Conf != nil {
			d.Conf.GrantDir(target) // 持久记住（ai.json）；失败不阻断——会话集已生效
		}
		d.emit(Event{Kind: "tool_result", Text: "read_source", Detail: "已授权目录（整棵子树）" + target})
	}
	// 敏感文件（yml/env/json/密钥类，可能含凭证）：目录授权不覆盖——逐文件、
	// 逐次授权（无「记住」选项，每次读取都问；拒绝后本会话不再问该文件）
	if sensitiveFile(clean) {
		if s.isDeniedFile(clean) {
			return "用户此前已拒绝读取该敏感文件，不要重复请求"
		}
		if d.AskAuth == nil {
			return "当前环境无授权通道，无法读取敏感文件"
		}
		d.emit(Event{Kind: "tool_result", Text: "read_source", Detail: "等待用户授权…（敏感文件，每次读取均需单独确认）"})
		r := d.AskAuth(ctx, clean, p.Reason+"（敏感文件：可能包含密钥/凭证，每次读取均需单独授权）", true)
		if ctx.Err() != nil {
			return "本轮已取消（未完成授权），如需读取请在下一轮重试"
		}
		if !r.Allow {
			s.denyFile(clean)
			return "用户拒绝了该敏感文件的读取（本会话内不再询问），请基于日志继续"
		}
		d.emit(Event{Kind: "tool_result", Text: "read_source", Detail: "已授权敏感文件（仅本次） " + filepath.Base(clean)})
	}

	// 路径必须落在授权目标内（防穿越：Clean 后前缀判定 + 边界）
	if !underDir(clean, target) {
		return "路径越界（不在授权目录内），已拒绝读取"
	}

	if info.IsDir() {
		// 目录：返回一层清单（≤200 项），AI 据此再 read_source 具体文件——
		// 「拖入文件夹给 AI」的配套能力
		ents, err := os.ReadDir(clean)
		if err != nil {
			return "读取目录失败: " + err.Error()
		}
		var out strings.Builder
		fmt.Fprintf(&out, "目录 %s（%d 项，展示前 200）：\n", clean, len(ents))
		n := 0
		for _, e := range ents {
			if n >= 200 {
				fmt.Fprintf(&out, "…（其余 %d 项略）", len(ents)-n)
				break
			}
			kind := ""
			if e.IsDir() {
				kind = "/"
			}
			fmt.Fprintf(&out, "%s%s\n", e.Name(), kind)
			n++
		}
		return out.String()
	}
	if info.Size() > int64(srcMaxBytes)<<2 {
		return fmt.Sprintf("文件过大（%d MB，上限 8MB），请用 startLine/endLine 分段读取", info.Size()>>20)
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		return "读取失败: " + err.Error()
	}
	if len(b) > srcMaxBytes {
		b = b[:srcMaxBytes]
	}
	// 二进制防御：前 16KB 含 NUL 判非文本（与日志文件判定同口径）
	probe := b
	if len(probe) > 16<<10 {
		probe = probe[:16<<10]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return "该文件是二进制文件，无法作为源码读取"
	}

	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	lo, hi := 1, len(lines)
	if p.StartLine > 0 {
		lo = p.StartLine - srcCtxLines
		if lo < 1 {
			lo = 1
		}
		hi = p.EndLine + srcCtxLines
		if p.EndLine <= 0 {
			hi = p.StartLine + srcCtxLines
		}
		if hi > len(lines) {
			hi = len(lines)
		}
	} else if len(lines) > srcMaxLines {
		hi = srcMaxLines
	}
	if lo > len(lines) {
		return fmt.Sprintf("startLine %d 超出文件总行数 %d", p.StartLine, len(lines))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s（共 %d 行，展示 %d-%d）：\n", clean, len(lines), lo, hi)
	for i := lo - 1; i < hi; i++ {
		fmt.Fprintf(&out, "%s:%d | %s\n", filepath.Base(clean), i+1, lines[i])
	}
	if hi < len(lines) {
		fmt.Fprintf(&out, "…（后续 %d 行略，需要时用 startLine/endLine 继续读取）", len(lines)-hi)
	}
	return out.String()
}

// findFilesExec find_files 工具执行：在已授权目录（会话 + 持久）内按文件名匹配。
// 安全边界：只搜授权范围（不扩权、不提示未授权目录的存在）——模型拿堆栈里的类名
// 定位源码路径的配套工具，找到后仍走 read_source（含敏感文件逐次授权）
func (d *Diagnoser) findFilesExec(s *Session, args string) string {
	var p struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "参数错误: " + err.Error()
	}
	pat := strings.TrimSpace(p.Pattern)
	if pat == "" {
		return "缺少 pattern 参数"
	}
	// 授权根目录合并（会话集 + 持久集，去重；拒过的目录不搜）
	var roots []string
	seen := map[string]bool{}
	s.mu.Lock()
	for g := range s.grantedDirs {
		if !seen[g] {
			seen[g] = true
			roots = append(roots, g)
		}
	}
	s.mu.Unlock()
	if d.Conf != nil {
		for _, g := range d.Conf.Get().GrantedDirs {
			if !seen[g] {
				seen[g] = true
				roots = append(roots, g)
			}
		}
	}
	if len(roots) == 0 {
		return "尚无已授权目录——先 read_source 一个路径触发授权，授权后即可按名查找"
	}
	// 匹配语义：含通配符走 filepath.Match；纯片段按小写包含。剪枝跳过
	// .git/node_modules/target 等大而无果目录（万级文件的工程毫秒级走完）
	hasWild := strings.ContainsAny(pat, "*?")
	lower := strings.ToLower(pat)
	var hits []string
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil // 不可读路径跳过（不中断整个查找）
			}
			if de.IsDir() {
				if path != root {
					n := de.Name()
					if strings.HasPrefix(n, ".") || n == "node_modules" || n == "target" || n == "build" || n == "dist" {
						return fs.SkipDir
					}
				}
				return nil
			}
			name := de.Name()
			ok := false
			if hasWild {
				// 通配分支两侧同 lower 再 Match——与片段分支的大小写不敏感语义一致
				ok, _ = filepath.Match(lower, strings.ToLower(name))
			} else {
				ok = strings.Contains(strings.ToLower(name), lower)
			}
			if ok {
				hits = append(hits, path)
			}
			return nil
		})
	}
	if len(hits) == 0 {
		return "授权目录内没有匹配 " + pat + " 的文件（可用 read_source 读目录清单确认结构）"
	}
	capped := len(hits) > findFilesCap
	if capped {
		hits = hits[:findFilesCap]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "授权目录内匹配 %s 共 %d 个文件%s：\n", pat, len(hits), map[bool]string{true: "（达上限截断，请用更具体的片段）", false: ""}[capped])
	for _, h := range hits {
		out.WriteString(h)
		out.WriteString("\n")
	}
	return out.String()
}

// findFilesCap find_files 返回路径数上限：定位工具不是清单工具
const findFilesCap = 50

// sensitiveFile 敏感文件判定：可能携带密钥/凭证的配置与凭据文件——
// 目录授权不覆盖，逐文件逐次授权（从严：宁可多问一次，不可漏一个 key）
func sensitiveFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	ext := filepath.Ext(name)
	switch ext {
	case ".yml", ".yaml", ".properties", ".json", ".pem", ".key", ".p12", ".pfx", ".keystore":
		return true
	}
	// .env / .env.* / *.env 与凭据类命名
	if strings.HasSuffix(name, ".env") || strings.HasPrefix(name, ".env.") {
		return true
	}
	for _, kw := range []string{"credential", "secret", "token", "password", "id_rsa"} {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

// underDir 路径是否在目录内（含相等；Clean 后前缀 + 边界分隔符判定）
func underDir(path, dir string) bool {
	if path == dir {
		return true
	}
	if dir == "." || dir == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// ---- Session 授权集（锁内访问）----

func (s *Session) isDeniedDir(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return anyPrefixMatch(s.deniedDirs, dir)
}

func (s *Session) isDeniedFile(f string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deniedFiles != nil && s.deniedFiles[f]
}

func (s *Session) denyFile(f string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deniedFiles == nil {
		s.deniedFiles = make(map[string]bool, 2)
	}
	s.deniedFiles[f] = true
}

func (s *Session) denyDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deniedDirs == nil {
		s.deniedDirs = make(map[string]bool, 2)
	}
	s.deniedDirs[dir] = true
}

func (s *Session) grantDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grantedDirs == nil {
		s.grantedDirs = make(map[string]bool, 2)
	}
	s.grantedDirs[dir] = true
}

// isGrantedDir 会话授权集或持久授权集（ai.json）命中即真。
// 递归语义：授权的是目录整棵子树——授权 D:\proj 后其下任何层级的子目录文件都
// 不再询问（否则 AI 每读一个子目录弹一次卡，授权就没意义了）
func (s *Session) isGrantedDir(dir string, d *Diagnoser) bool {
	s.mu.Lock()
	granted := anyPrefixMatch(s.grantedDirs, dir)
	s.mu.Unlock()
	if granted {
		return true
	}
	if d.Conf != nil {
		c := d.Conf.Get()
		for _, g := range c.GrantedDirs {
			if underDir(dir, g) {
				return true
			}
		}
	}
	return false
}

// anyPrefixMatch dir 是否落在集合中任一目录之下（含相等）
func anyPrefixMatch(set map[string]bool, dir string) bool {
	for g := range set {
		if underDir(dir, g) {
			return true
		}
	}
	return false
}
