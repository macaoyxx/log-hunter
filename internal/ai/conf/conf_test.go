package conf

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizeDefaultsAndRefs(t *testing.T) {
	c := Normalize(Conf{
		Profiles: []Profile{
			{ID: "", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-1",
				Models: []Model{{Name: "deepseek-chat"}, {Name: "deepseek-chat"}, {Name: ""}}},
			{ID: "p1", Name: "重复ID", BaseURL: "http://x"}, // 与上面补出的 p1 冲突 → 改 p2
		},
		ActiveProfile: "ghost", ActiveModel: "nope", // 引用不存在 → 清空
		Effort: "ultra", // 非法 → ""
	})
	if len(c.Profiles) != 2 {
		t.Fatalf("档案数: %d", len(c.Profiles))
	}
	if c.Profiles[0].ID != "p1" || c.Profiles[1].ID != "p2" {
		t.Fatalf("ID 分配: %s / %s", c.Profiles[0].ID, c.Profiles[1].ID)
	}
	if n := len(c.Profiles[0].Models); n != 1 {
		t.Fatalf("模型去重失败: %d", n)
	}
	if c.Effort != "" {
		t.Fatalf("非法 effort 应回落: %q", c.Effort)
	}
	if c.CtxTokens != DefaultCtxTokens || c.MaxOutTokens != DefaultMaxOut {
		t.Fatalf("全局默认未填: %d/%d", c.CtxTokens, c.MaxOutTokens)
	}
	if c.ActiveProfile != "" || c.ActiveModel != "" {
		t.Fatalf("幽灵引用未清: %s/%s", c.ActiveProfile, c.ActiveModel)
	}

	// 有效引用保留
	c2 := Normalize(Conf{
		Profiles:      []Profile{{ID: "p1", Models: []Model{{Name: "m1"}}}},
		ActiveProfile: "p1", ActiveModel: "m1",
	})
	if c2.ActiveProfile != "p1" || c2.ActiveModel != "m1" {
		t.Fatal("有效引用被误清")
	}
}

func TestResolveChain(t *testing.T) {
	c := Conf{
		Profiles: []Profile{{
			ID:     "p1",
			Models: []Model{{Name: "big", Ctx: 1048576, MaxOut: 32768, Vision: true}, {Name: "plain"}},
		}},
		CtxTokens: 131072, MaxOutTokens: 8192,
	}
	p := c.FindProfile("p1")
	if c.ResolveCtx(p, "big") != 1048576 {
		t.Fatal("模型级 ctx 未生效")
	}
	if c.ResolveCtx(p, "plain") != 131072 {
		t.Fatal("应回落全局默认 ctx")
	}
	if c.ResolveMaxOut(p, "big") != 32768 || c.ResolveMaxOut(p, "plain") != 8192 {
		t.Fatal("maxOut 回落链错误")
	}
	if !c.ResolveVision(p, "big") || c.ResolveVision(p, "plain") {
		t.Fatal("vision 判定错误")
	}
	// 零值全局（未 Normalize 的构造）也应回落常量
	c0 := Conf{Profiles: c.Profiles}
	if c0.ResolveCtx(p, "plain") != DefaultCtxTokens {
		t.Fatal("零值全局应回落 DefaultCtxTokens")
	}
}

func TestStoreSaveLoad(t *testing.T) {
	dir := t.TempDir()
	s, err := openPath(filepath.Join(dir, "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	in := Conf{
		Profiles:      []Profile{{ID: "p1", Name: "Ollama", BaseURL: "http://localhost:11434/v1", Models: []Model{{Name: "qwen2.5:14b"}}}},
		ActiveProfile: "p1", ActiveModel: "qwen2.5:14b",
	}
	if err := s.Set(in); err != nil {
		t.Fatal(err)
	}
	// 权限 0600（仅 Unix 真实生效；Windows 的 Go 只解释 perm 只读位，跳过断言）
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, "ai.json"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("权限应为 0600: %v", fi.Mode().Perm())
		}
	}
	// 重开读取往返
	s2, _ := openPath(filepath.Join(dir, "ai.json"))
	got := s2.Get()
	if !got.Configured() || got.ActiveModel != "qwen2.5:14b" {
		t.Fatalf("往返丢失: %+v", got)
	}
	// 损坏文件静默回落默认
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{oops"), 0o600)
	s3, _ := openPath(filepath.Join(dir, "bad.json"))
	if s3.Get().Configured() {
		t.Fatal("坏文件应回落默认")
	}
}

func TestEndpointHelpers(t *testing.T) {
	c := Conf{
		Profiles: []Profile{
			{ID: "p1", Name: "主", BaseURL: "https://a.example/v1", APIKey: "k1"},
			{ID: "p2", Name: "视觉", BaseURL: "https://b.example/v1", APIKey: "k2", Models: []Model{{Name: "vl", Vision: true}}},
		},
		ActiveProfile: "p1", VisionProfile: "p2", VisionModel: "vl",
	}
	ep, err := c.Endpoint("p1")
	if err != nil || ep.BaseURL != "https://a.example/v1" || ep.APIKey != "k1" {
		t.Fatalf("主端点错误: %+v %v", ep, err)
	}
	vep, vmodel, err := c.VisionEndpoint()
	if err != nil || vmodel != "vl" || vep.APIKey != "k2" {
		t.Fatalf("视觉端点错误: %+v %s %v", vep, vmodel, err)
	}
	// 未配置视觉 → 报错
	c2 := c
	c2.VisionProfile = ""
	if _, _, err := c2.VisionEndpoint(); err == nil {
		t.Fatal("视觉未配置应报错")
	}
	if _, err := c.Endpoint("ghost"); err == nil {
		t.Fatal("幽灵档案应报错")
	}
}

func TestLookupCaps(t *testing.T) {
	// 命中表：确定数字
	cases := []struct {
		name    string
		wantCtx int
		wantV   bool
	}{
		{"deepseek-chat", 131072, false},
		{"DeepSeek-Reasoner", 131072, false},      // 大小写不敏感
		{"qwen2.5-vl-72b-instruct", 131072, true}, // 家族 + vl 组合判定
		{"gpt-4o", 131072, true},
		{"o3-mini", 200000, false},
		{"gemini-2.9-flash-thinking", 1048576, true},
	}
	for _, c := range cases {
		got, hit := LookupCaps(c.name)
		if !hit || got.Ctx != c.wantCtx || got.Vision != c.wantV {
			t.Errorf("LookupCaps(%q) = %+v hit=%v, want ctx=%d v=%v", c.name, got, hit, c.wantCtx, c.wantV)
		}
	}
	// 未收录：不编数字（空=全局默认回落），vision 名仍自动勾
	got, hit := LookupCaps("llama3-8b")
	if hit || got.Ctx != 0 || got.MaxOut != 0 || got.Vision {
		t.Errorf("未收录应空值: %+v hit=%v", got, hit)
	}
	got, hit = LookupCaps("deepseek-v4-flash-vision-exp")
	if hit || got.Ctx != 0 || !got.Vision {
		t.Errorf("未收录 vision 名应只勾视觉: %+v hit=%v", got, hit)
	}
}

// TestPrefill 预填只在条目诞生时调用一次：命中能力表填确定数字（o 系 maxOut 0 = 不发），
// 未命中只按关键词勾 vision、数字留空走全局默认
func TestPrefill(t *testing.T) {
	cases := []struct {
		name string
		want Model
	}{
		{"gpt-4o", Model{Name: "gpt-4o", Ctx: 131072, Vision: true, MaxOut: 16384}},
		{"o3-mini", Model{Name: "o3-mini", Ctx: 200000}},              // maxOut 0 = 不发字段
		{"some-vl-model", Model{Name: "some-vl-model", Vision: true}}, // 未命中表，仅关键词勾视觉
		{"GLM-5.3-Flash", Model{Name: "GLM-5.3-Flash"}},               // 未命中且无关键词：全空
		{"", Model{}}, // 空名不预填
	}
	for _, c := range cases {
		got := Model{Name: c.name}
		Prefill(&got)
		if got != c.want {
			t.Errorf("Prefill(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
}
