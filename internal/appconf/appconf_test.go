package appconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "config.json")
	s, err := openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetThemeSettings("light", "teal"); err != nil {
		t.Fatal(err)
	}
	if err := s.PushHistory("无条件", `{"keywords":null}`); err != nil {
		t.Fatal(err)
	}
	// 重新打开：主题设置与历史持久化生效，旧顶层 theme 键不出现在盘上
	s2, err := openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s2.Get()
	if c.Settings.Theme != (ThemeSettings{Appearance: "light", Palette: "teal"}) {
		t.Fatalf("主题设置应持久化: %+v", c.Settings.Theme)
	}
	if len(c.History) != 1 || c.History[0].Label != "无条件" {
		t.Fatalf("历史应持久化: %+v", c.History)
	}
}

func TestLegacyThemeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	// 旧格式：顶层 theme 字符串 + 历史
	old := `{"theme":"light","history":[{"label":"旧查询","spec":"{}"}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	// 外观沿用旧偏好，主题色落默认；历史无损
	if c.Settings.Theme != (ThemeSettings{Appearance: "light", Palette: "ice"}) {
		t.Fatalf("旧 theme 应迁入 settings.theme: %+v", c.Settings.Theme)
	}
	if c.Theme != "" {
		t.Fatalf("旧字段应清空: %q", c.Theme)
	}
	if len(c.History) != 1 || c.History[0].Label != "旧查询" {
		t.Fatalf("历史应保留: %+v", c.History)
	}
	// 盘上应已回写为新格式：settings 存在、顶层 theme 键消失
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, `"settings"`) || strings.Contains(out, `"theme": "light"`) {
		t.Fatalf("应回写新格式且无顶层 theme: %s", out)
	}

	// 旧字段为非法值：外观回落 auto，不继承坏数据
	path2 := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path2, []byte(`{"theme":"blue"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, _ := openPath(path2)
	if got := s2.Get().Settings.Theme; got != defaultTheme {
		t.Fatalf("非法旧值应回落默认: %+v", got)
	}
}

func TestInvalidThemeFallback(t *testing.T) {
	s, _ := openPath(filepath.Join(t.TempDir(), "config.json"))
	if err := s.SetThemeSettings("bogus", "nope"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get().Settings.Theme; got != defaultTheme {
		t.Fatalf("非法入参应回落默认: %+v", got)
	}
	// 盘上/settings JSON 里的非法值同样回落
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"settings":{"theme":{"appearance":"midnight","palette":"ultra"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, _ := openPath(path)
	if got := s2.Get().Settings.Theme; got != defaultTheme {
		t.Fatalf("盘上非法取值应回落默认: %+v", got)
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
	if c := s.Get(); c.Settings.Theme != defaultTheme || len(c.History) != 0 {
		t.Fatalf("损坏文件应回落默认: %+v", c)
	}
	// 下次写入应修复为合法文件
	if err := s.SetThemeSettings("dark", "green"); err != nil {
		t.Fatal(err)
	}
	if s2, _ := openPath(path); s2.Get().Settings.Theme != (ThemeSettings{Appearance: "dark", Palette: "green"}) {
		t.Fatal("写后应可回读")
	}
}

func TestFreshInstallNoFile(t *testing.T) {
	// 全新安装：不主动建盘，内存回落默认
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s, _ := openPath(path)
	if got := s.Get().Settings.Theme; got != defaultTheme {
		t.Fatalf("无文件应回落默认: %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("全新安装不应主动写 config.json")
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
