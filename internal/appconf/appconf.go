package appconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// HistItem 一条查询历史：显示标签 + 查询条件 JSON（原样存储，前端反序列化重跑）
type HistItem struct {
	Label string `json:"label"`
	Spec  string `json:"spec"`
}

// ThemeSettings 主题偏好：外观三态 × 主题色，五套均夜间/日间成对定义
type ThemeSettings struct {
	Appearance string `json:"appearance"` // auto | light | dark
	Palette    string `json:"palette"`    // ice | teal | green | plat | amber
}

// Settings 应用设置（后续一切设置项都在此扩展，不再动顶层结构）
type Settings struct {
	Theme ThemeSettings `json:"theme"`
}

// Conf 应用级持久化配置（exe 同级 data/config.json）
type Conf struct {
	Settings Settings   `json:"settings"`
	History  []HistItem `json:"history"` // 最近查询（新在前，上限 20）

	// Theme 旧版顶层主题字段（auto|light|dark）。仅为读取迁移保留：
	// openPath 时其合法值迁入 Settings.Theme.Appearance 并清空，此后写入不再出现该键
	Theme string `json:"theme,omitempty"`
}

// maxHistory 历史上限（与会话内 HISTORY_MAX 语义一致）
const maxHistory = 20

// 主题取值域与默认值（非法一律回落，坏数据不落盘）
var (
	validAppearance = map[string]bool{"auto": true, "light": true, "dark": true}
	validPalette    = map[string]bool{"ice": true, "teal": true, "green": true, "plat": true, "amber": true}
	defaultTheme    = ThemeSettings{Appearance: "auto", Palette: "ice"}
)

// Store 配置文件读写。进程内低频调用，mu 防并发交叉写坏文件
type Store struct {
	path string
	mu   sync.Mutex
	conf Conf
}

// Open 打开默认位置的配置（exe 同级 data/config.json）；文件不存在或损坏回落默认值
func Open() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return openPath(path)
}

// DefaultPath exe 同级 data/config.json（开发模式在 build/bin/data 下，已被 gitignore）
func DefaultPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "data", "config.json"), nil
}

// openPath 指定路径打开（测试注入用）：解析失败静默回落默认，随后做旧格式迁移
func openPath(path string) (*Store, error) {
	s := &Store{path: path}
	loaded := false
	if b, err := os.ReadFile(path); err == nil {
		loaded = json.Unmarshal(b, &s.conf) == nil // 解析失败静默回落默认，坏文件下次写入时修复
	}
	s.migrate(loaded)
	return s, nil
}

// migrate 主题设置归一与旧格式迁移（loaded 表示磁盘上确有已解析的旧数据）：
//  1. 旧版顶层 theme（auto|light|dark）迁入 settings.theme.appearance；
//  2. appearance/palette 非法或缺失回落默认（auto/ice）；
//  3. 旧数据有变更则立刻回写为新格式（顶层 theme 键从此消失）；
//     全新安装（无文件/损坏）只回落内存默认，不主动建盘。
// 迁移写失败不报错：下次任意写入时自然修复
func (s *Store) migrate(loaded bool) {
	t := s.conf.Settings.Theme
	if t.Appearance == "" && validAppearance[s.conf.Theme] {
		t.Appearance = s.conf.Theme // 旧格式迁移：外观沿用用户既有偏好
	}
	if !validAppearance[t.Appearance] {
		t.Appearance = defaultTheme.Appearance
	}
	if !validPalette[t.Palette] {
		t.Palette = defaultTheme.Palette
	}
	changed := loaded && (s.conf.Settings.Theme != t || s.conf.Theme != "")
	s.conf.Settings.Theme = t
	s.conf.Theme = ""
	if changed {
		_ = s.save()
	}
}

// Get 当前配置快照（副本，避免调用方持有内部切片）
func (s *Store) Get() Conf {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conf
	c.History = append([]HistItem(nil), s.conf.History...)
	return c
}

// SetThemeSettings 持久化主题偏好（外观 + 主题色），非法值回落默认
func (s *Store) SetThemeSettings(appearance, palette string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validAppearance[appearance] {
		appearance = defaultTheme.Appearance
	}
	if !validPalette[palette] {
		palette = defaultTheme.Palette
	}
	s.conf.Settings.Theme = ThemeSettings{Appearance: appearance, Palette: palette}
	return s.save()
}

// PushHistory 头插一条历史：同标签去重置顶，超出上限丢弃最旧
func (s *Store) PushHistory(label, specJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make([]HistItem, 0, maxHistory)
	keep = append(keep, HistItem{Label: label, Spec: specJSON})
	for _, h := range s.conf.History {
		if h.Label == label {
			continue
		}
		if len(keep) >= maxHistory {
			break
		}
		keep = append(keep, h)
	}
	s.conf.History = keep
	return s.save()
}

// save 原子写：先写 tmp 再 rename，断电/崩溃不产生半截配置
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.conf, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
