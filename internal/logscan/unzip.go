package logscan

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// maxUnzipBytes 单个 zip 累计解压上限（8GB）：防 zip 炸弹耗尽磁盘，正常日志包远小于此
const maxUnzipBytes = 8 << 30

// binSniffLen 二进制探测采样长度：前 16KB 含 NUL(0x00) 即判二进制（文本文件不含 NUL）。
// 按内容而非扩展名判定——日志常有 .log.101 等轮转命名，不能依赖后缀白名单
const binSniffLen = 16 << 10

// addZip 加载一个 zip：解压文本文件到会话临时目录后逐个建索引。
// 重复拖入同一 zip 只提示不重复加载（GB 级日志双份内存不可接受）
func (s *Session) addZip(ctx context.Context, zipPath string, fileNo int, onProgress func(FileProgress)) ([]*FileEntry, []string, error) {
	abs, err := filepath.Abs(zipPath)
	if err != nil {
		return nil, nil, err
	}
	if s.zips[abs] {
		return nil, []string{filepath.Base(zipPath) + "：已加载过，跳过"}, nil
	}
	// 懒建会话级临时根；每个 zip 独立子目录（序号命名防同名 zip 互踩）
	if s.tempRoot == "" {
		root, err := os.MkdirTemp("", "log-hunter-unzip-")
		if err != nil {
			return nil, nil, fmt.Errorf("创建临时目录失败: %w", err)
		}
		s.tempRoot = root
	}
	s.zipDirSeq++
	dest := filepath.Join(s.tempRoot, fmt.Sprintf("zip-%d", s.zipDirSeq))
	paths, displays, notices, err := extractZip(ctx, abs, dest, fileNo, onProgress)
	if err != nil {
		_ = os.RemoveAll(dest) // 失败/取消：不留半解压残留
		return nil, notices, err
	}
	s.zips[abs] = true

	var added []*FileEntry
	for i, p := range paths {
		// 显示名带 zip 来源前缀；tooltip 的临时路径对用户只是真实落点
		e, err := s.addOne(ctx, p, fileNo, onProgress, displays[i], dest)
		if err != nil {
			notices = append(notices, fmt.Sprintf("%s: %v", displays[i], err))
			continue
		}
		if e != nil {
			added = append(added, e)
		}
	}
	return added, notices, nil
}

// extractZip 解压 zip 到 dest 子目录：只收文本文件（内容探测），二进制/空文件/加密
// 条目跳过并记入 notices；嵌套 zip 天然是二进制被自动跳过（不递归）。
// 返回解压产物绝对路径与对应显示名（zip名/内部相对路径）
func extractZip(ctx context.Context, zipPath, dest string, fileNo int, onProgress func(FileProgress)) (paths, displays, notices []string, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("不是有效的 zip: %w", err)
	}
	defer zr.Close()

	zipBase := filepath.Base(zipPath)
	total := len(zr.File)
	var done, writtenTotal int64
	for _, f := range zr.File {
		if ctx.Err() != nil {
			return nil, nil, notices, ctx.Err()
		}
		if f.FileInfo().IsDir() {
			continue // 目录条目随文件 MkdirAll 逐级创建
		}
		name := zipEntryName(f)
		done++
		if onProgress != nil {
			onProgress(FileProgress{Name: zipBase + "/" + name, Phase: "unzip", Done: done, Total: int64(total), FileNo: fileNo})
		}
		if f.Flags&1 != 0 { // 加密位（标准库无 IsEncrypted，手工查 flag bit0）
			notices = append(notices, name+"（加密，不支持）")
			continue
		}
		// 路径安全（Zip Slip）：个别工具用反斜杠分隔，统一后清洗，不得为绝对路径或逃出 dest
		rel := filepath.FromSlash(filepath.Clean(strings.ReplaceAll(name, "\\", "/")))
		if rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			notices = append(notices, name+"（路径非法，已跳过）")
			continue
		}
		target := filepath.Join(dest, rel)
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			notices = append(notices, name+"（路径非法，已跳过）")
			continue
		}

		// 先读头 16KB 做二进制探测，文本才落盘——二进制条目完全不产生磁盘写入
		rc, oerr := f.Open()
		if oerr != nil {
			notices = append(notices, name+"（打开失败）")
			continue
		}
		head := make([]byte, binSniffLen)
		n, rerr := io.ReadFull(rc, head)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			_ = rc.Close()
			notices = append(notices, name+"（读取失败）")
			continue
		}
		head = head[:n]
		if n == 0 {
			_ = rc.Close()
			notices = append(notices, name+"（空文件）")
			continue
		}
		if bytes.IndexByte(head, 0) >= 0 {
			_ = rc.Close()
			notices = append(notices, name+"（二进制，跳过）")
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = rc.Close()
			return nil, nil, notices, fmt.Errorf("创建解压目录失败: %w", err)
		}
		out, cerr := os.Create(target)
		if cerr != nil {
			_ = rc.Close()
			notices = append(notices, name+"（创建失败）")
			continue
		}
		var size int64
		_, werr := out.Write(head)
		if werr == nil {
			var cp int64
			cp, werr = io.Copy(out, rc)
			size = int64(n) + cp
		}
		_ = out.Close()
		_ = rc.Close()
		if werr != nil {
			_ = os.Remove(target)
			notices = append(notices, name+"（解压失败）")
			continue
		}
		writtenTotal += size
		if writtenTotal > maxUnzipBytes {
			_ = os.Remove(target)
			return nil, nil, notices, fmt.Errorf("解压总量超过上限 %d GB", maxUnzipBytes>>30)
		}
		paths = append(paths, target)
		displays = append(displays, zipBase+"/"+name)
	}
	return paths, displays, notices, nil
}

// zipEntryName 条目名解码：老打包工具在 NonUTF8 标志下按系统码页（GBK）写文件名
// 字节，需转回 UTF-8；解码失败保留原名
func zipEntryName(f *zip.File) string {
	if !f.NonUTF8 {
		return f.Name
	}
	if out, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil {
		return out
	}
	return f.Name
}
