package diag

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"log-hunter/internal/ai/conf"
)

// newAuthDiagnoser 构造带授权集的编排器；grant 预授目录列表（模拟用户已点「允许并记住」）
func newAuthDiagnoser(t *testing.T, grants []string) *Diagnoser {
	t.Helper()
	dir := t.TempDir()
	store, err := conf.OpenPath(filepath.Join(dir, "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if err := store.GrantDir(g); err != nil {
			t.Fatal(err)
		}
	}
	return &Diagnoser{Conf: store, DataDir: dir, Emit: func(Event) {}}
}

// stubAuth 注入即答的 AskAuth（模拟用户点授权卡），asks 记录征询次数
func stubAuth(d *Diagnoser, r AuthResult, asks *int) {
	d.AskAuth = func(context.Context, string, string, bool) AuthResult {
		if asks != nil {
			*asks++
		}
		return r
	}
}

func TestReadSourceGranted(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "PayService.java")
	os.WriteFile(src, []byte("line1\nline2\nline3\n"), 0o644)
	d := newAuthDiagnoser(t, []string{srcDir})
	s := &Session{}
	args := `{"path": "` + filepath.ToSlash(src) + `", "reason": "stack"}`
	out := d.readSourceExec(context.Background(), s, args)
	if !contains(out, "line1") {
		t.Fatalf("granted should read: %.100s", out)
	}
	if out2 := d.readSourceExec(context.Background(), s, args); !contains(out2, "line2") {
		t.Fatal("second read should succeed")
	}
}

// 阻塞征询-允许并记住：本次即读取成功；remember 持久化后新会话（同 store）免问直读
func TestReadSourceAskAllowRemember(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "A.java")
	os.WriteFile(src, []byte("x-content\n"), 0o644)
	d := newAuthDiagnoser(t, nil)
	var asks int
	stubAuth(d, AuthResult{Allow: true, Remember: true}, &asks)
	out := d.readSourceExec(context.Background(), &Session{}, `{"path": "`+filepath.ToSlash(src)+`", "reason": "r"}`)
	if !contains(out, "x-content") {
		t.Fatalf("allow should read immediately: %.100s", out)
	}
	if asks != 1 {
		t.Fatalf("asked %d times, want 1", asks)
	}
	// 持久化验证：全新会话（空会话授权集）+ 计数 AskAuth——仍应免问直读（remember 已入 store）
	d2 := &Diagnoser{Conf: d.Conf, DataDir: d.DataDir, Emit: func(Event) {}}
	var asks2 int
	stubAuth(d2, AuthResult{}, &asks2)
	if out2 := d2.readSourceExec(context.Background(), &Session{}, `{"path": "`+filepath.ToSlash(src)+`", "reason": "r"}`); !contains(out2, "x-content") {
		t.Fatalf("remembered grant should cover new session: %.100s", out2)
	}
	if asks2 != 0 {
		t.Fatalf("remembered grant should not re-ask, asked %d", asks2)
	}
}

// 阻塞征询-拒绝：返回拒绝话术且会话内登记拒绝——第二次不再征询直接拒绝
func TestReadSourceAskDeny(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "A.java")
	os.WriteFile(src, []byte("x\n"), 0o644)
	d := newAuthDiagnoser(t, nil)
	var asks int
	stubAuth(d, AuthResult{Allow: false}, &asks)
	s := &Session{}
	out := d.readSourceExec(context.Background(), s, `{"path": "`+filepath.ToSlash(src)+`", "reason": "r"}`)
	if !contains(out, "拒绝") {
		t.Fatalf("deny should say rejected: %.100s", out)
	}
	if out2 := d.readSourceExec(context.Background(), s, `{"path": "`+filepath.ToSlash(src)+`", "reason": "r"}`); !contains(out2, "此前已拒绝") {
		t.Fatalf("second ask should hit session denial: %.100s", out2)
	}
	if asks != 1 {
		t.Fatalf("should ask once, asked %d", asks)
	}
}

// 取消（ctx 已断）：不登记拒绝——下一轮换新 ctx 仍可重新征询
func TestReadSourceCancelledNotDenied(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "A.java")
	os.WriteFile(src, []byte("okmark\n"), 0o644)
	d := newAuthDiagnoser(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.AskAuth = func(context.Context, string, string, bool) AuthResult {
		return AuthResult{} // askSourceAuth 在 ctx.Done 分支返回空回执
	}
	s := &Session{}
	if out := d.readSourceExec(ctx, s, `{"path": "`+filepath.ToSlash(src)+`", "reason": "r"}`); !contains(out, "取消") {
		t.Fatalf("cancel should say cancelled: %.100s", out)
	}
	// 新一轮（新 ctx + 允许回执）：不应被上轮取消污染
	stubAuth(d, AuthResult{Allow: true}, nil)
	if out := d.readSourceExec(context.Background(), s, `{"path": "`+filepath.ToSlash(src)+`", "reason": "r"}`); !contains(out, "okmark") {
		t.Fatalf("cancel must not deny dir: %.100s", out)
	}
}

func TestReadSourceRecursiveGrant(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "ab-wf")
	os.MkdirAll(sub, 0o755)
	src := filepath.Join(sub, "Flow.java")
	os.WriteFile(src, []byte("code\n"), 0o644)
	d := newAuthDiagnoser(t, []string{root})
	s := &Session{}
	args := `{"path": "` + filepath.ToSlash(src) + `", "reason": "r"}`
	if out := d.readSourceExec(context.Background(), s, args); !contains(out, "code") {
		t.Fatalf("subtree grant should cover subdir: %.100s", out)
	}
}

func TestReadSourceDirectoryListing(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "A.java"), []byte("a"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	d := newAuthDiagnoser(t, []string{dir})
	s := &Session{}
	args := `{"path": "` + filepath.ToSlash(dir) + `", "reason": "r"}`
	out := d.readSourceExec(context.Background(), s, args)
	if !contains(out, "A.java") || !contains(out, "sub") {
		t.Fatalf("dir listing: %.150s", out)
	}
}

// 敏感文件：目录授权不覆盖——每次读取都单独征询；允许即读、拒绝登记后不再问
func TestSensitiveFilePerRead(t *testing.T) {
	srcDir := t.TempDir()
	yml := filepath.Join(srcDir, "application.yml")
	os.WriteFile(yml, []byte("db:\n  password: x\n"), 0o644)
	d := newAuthDiagnoser(t, []string{srcDir})
	var asks int
	stubAuth(d, AuthResult{Allow: true}, &asks)
	s := &Session{}
	args := `{"path": "` + filepath.ToSlash(yml) + `", "reason": "cfg"}`
	if out := d.readSourceExec(context.Background(), s, args); !contains(out, "password") {
		t.Fatalf("allowed sensitive read should see content: %.100s", out)
	}
	// 第二次读取：无记住选项——必须再次征询
	if out := d.readSourceExec(context.Background(), s, args); !contains(out, "password") {
		t.Fatalf("second allowed read: %.100s", out)
	}
	if asks != 2 {
		t.Fatalf("sensitive should ask per read, asked %d", asks)
	}
	// 拒绝一次：登记后不再征询
	stubAuth(d, AuthResult{Allow: false}, nil)
	if out := d.readSourceExec(context.Background(), s, args); !contains(out, "拒绝") {
		t.Fatalf("deny sensitive: %.100s", out)
	}
	if out := d.readSourceExec(context.Background(), s, args); !contains(out, "此前已拒绝") {
		t.Fatalf("denied sensitive should not re-ask: %.100s", out)
	}
}

func TestSensitiveFileMatch(t *testing.T) {
	for _, f := range []string{".env", "prod.env", "application.yml", "a.YAML", "config.json", "server.pem", "id_rsa", "db-credentials.txt"} {
		if !sensitiveFile(f) {
			t.Errorf("should be sensitive: %s", f)
		}
	}
	for _, f := range []string{"Pay.java", "main.go", "readme.md"} {
		if sensitiveFile(f) {
			t.Errorf("should not be sensitive: %s", f)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
