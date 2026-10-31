package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip 构造测试 zip（条目名→内容）
func makeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "test.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractExe(t *testing.T) {
	dir := t.TempDir()

	// 正常：唯一条目解出且内容一致
	zp := makeZip(t, map[string]string{exeEntryName: "NEW-EXE-BYTES"})
	dest := filepath.Join(dir, "new.exe")
	if err := ExtractExe(zp, dest); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "NEW-EXE-BYTES" {
		t.Fatalf("解压内容错误: %q", b)
	}

	// 条目名不符：报错不产出文件
	zp2 := makeZip(t, map[string]string{"other.exe": "x"})
	dest2 := filepath.Join(dir, "new2.exe")
	if err := ExtractExe(zp2, dest2); err == nil || !strings.Contains(err.Error(), "未找到") {
		t.Fatalf("应报条目缺失: %v", err)
	}
	if _, err := os.Stat(dest2); !os.IsNotExist(err) {
		t.Fatal("失败时不应产出文件")
	}
}

func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	os.WriteFile(p, []byte("hello log-hunter"), 0644)
	sum := sha256.Sum256([]byte("hello log-hunter"))
	want := hex.EncodeToString(sum[:])

	if err := VerifySHA256(p, want); err != nil {
		t.Fatalf("正确摘要应通过: %v", err)
	}
	if err := VerifySHA256(p, strings.ToUpper(want)); err != nil {
		t.Fatalf("大小写不敏感应通过: %v", err)
	}
	if err := VerifySHA256(p, "deadbeef"); err == nil {
		t.Fatal("错误摘要应失败")
	}
}

func TestApplyAndRollback(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "app.exe")
	newExe := filepath.Join(dir, "new.exe")
	os.WriteFile(cur, []byte("OLD"), 0644)
	os.WriteFile(newExe, []byte("NEW"), 0644)

	// 正常替换：cur=NEW、.old=OLD
	if err := Apply(newExe, cur); err != nil {
		t.Fatal(err)
	}
	cb, _ := os.ReadFile(cur)
	ob, _ := os.ReadFile(cur + ".old")
	if string(cb) != "NEW" || string(ob) != "OLD" {
		t.Fatalf("替换结果错误: cur=%q old=%q", cb, ob)
	}
	CleanupOld(cur)
	if _, err := os.Stat(cur + ".old"); !os.IsNotExist(err) {
		t.Fatal("CleanupOld 应删除 .old")
	}

	// 落位失败回滚：new 缺失 → cur 保持 OLD 可启动
	os.WriteFile(cur, []byte("OLD"), 0644)
	if err := Apply(filepath.Join(dir, "missing.exe"), cur); err == nil {
		t.Fatal("应报落位失败")
	}
	cb, _ = os.ReadFile(cur)
	if string(cb) != "OLD" {
		t.Fatalf("回滚后应保持旧内容: %q", cb)
	}
}

func TestDownload(t *testing.T) {
	payload := strings.Repeat("A", 200_000) // 大于一个读缓冲，验证分块累计
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("请求应带 User-Agent")
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	dir := t.TempDir()
	save := filepath.Join(dir, "dl.zip")
	var last DownloadProgress
	err := Download(context.Background(), srv.URL, save, func(p DownloadProgress) {
		last = p
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(save)
	if len(b) != len(payload) {
		t.Fatalf("下载不完整: %d", len(b))
	}
	if last.Received != int64(len(payload)) {
		t.Fatalf("进度收尾值错误: %d", last.Received)
	}
	if _, err := os.Stat(save + ".part"); !os.IsNotExist(err) {
		t.Fatal("完成后不应残留 .part")
	}

	// 取消：请求已取消的 ctx 应失败且不残留文件
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Download(ctx, srv.URL, filepath.Join(dir, "dl2.zip"), nil)
	if err == nil {
		t.Fatal("已取消应报错")
	}
	if _, err := os.Stat(filepath.Join(dir, "dl2.zip")); !os.IsNotExist(err) {
		t.Fatal("取消后不应有产物")
	}
}

// TestCopyFile 复制内容一致性（跨盘落位降级路径的基石）
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	payload := []byte("LogHunter update payload — 跨盘落位测试内容")
	if err := os.WriteFile(src, payload, 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.bin")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, payload) {
		t.Fatalf("复制内容不一致: %q", got)
	}
}

// TestApplyViaCopyPath 走「rename 失败→copy 兜底」路径：把 newExe 放在已删除的
// 路径使第一步 rename 失败不可行——改为直接验证 copy+rename 组合：模拟跨盘
// 场景难（需双卷），这里验证 Apply 在同盘 copy 兜底可用性由 TestApplyAndRollback
// 覆盖正常路径、copyFile 覆盖复制正确性，跨盘组合为代码路径直查
func TestApplyNewMissingRollback(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "app.exe")
	os.WriteFile(cur, []byte("OLD"), 0644)
	// newExe 不存在：rename 失败 → copyFile 也失败 → 回滚保持旧版可启动
	if err := Apply(filepath.Join(dir, "missing.exe"), cur); err == nil {
		t.Fatal("应报落位失败")
	}
	cb, _ := os.ReadFile(cur)
	if string(cb) != "OLD" {
		t.Fatalf("回滚后应保持旧内容: %q", cb)
	}
}
