package logscan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
)

// chunkSize 单个并行分块的目标大小（16MB，兼顾并行度、缓冲内存与超长行容忍度）
const chunkSize = int64(16 << 20)

// alignProbe 计算块边界对齐时预读的尾部字节数
const alignProbe = int64(4096)

// lineFile 只读文件访问：os.File + ReadAt 分块读，避免整文件载入内存
type lineFile struct {
	path string
	f    *os.File
	size int64
}

// openLineFile 打开只读文件
func openLineFile(path string) (*lineFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.IsDir() {
		_ = f.Close()
		return nil, errors.New("是目录不是文件")
	}
	return &lineFile{path: path, f: f, size: fi.Size()}, nil
}

// Size 文件大小
func (lf *lineFile) Size() int64 { return lf.size }

// Close 关闭句柄
func (lf *lineFile) Close() error { return lf.f.Close() }

// readAt 读满 buf（ReadAt 合约：err==nil 即读满）
func (lf *lineFile) readAt(buf []byte, off int64) error {
	_, err := lf.f.ReadAt(buf, off)
	return err
}

// readRange 读取 [start,end) 返回新缓冲；范围越界按文件尾截断
func (lf *lineFile) readRange(start, end int64) ([]byte, error) {
	if end > lf.size {
		end = lf.size
	}
	if start >= end {
		return nil, nil
	}
	buf := make([]byte, end-start)
	if err := lf.readAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// chunkPlan 预计算分块边界：每块 [start,end)，end 对齐到块尾附近的换行符（块间不拆行）。
// 对齐探测只读每块末尾 alignProbe 字节；块尾探测窗内无换行（超长行）则该行容忍截断
func (lf *lineFile) chunkPlan() ([][2]int64, error) {
	var chunks [][2]int64
	var probe []byte
	start := int64(0)
	for start < lf.size {
		end := start + chunkSize
		if end >= lf.size {
			end = lf.size
		} else {
			ps := end - alignProbe
			if ps < start {
				ps = start
			}
			if cap(probe) < int(end-ps) {
				probe = make([]byte, end-ps)
			}
			probe = probe[:end-ps]
			if err := lf.readAt(probe, ps); err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			if i := bytes.LastIndexByte(probe, '\n'); i >= 0 {
				end = ps + int64(i) + 1 // 收缩到探测窗内最后一个换行之后
			}
		}
		if end <= start { // 防御：对齐后为空则放弃对齐
			end = start + chunkSize
			if end > lf.size {
				end = lf.size
			}
		}
		chunks = append(chunks, [2]int64{start, end})
		start = end
	}
	return chunks, nil
}

// bufPool 扫描缓冲复用池，控制峰值内存（workers × chunkSize）
type bufPool struct{ ch chan []byte }

func newBufPool(n int) *bufPool { return &bufPool{ch: make(chan []byte, n)} }

func (p *bufPool) get(n int) []byte {
	select {
	case b := <-p.ch:
		if cap(b) >= n {
			return b[:n]
		}
		return make([]byte, n) // 容量不足（块对齐后长度不一），放弃复用
	default:
		return make([]byte, n)
	}
}

func (p *bufPool) put(b []byte) {
	select {
	case p.ch <- b:
	default: // 池满丢弃，交给 GC
	}
}

// parallelChunks 并行处理所有块：worker 从池取缓冲 ReadAt 读块后回调 handle（块序号, 块数据, 块起点）。
// handle 返回 error 时记录首个错误；ctx 取消时尽快退出。注意：块缓冲来自复用池，handle 返回后失效
func (lf *lineFile) parallelChunks(ctx context.Context, workers int, progress func(done, total int64), handle func(ci int, chunk []byte, start int64) error) error {
	chunks, err := lf.chunkPlan()
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	if workers > len(chunks) {
		workers = len(chunks)
	}
	if workers < 1 {
		workers = 1
	}

	var (
		wg       sync.WaitGroup
		firstErr atomic.Value
		done     atomic.Int64
		next     atomic.Int64
	)
	setErr := func(err error) {
		firstErr.CompareAndSwap(nil, err)
	}
	getErr := func() error {
		if v, ok := firstErr.Load().(error); ok {
			return v
		}
		return nil
	}
	pool := newBufPool(workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ci := int(next.Add(1)) - 1
				if ci >= len(chunks) {
					return
				}
				if err := ctx.Err(); err != nil {
					setErr(err)
					return
				}
				c := chunks[ci]
				n := int(c[1] - c[0])
				buf := pool.get(n)
				if err := lf.readAt(buf[:n], c[0]); err != nil && !errors.Is(err, io.EOF) {
					setErr(err)
					pool.put(buf)
					return
				}
				herr := handle(ci, buf[:n], c[0])
				pool.put(buf)
				if herr != nil {
					setErr(herr)
					return
				}
				if progress != nil {
					progress(done.Add(int64(n)), lf.size)
				}
			}
		}()
	}
	wg.Wait()
	if e := getErr(); e != nil {
		return e
	}
	return ctx.Err()
}

// iterLines 遍历 data 内的每一行（不含换行符，可能带 \r），回调 (行首相对偏移, 行内容)
func iterLines(data []byte, fn func(offset int64, line []byte)) {
	var off int64
	for p := 0; p < len(data); {
		rel := bytes.IndexByte(data[p:], '\n')
		end := len(data)
		if rel >= 0 {
			end = p + rel
		}
		fn(off+int64(p), data[p:end])
		if rel < 0 {
			break
		}
		p = end + 1
	}
}

// lineEndRel 返回行首相对偏移 off 所在行的结尾下标（不含 \n）
func lineEndRel(data []byte, off int64) int64 {
	if i := bytes.IndexByte(data[off:], '\n'); i >= 0 {
		return off + int64(i)
	}
	return int64(len(data))
}

// seqReader 顺序读游标：按升序偏移取行，缓冲复用，适合候选行批量访问
type seqReader struct {
	lf   *lineFile
	buf  []byte
	base int64 // buf[0] 对应的文件偏移
}

const seqBufSize = int64(4 << 20)

// maxSeqLineBytes 超长行 fallback 的最大读量：不封顶会读到文件尾——单行 >4MB
// 且其后还有数 GB 内容时一次性分配 GB 级内存直接 OOM；16MB 仍未遇换行按病态
// 行截断返回（单行显示防御上限 64KB，16MB 已极宽裕）
const maxSeqLineBytes = int64(16 << 20)

// line 返回 off 起的一行（不含 \n）；自动保证该行完整落在缓冲内
func (sr *seqReader) line(off int64) ([]byte, error) {
	if off < sr.base || off >= sr.base+int64(len(sr.buf)) {
		if err := sr.fill(off); err != nil {
			return nil, err
		}
	}
	rel := off - sr.base
	if i := bytes.IndexByte(sr.buf[rel:], '\n'); i >= 0 {
		return sr.buf[rel : rel+int64(i)], nil
	}
	// 行尾换行不在缓冲内（超长行）：读受限区间找换行（见 maxSeqLineBytes 注释）
	end := sr.lf.size
	if limit := off + maxSeqLineBytes; end > limit {
		end = limit
	}
	tail, err := sr.lf.readRange(off, end)
	if err != nil {
		return nil, err
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		return tail[:i], nil
	}
	return tail, nil
}

// fill 从 off 起重填缓冲
func (sr *seqReader) fill(off int64) error {
	if cap(sr.buf) < int(seqBufSize) {
		sr.buf = make([]byte, seqBufSize)
	}
	end := off + seqBufSize
	if end > sr.lf.size {
		end = sr.lf.size
	}
	n := int(end - off)
	err := sr.lf.readAt(sr.buf[:n], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	sr.buf, sr.base = sr.buf[:n], off
	return nil
}

// sortSearchInts 二分查找（行号定位用）
func sortSearchInts(a []int64, v int64) int {
	return sort.Search(len(a), func(i int) bool { return a[i] >= v })
}
