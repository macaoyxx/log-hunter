// Package conf 管理 AI 诊断的厂商档案化配置（exe 同级 data/ai.json，独立于 config.json）。
// 档案模型：N 个端点档案（名称/BaseURL/APIKey/模型列表），主对话与视觉各选档案——
// 跨厂商组合（A 厂商主模型 + B 厂商视觉模型）即两个档案各选各的。
// 会话中切模型不写盘：会话请求显式携带 profileId+model，这里只存档案与默认选择。
//
// 存储口径：明文（用户拍板），原子写（tmp+rename），0600 权限（Windows 下 Go 映射为
// 仅当前用户的 ACL）；模型能力（ctx/vision/maxOut）预填只在条目诞生时发生一次
// （由绑定层调 Prefill：拉取到新名 / 手填名敲定），本包存取不猜、保存不重算。
package conf

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"log-hunter/internal/ai/client"
)

// 默认值（模型/档案未指定时的全局回落）
const (
	DefaultCtxTokens = 1048576 // 1M：新模型窗口普遍 128K~1M，默认给足（组装预算按 ctx×70% 自适应）
	DefaultMaxOut    = 8192    // 8K：诊断报告富余量级
	maxProfiles      = 20      // 档案数上限（防配置面板失控增长）
	maxModelsPerProf = 100
)

// Profile 模型厂商（一条 = 名称/BaseURL/APIKey/模型列表；自建网关也算一个「厂商」位）
type Profile struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	BaseURL string  `json:"baseUrl"`
	APIKey  string  `json:"apiKey"`
	Models  []Model `json:"models"`
}

// Model 模型条目：能力三属性零值 = 未指定，运行时逐级回落（见 Resolve*）。
// 预填只发生在条目诞生时（拉取到新名 / 手填名敲定，见 Prefill），之后条目完全归用户；
// 保存不按名字重算——bool 的 false 与「未指定」同值，按零值判「新增」必覆盖留空条目
// 的视觉勾选（vision 没有数字那种「改过即非零」的痕迹）
type Model struct {
	Name   string `json:"name"`
	Ctx    int    `json:"ctx,omitempty"`
	Vision bool   `json:"vision,omitempty"`
	MaxOut int    `json:"maxOut,omitempty"`
}

// Conf ai.json 根结构
type Conf struct {
	Profiles      []Profile `json:"profiles"`
	ActiveProfile string    `json:"activeProfile,omitempty"`
	ActiveModel   string    `json:"activeModel,omitempty"`
	VisionProfile string    `json:"visionProfile,omitempty"` // 视觉档案（可选，可与主档案不同厂商）
	VisionModel   string    `json:"visionModel,omitempty"`
	Effort        string    `json:"effort,omitempty"`       // ""=不发 | low|medium|high
	CtxTokens     int       `json:"ctxTokens,omitempty"`    // 全局默认窗口
	MaxOutTokens  int       `json:"maxOutTokens,omitempty"` // 全局默认响应上限
	GrantedDirs   []string  `json:"grantedDirs,omitempty"`  // 源码读取授权目录（用户「允许并记住」累计，设置里可移除）
}

// Store ai.json 读写。进程内低频调用，mu 防并发交叉写坏文件（照 appconf 范式）
type Store struct {
	path string
	mu   sync.Mutex
	c    Conf
}

// DefaultPath exe 同级 data/ai.json（开发模式在 build/bin/data 下，已被 gitignore）
func DefaultPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "data", "ai.json"), nil
}

// Open 打开默认位置配置；文件不存在或损坏回落默认值（空档案列表 = 未配置 AI）
func Open() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return openPath(path)
}

// OpenPath 打开指定路径的配置（测试注入用，语义同 Open）
func OpenPath(path string) (*Store, error) {
	return openPath(path)
}

// openPath 指定路径打开（测试注入用）：解析失败静默回落默认，坏文件下次写入时修复
func openPath(path string) (*Store, error) {
	s := &Store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.c) // 解析失败静默回落默认
	}
	return s, nil
}

// Get 当前配置快照（副本）
func (s *Store) Get() Conf {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.confCopy()
	return c
}

// ensureProfiles Profiles 保证非 nil：Go 零值 nil 切片会序列化成 JSON null，
// 前端（生成的 models 构造器对 null 不兜底）拿 null 访问 .length 即渲染崩溃
func ensureProfiles(c *Conf) {
	if c.Profiles == nil {
		c.Profiles = []Profile{}
	}
	// Models 同理：空厂商的 nil 切片会序列化成 null，前端渲染 p.models.length 即崩。
	// 数字能力不代填——「空 = 用全局默认」才是正语义（默认值变更不被固化数据拖住）；
	// 一次性迁移：旧版本曾把当时的默认 128K 代填进数据，非已知模型的 131072 视为代填产物清空
	for i := range c.Profiles {
		if c.Profiles[i].Models == nil {
			c.Profiles[i].Models = []Model{}
		}
	}
}

// Set 保存并持久化（先 Normalize：补默认值/校验回落/去重/修引用），非法输入尽量收敛而非报错
func (s *Store) Set(c Conf) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c = Normalize(c)
	return s.save()
}

// save 原子写：tmp + rename。perm 0600 在 Linux/mac 上真实收紧其他用户读权；
// Windows 上 Go 只解释 perm 的只读位，ACL 继承目录默认（exe 同级 data/ 本就是
// 单用户目录语义，与 config.json 一致）——不为此引 advapi32 显式 DACL
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) confCopy() Conf {
	c := s.c
	c.Profiles = append([]Profile(nil), s.c.Profiles...)
	c.GrantedDirs = append([]string(nil), s.c.GrantedDirs...)
	for i := range c.Profiles {
		c.Profiles[i].Models = append([]Model(nil), s.c.Profiles[i].Models...)
	}
	ensureProfiles(&c)        // 副本出口兜底：null 切片 → 空数组（前端 .length 安全）
	migrateLegacyDefaults(&c) // 旧默认代填值读侧修正（盘上数据下次保存时落正）
	return c
}

// migrateLegacyDefaults 旧版本曾把当时的默认 128K 代填进数据（全局 ctxTokens 与
// 非已知模型的 ctx）——默认升级后这些固化值须随迁：全局升为新默认，模型清空回落。
// 131072 的唯一来源是代填（真实用户手填 128K 的概率与代价皆低，取随迁）
func migrateLegacyDefaults(c *Conf) {
	if c.CtxTokens == 131072 {
		c.CtxTokens = DefaultCtxTokens
	}
	for i := range c.Profiles {
		for j := range c.Profiles[i].Models {
			m := &c.Profiles[i].Models[j]
			if m.Ctx == 131072 && !knownModel(m.Name) {
				m.Ctx = 0
			}
		}
	}
}

// validEffort 思考等级合法域（""=不发该字段）
var validEffort = map[string]bool{"": true, "low": true, "medium": true, "high": true}

// Normalize 规整配置：
//  1. 档案数/每档案模型数超限截断；空 ID 补 p<N>（不与现有冲突）；
//  2. 模型按名去重（保留靠前条目——前端编辑序即用户序）；空名剔除；
//  3. Effort 非法回落 ""；全局默认零值填 DefaultCtxTokens/DefaultMaxOut；
//  4. Active/Vision 引用不存在的档案或模型时清空（运行时据此判「未配置」）
func Normalize(c Conf) Conf {
	if len(c.Profiles) > maxProfiles {
		c.Profiles = c.Profiles[:maxProfiles]
	}
	if !validEffort[c.Effort] {
		c.Effort = ""
	}
	if c.CtxTokens <= 0 {
		c.CtxTokens = DefaultCtxTokens
	}
	if c.MaxOutTokens <= 0 {
		c.MaxOutTokens = DefaultMaxOut
	}
	ids := make(map[string]bool, len(c.Profiles))
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.ID == "" || ids[p.ID] {
			p.ID = allocProfileID(ids)
		}
		ids[p.ID] = true
		if len(p.Models) > maxModelsPerProf {
			p.Models = p.Models[:maxModelsPerProf]
		}
		seen := make(map[string]bool, len(p.Models))
		kept := p.Models[:0]
		for _, m := range p.Models {
			if m.Name == "" || seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			kept = append(kept, m)
		}
		p.Models = kept
	}
	// 引用校验：只校验档案存在性——模型名允许「先选后拉取/手填」（会话请求显式携带
	// 模型名，不必已登记在档案模型列表；此前连模型一起校验会把先选名的合法配置清空）
	profileOK := func(pid string) bool {
		if pid == "" {
			return false
		}
		for _, p := range c.Profiles {
			if p.ID == pid {
				return true
			}
		}
		return false
	}
	if !profileOK(c.ActiveProfile) {
		c.ActiveProfile, c.ActiveModel = "", ""
	}
	if !profileOK(c.VisionProfile) {
		c.VisionProfile, c.VisionModel = "", ""
	}
	ensureProfiles(&c) // 存盘侧统一：nil → 空数组
	migrateLegacyDefaults(&c)
	return c
}

// allocProfileID 分配不冲突的档案 ID（p1、p2…）
func allocProfileID(used map[string]bool) string {
	for i := 1; ; i++ {
		id := "p" + itoa(i)
		if !used[id] {
			return id
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// GrantDir 追加一条持久授权目录（去重、路径规整）
func (s *Store) GrantDir(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir = filepath.Clean(dir)
	for _, d := range s.c.GrantedDirs {
		if d == dir {
			return nil
		}
	}
	s.c.GrantedDirs = append(s.c.GrantedDirs, dir)
	return s.save()
}

// ---- 运行时取值（回落链：模型条目 → 全局默认）----

// FindProfile 按 ID 找档案
func (c *Conf) FindProfile(id string) *Profile {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			return &c.Profiles[i]
		}
	}
	return nil
}

// FindModel 档案内按名找模型条目（可能 nil——档案允许先建好、模型运行时手填）
func (p *Profile) FindModel(name string) *Model {
	for i := range p.Models {
		if p.Models[i].Name == name {
			return &p.Models[i]
		}
	}
	return nil
}

// ResolveCtx 上下文窗口：模型条目 → 全局默认
func (c *Conf) ResolveCtx(p *Profile, model string) int {
	if m := p.FindModel(model); m != nil && m.Ctx > 0 {
		return m.Ctx
	}
	if c.CtxTokens > 0 {
		return c.CtxTokens
	}
	return DefaultCtxTokens
}

// ResolveMaxOut 响应上限：模型条目 → 全局默认
func (c *Conf) ResolveMaxOut(p *Profile, model string) int {
	if m := p.FindModel(model); m != nil && m.MaxOut > 0 {
		return m.MaxOut
	}
	if c.MaxOutTokens > 0 {
		return c.MaxOutTokens
	}
	return DefaultMaxOut
}

// ResolveVision 模型是否支持图片：条目显式声明为准（vision 无全局默认——
// 未声明按 false 处理，贴图走视觉档案中转；用户明确知道自己的模型能力）
func (c *Conf) ResolveVision(p *Profile, model string) bool {
	if m := p.FindModel(model); m != nil {
		return m.Vision
	}
	return false
}

// Endpoint 主对话端点（未配置返回错误）
func (c *Conf) Endpoint(profileID string) (client.Endpoint, error) {
	p := c.FindProfile(profileID)
	if p == nil || p.BaseURL == "" {
		return client.Endpoint{}, errors.New("AI 端点未配置")
	}
	return client.Endpoint{BaseURL: p.BaseURL, APIKey: p.APIKey}, nil
}

// VisionEndpoint 视觉档案端点（未配置返回错误——贴图中转前的显式提示用）
func (c *Conf) VisionEndpoint() (client.Endpoint, string, error) {
	p := c.FindProfile(c.VisionProfile)
	if p == nil || p.BaseURL == "" || c.VisionModel == "" {
		return client.Endpoint{}, "", errors.New("视觉模型未配置")
	}
	return client.Endpoint{BaseURL: p.BaseURL, APIKey: p.APIKey}, c.VisionModel, nil
}

// Configured 是否已配置到可用（主档案 + 端点地址齐备；模型名运行时传参可为空，
// 但默认模型为空时前端仍应引导选择）。值接收者：需在 Get() 返回值上直接调用
func (c Conf) Configured() bool {
	// 值接收者的副本上取址调 FindProfile 仅判 nil，不解引用返回的指针
	return c.ActiveProfile != "" && c.FindProfile(c.ActiveProfile) != nil
}
