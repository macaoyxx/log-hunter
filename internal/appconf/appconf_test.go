package appconf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "config.json")
	s, err := openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTheme("light"); err != nil {
		t.Fatal(err)
	}
	if err := s.PushHistory("无条件", `{"keywords":null}`); err != nil {
		t.Fatal(err)
	}
	// 重新打开：主题与历史持久化生效
	s2, err := openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s2.Get()
	if c.Theme != "light" {
		t.Fatalf("主题应持久化: %q", c.Theme)
	}
	if len(c.History) != 1 || c.History[0].Label != "无条件" {
		t.Fatalf("历史应持久化: %+v", c.History)
	}
}

func TestPushHistoryDedupAndCap(t *testing.T) {
	s, err := openPath(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 填满上限
	for i := 0; i < maxHistory; i++ {
		if err := s.PushHistory(string(rune('a'+i%26))+string(rune('0'+i%10)), "{}"); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.Get().History); got != maxHistory {
		t.Fatalf("应恰好 %d 条: %d", maxHistory, got)
	}
	// 已有标签再推：置顶去重，总数不变
	if err := s.PushHistory(s.Get().History[5].Label, "{}"); err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if len(c.History) != maxHistory {
		t.Fatalf("去重后仍应 %d 条: %d", maxHistory, len(c.History))
	}
	// 新标签：最旧被挤出
	oldest := c.History[maxHistory-1].Label
	if err := s.PushHistory("全新查询", "{}"); err != nil {
		t.Fatal(err)
	}
	c = s.Get()
	if len(c.History) != maxHistory || c.History[0].Label != "全新查询" {
		t.Fatalf("新历史应头插且保持上限: %+v", c.History[:2])
	}
	for _, h := range c.History {
		if h.Label == oldest {
			t.Fatal("最旧一条应被挤出")
		}
	}
}

func TestCorruptFileFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{不是合法json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := s.Get(); c.Theme != "" || len(c.History) != 0 {
		t.Fatalf("损坏文件应回落默认: %+v", c)
	}
	// 下次写入应修复为合法文件
	if err := s.SetTheme("dark"); err != nil {
		t.Fatal(err)
	}
	if s2, _ := openPath(path); s2.Get().Theme != "dark" {
		t.Fatal("写后应可回读")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s, _ := openPath(filepath.Join(t.TempDir(), "config.json"))
	_ = s.PushHistory("a", "{}")
	h := s.Get().History
	h[0].Label = "篡改"
	if s.Get().History[0].Label != "a" {
		t.Fatal("Get 应返回副本，外部修改不得影响内部状态")
	}
}
