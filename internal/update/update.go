// Package update 应用内自动更新：从 GitHub Release 下载 zip、sha256 校验、解压、
// 替换自身 exe。纯标准库实现。安全基线：下载 URL 由调用方给 GitHub Release 资产
// 直链（HTTPS）；资产 sha256 存在时强制校验（历史 Release 无 checksum 时过渡跳过）；
// 解压条目名精确匹配发布产物固定名（比通用解压的路径清洗更严格，杜绝 Zip Slip 变体）
package update

import (
	"context"
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// exeEntryName 发布 zip 内的唯一条目名（package.sh/packzip 固定产物结构）
const exeEntryName = "log-hunter.exe"

// maxExeBytes 解压尺寸防御上限（产物约 12MB，给足余量防解压炸弹）
const maxExeBytes = 128 << 20

// DownloadProgress 下载进度（字节；Total 为 -1 表示服务端未给 Content-Length）
type DownloadProgress struct {
	Received int64 `json:"received"`
	Total    int64 `json:"total"`
}

// Download 下载 url 到 savePath：先写 .part 临时文件，完成后再 rename 落位——
// 中途失败/取消不残留半截文件。onProgress 节流 80ms（与查询进度口径一致）
func Download(ctx context.Context, url, savePath string, onProgress func(DownloadProgress)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "log-hunter") // GitHub 硬性要求 UA
	client := &http.Client{Timeout: 10 * time.Minute} // 12MB 量级，慢网也要给足
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	part := savePath + ".part"
	fail := func(e error) error {
		_ = os.Remove(part)
		return e
	}
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	var received int64
	lastEmit := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return fail(fmt.Errorf("已取消: %w", err))
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return fail(werr)
			}
			received += int64(n)
			if onProgress != nil && time.Since(lastEmit) > 80*time.Millisecond {
				lastEmit = time.Now()
				onProgress(DownloadProgress{Received: received, Total: resp.ContentLength})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fail(fmt.Errorf("下载中断: %w", rerr))
		}
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(part, savePath); err != nil {
		return fail(err)
	}
	if onProgress != nil {
		onProgress(DownloadProgress{Received: received, Total: received})
	}
	return nil
}

// VerifySHA256 流式计算文件 sha256 并与期望值对比（忽略大小写与首尾空白——
// 与 sha256sum 文件格式 "哈希  文件名" 兼容，取首个字段即可由调用方切分）
func VerifySHA256(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(strings.TrimSpace(expectedHex), got) {
		return fmt.Errorf("校验失败: 期望 %s 实得 %s", strings.TrimSpace(expectedHex), got)
	}
	return nil
}

// ExtractExe 从更新 zip 解压 log-hunter.exe 到 destPath。条目名精确匹配（发布产物
// 固定单条目），不符即报错；解压量超 maxExeBytes 视为异常包中止
func ExtractExe(zipPath, destPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开更新包失败: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != exeEntryName {
			continue // 跳过 sha256 等其他资产（正常发布 zip 只有 exe 一个条目，防御兼容）
		}
		if f.FileInfo().IsDir() || f.UncompressedSize64 > maxExeBytes {
			return fmt.Errorf("更新包条目异常: %q", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return err
		}
		_, werr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if werr != nil {
			_ = os.Remove(destPath)
			return fmt.Errorf("解压失败: %w", werr)
		}
		return nil
	}
	return fmt.Errorf("更新包中未找到 %s", exeEntryName)
}

// Apply 替换当前 exe：Windows 运行中的 exe 不可覆盖但可改名——旧 exe 改名 .old
// 让位、新 exe 落位原名；第二步失败自动回滚保持可启动。残留的 .old 由新进程
// 启动时 CleanupOld 清理（旧进程退出释放文件锁有时差）
func Apply(newExe, currentExe string) error {
	old := currentExe + ".old"
	_ = os.Remove(old) // 清掉上次更新未及清理的残留
	if err := os.Rename(currentExe, old); err != nil {
		return fmt.Errorf("旧版本让位失败: %w", err)
	}
	if err := os.Rename(newExe, currentExe); err != nil {
		if rbErr := os.Rename(old, currentExe); rbErr != nil {
			return fmt.Errorf("新版落位失败且回滚失败（请手动把 %s 改回 %s）: 落位 %v / 回滚 %v",
				old, currentExe, err, rbErr)
		}
		return fmt.Errorf("新版落位失败（已回滚旧版本）: %w", err)
	}
	return nil
}

// CleanupOld 删除上次更新残留的 .old（新进程启动时调用；旧进程退出释放文件锁
// 有时差，退避重试 3 次）
func CleanupOld(currentExe string) {
	old := currentExe + ".old"
	for i := 0; i < 3; i++ {
		err := os.Remove(old)
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}
