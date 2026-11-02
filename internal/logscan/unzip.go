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
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// maxUnzipBytes 单次拖入 zip 的累计解压上限（8GB，嵌套各层共享）：防 zip 炸弹
// 耗尽磁盘，正常日志包远小于此
const maxUnzipBytes = 8 << 30

// maxZipDepth zip 递归解压深度上限（含最外层）：三层覆盖「收集包/服务包/日期包」
// 的常见打包习惯，再深按病态嵌套跳过（zip 炸弹的常见形态）
const maxZipDepth = 3

// binSniffLen 二进制探测采样长度：前 16KB 含 NUL(0x00) 即判二进制（文本文件不含 NUL）。
// 按内容而非扩展名判定——日志常有 .log.101 等轮转命名，不能依赖后缀白名单
const binSniffLen = 16 << 10

// staleTempAge 解压临时根的陈旧判定年龄：清扫只收修改时间超过该值的目录。
// 活会话的目录随解压持续刷新永远年轻；该护栏同时关死「实例刚建根、锁句柄
// 尚未打开」时被其他实例清扫误删的竞态窗口
const staleTempAge = time.Hour

// tempLockName 临时根内的锁文件名：会话存活期间持有其打开句柄，进程死亡句柄
// 自动释放——其他实例清扫时以「能否删除该文件」探测目录是否无主
const tempLockName = "lock"

// extracted extractZip 的单条解压产物
type extracted struct {
	path    string // 解压产物绝对路径
	display string // 显示名（zip名/内部相对路径，嵌套层递归续接）
	dir     string // 所属 zip 子目录（addOne 的 tempDir，移除时按目录引用计数清理）
}

// addZip 加载一个 zip：递归解压文本文件（含嵌套 zip，限 maxZipDepth 层）到会话
// 临时目录后逐个建索引。重复拖入同一 zip 只提示不重复加载（GB 级日志双份内存
// 不可接受）
func (s *Session) addZip(ctx context.Context, zipPath string, fileNo int, onProgress func(FileProgress)) ([]*FileEntry, []string, error) {
	abs, err := filepath.Abs(zipPath)
	if err != nil {
		return nil, nil, err
	}
	if s.zips[abs] {
		return nil, []string{filepath.Base(zipPath) + "：已加载过，跳过"}, nil
	}
	if err := s.ensureTempRoot(); err != nil {
		return nil, nil, err
	}
	startSeq := s.zipDirSeq // 本包新增目录的序号区间起点（失败清理用）
	s.zipDirSeq++
	dest := filepath.Join(s.tempRoot, fmt.Sprintf("zip-%d", s.zipDirSeq))
	var written int64
	files, notices, err := s.extractZip(ctx, abs, dest, 1, "", &written, fileNo, onProgress)
	if err != nil {
		s.cleanupZipDirs(startSeq) // 失败/取消：不留半解压残留（含内层 zip 的兄弟目录）
		return nil, notices, err
	}
	s.zips[abs] = true

	var added []*FileEntry
	for _, x := range files {
		// 显示名带 zip 来源前缀；tooltip 的临时路径对用户只是真实落点
		e, err := s.addOne(ctx, x.path, fileNo, onProgress, x.display, x.dir)
		if err != nil {
			notices = append(notices, fmt.Sprintf("%s: %v", x.display, err))
			continue
		}
		if e != nil {
			e.zipAbs = abs // 该 zip 目录清空时同步清 s.zips 登记，否则删光后重拖被「已加载过」挡住
			added = append(added, e)
		}
	}
	return added, notices, nil
}

// ensureTempRoot 懒建会话级临时根并持有锁文件：锁句柄随进程存活，其他实例启动
// 清扫以「能否删动 lock」判定本目录是否有主（进程死亡句柄自动释放）
func (s *Session) ensureTempRoot() error {
	if s.tempRoot != "" {
		return nil
	}
	root, err := os.MkdirTemp("", "log-hunter-unzip-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(root, tempLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(root)
		return fmt.Errorf("创建临时目录锁失败: %w", err)
	}
	s.tempRoot = root
	s.tempLock = lock
	return nil
}

// cleanupZipDirs 删除序号在 (startSeq, s.zipDirSeq] 的 zip 子目录与内层 scratch：
// 解压失败/取消的兜底。内层 zip 的目录是外层 dest 的兄弟目录，RemoveAll(dest)
// 覆盖不到，须按本次新增的序号区间清理
func (s *Session) cleanupZipDirs(startSeq int) {
	for k := startSeq + 1; k <= s.zipDirSeq; k++ {
		_ = os.RemoveAll(filepath.Join(s.tempRoot, fmt.Sprintf("zip-%d", k)))
		_ = os.RemoveAll(filepath.Join(s.tempRoot, fmt.Sprintf("zipsrc-%d", k)))
	}
}

// extractZip 解压 zip 到 dest 子目录：只收文本文件（内容探测），二进制/空文件/
// 加密条目跳过并记入 notices；嵌套 zip 按内容魔数识别后递归解压（每层独立
// zip-<seq> 子目录， RemoveFile 的按目录引用计数清理天然隔离）。
// prefix 为本层 zip 自身的显示名：外层传空（显示/进度名 = zip名/条目名、notice
// 不带前缀，行为与旧版一致）；内层传完整「外层/…/本层」前缀，notice 可定位来源
func (s *Session) extractZip(ctx context.Context, zipPath, dest string, depth int, prefix string, writtenTotal *int64, fileNo int, onProgress func(FileProgress)) (out []extracted, notices []string, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, fmt.Errorf("不是有效的 zip: %w", err)
	}
	defer zr.Close()

	zipBase := filepath.Base(zipPath)
	nameOf := func(name string) string {
		if prefix == "" {
			return zipBase + "/" + name
		}
		return prefix + "/" + name
	}
	noticeOf := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "/" + name
	}
	total := len(zr.File)
	var done int64
	for _, f := range zr.File {
		if ctx.Err() != nil {
			return nil, notices, ctx.Err()
		}
		if f.FileInfo().IsDir() {
			continue // 目录条目随文件 MkdirAll 逐级创建
		}
		name := zipEntryName(f)
		done++
		if onProgress != nil {
			onProgress(FileProgress{Name: nameOf(name), Phase: "unzip", Done: done, Total: int64(total), FileNo: fileNo})
		}
		if f.Flags&1 != 0 { // 加密位（标准库无 IsEncrypted，手工查 flag bit0）
			notices = append(notices, noticeOf(name)+"（加密，不支持）")
			continue
		}
		// 路径安全（Zip Slip）：个别工具用反斜杠分隔，统一后清洗，不得为绝对路径或逃出 dest
		rel := filepath.FromSlash(filepath.Clean(strings.ReplaceAll(name, "\\", "/")))
		if rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			notices = append(notices, noticeOf(name)+"（路径非法，已跳过）")
			continue
		}
		target := filepath.Join(dest, rel)
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			notices = append(notices, noticeOf(name)+"（路径非法，已跳过）")
			continue
		}

		// 先读头 16KB 做二进制探测，文本才落盘——二进制条目完全不产生磁盘写入
		rc, oerr := f.Open()
		if oerr != nil {
			notices = append(notices, noticeOf(name)+"（打开失败）")
			continue
		}
		head := make([]byte, binSniffLen)
		n, rerr := io.ReadFull(rc, head)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			_ = rc.Close()
			notices = append(notices, noticeOf(name)+"（读取失败）")
			continue
		}
		head = head[:n]
		if n == 0 {
			_ = rc.Close()
			notices = append(notices, noticeOf(name)+"（空文件）")
			continue
		}
		// 嵌套 zip（内容魔数判定，先于 NUL 探测——zip 是压缩二进制必含 NUL）：
		// 限深内递归解压，超深按病态嵌套跳过
		if isZipHead(head) {
			if depth >= maxZipDepth {
				_ = rc.Close()
				notices = append(notices, fmt.Sprintf("%s（嵌套超过 %d 层，跳过）", nameOf(name), maxZipDepth))
				continue
			}
			sub, subNotices, serr := s.extractInnerZip(ctx, rc, head, nameOf(name), depth, writtenTotal, fileNo, onProgress)
			notices = append(notices, subNotices...)
			if serr != nil {
				return nil, notices, serr
			}
			out = append(out, sub...)
			continue
		}
		if bytes.IndexByte(head, 0) >= 0 {
			_ = rc.Close()
			notices = append(notices, noticeOf(name)+"（二进制，跳过）")
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = rc.Close()
			return nil, notices, fmt.Errorf("创建解压目录失败: %w", err)
		}
		outf, cerr := os.Create(target)
		if cerr != nil {
			_ = rc.Close()
			notices = append(notices, noticeOf(name)+"（创建失败）")
			continue
		}
		var size int64
		_, werr := outf.Write(head)
		if werr == nil {
			var cp int64
			cp, werr = io.Copy(outf, rc)
			size = int64(n) + cp
		}
		_ = outf.Close()
		_ = rc.Close()
		if werr != nil {
			_ = os.Remove(target)
			notices = append(notices, noticeOf(name)+"（解压失败）")
			continue
		}
		*writtenTotal += size
		if *writtenTotal > maxUnzipBytes {
			_ = os.Remove(target)
			return nil, notices, fmt.Errorf("解压总量超过上限 %d GB", maxUnzipBytes>>30)
		}
		out = append(out, extracted{path: target, display: nameOf(name), dir: dest})
	}
	return out, notices, nil
}

// extractInnerZip 把内层 zip 条目完整落盘为 scratch 文件后递归解压，完成后即删
// scratch。display 为内层 zip 的完整显示名（如 outer.zip/svc.zip）；writtenTotal
// 跨层共享（scratch 字节也计入 8GB 总量）；无效内层 zip 降级为提示不中断整包
func (s *Session) extractInnerZip(ctx context.Context, rc io.ReadCloser, head []byte, display string, depth int, writtenTotal *int64, fileNo int, onProgress func(FileProgress)) (out []extracted, notices []string, err error) {
	defer func() { _ = rc.Close() }()
	seq := s.zipDirSeq + 1
	s.zipDirSeq = seq
	scratch := filepath.Join(s.tempRoot, fmt.Sprintf("zipsrc-%d", seq))

	// 头 16KB 已消费，落 scratch 续拷剩余——不在内存缓冲整个内层 zip（GB 级会炸）
	outf, cerr := os.Create(scratch)
	if cerr != nil {
		return nil, append(notices, display+"（创建失败）"), nil
	}
	n1, _ := outf.Write(head)
	n2, cperr := io.Copy(outf, rc)
	_ = outf.Close()
	if cperr != nil || n1 != len(head) {
		_ = os.Remove(scratch)
		return nil, append(notices, display+"（解压失败）"), nil
	}
	*writtenTotal += int64(n1) + n2
	if *writtenTotal > maxUnzipBytes {
		_ = os.Remove(scratch)
		return nil, notices, fmt.Errorf("解压总量超过上限 %d GB", maxUnzipBytes>>30)
	}
	// 魔数侥幸命中/损坏的内层 zip：降级为提示，不中断整包其余条目
	if zr, oerr := zip.OpenReader(scratch); oerr != nil {
		_ = os.Remove(scratch)
		return nil, append(notices, display+"（内层 zip 无效，跳过）"), nil
	} else {
		_ = zr.Close()
	}

	dest := filepath.Join(s.tempRoot, fmt.Sprintf("zip-%d", seq))
	out, notices, err = s.extractZip(ctx, scratch, dest, depth+1, display, writtenTotal, fileNo, onProgress)
	_ = os.Remove(scratch) // 内容已解出（或已失败），scratch 即删
	return out, notices, err
}

// isZipHead 按内容魔数识别 zip：本地文件头 PK\x03\x04 或空包结尾记录 PK\x05\x06。
// 嵌套判定不依赖扩展名（沿用轮转命名不吃后缀亏的原则）
func isZipHead(head []byte) bool {
	return bytes.HasPrefix(head, []byte("PK\x03\x04")) ||
		bytes.HasPrefix(head, []byte("PK\x05\x06"))
}

// SweepStaleTemp 清扫历史残留的解压临时根（崩溃/强杀未走退出清理、或退出删除被
// 杀毒等占用失败），app 启动时调用。两个护栏防误删活实例目录：
//  1. 年龄：目录修改时间超过 staleTempAge 才收——活会话随解压刷新永远年轻，
//     也关死「刚建根、锁句柄未开」的竞态窗口；
//  2. 探锁：先删目录内 lock 文件，删得动（无句柄持有）或文件不存在（老版本
//     残留/上次清扫留的半残目录）才删整目录；sharing violation 说明有活实例
//     持有，跳过。必须先探锁再删整目录——直接删会先删掉活实例的解压文件再
//     撞锁，留半残。
// 幂等：本次清不掉（数据文件被占用）下次启动再收，稳态不累积
func SweepStaleTemp() {
	sweepStaleTempIn(os.TempDir())
}

// sweepStaleTempIn SweepStaleTemp 的可测内核：对 root 下本应用前缀目录执行清扫
func sweepStaleTempIn(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleTempAge)
	for _, en := range entries {
		if !en.IsDir() || !strings.HasPrefix(en.Name(), "log-hunter-unzip-") {
			continue
		}
		fi, ierr := en.Info()
		if ierr != nil || fi.ModTime().After(cutoff) {
			continue // 太年轻：活会话或刚建的目录，不动
		}
		dir := filepath.Join(root, en.Name())
		if lerr := os.Remove(filepath.Join(dir, tempLockName)); lerr != nil && !os.IsNotExist(lerr) {
			continue // 锁删不动 = 有活实例持有句柄
		}
		_ = os.RemoveAll(dir)
	}
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
