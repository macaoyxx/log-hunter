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

// Conf 应用级持久化配置（exe 同级 data/config.json）
type Conf struct {
	Theme   string     `json:"theme"`   // auto | light | dark，空 = 未设置过
	History []HistItem `json:"history"` // 最近查询（新在前，上限 20）
}

// maxHistory 历史上限（与会话内 HISTORY_MAX 语义一致）
const maxHistory = 20

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

// openPath 指定路径打开（测试注入用）
func openPath(path string) (*Store, error) {
	s := &Store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.conf) // 解析失败静默回落默认，坏文件下次写入时修复
	}
	return s, nil
}

// Get 当前配置快照（副本，避免调用方持有内部切片）
func (s *Store) Get() Conf {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conf
	c.History = append([]HistItem(nil), s.conf.History...)
	return c
}

// SetTheme 持久化主题偏好
func (s *Store) SetTheme(theme string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conf.Theme = theme
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
