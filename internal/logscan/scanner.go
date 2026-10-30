package logscan

import (
	"context"
	"runtime"
)

// scanProgress 扫描进度回调（done/total 为字节）
type scanProgress func(done, total int64)

// matchRef 命中行引用：偏移 + 行长度（供结果高度估算，前端动态行高虚拟滚动用）
type matchRef struct {
	off, ln int64
}

// scanFile 并行扫全文件做文本匹配，返回各块命中的行偏移与行长（外层按块序、内层按偏移序）
func scanFile(ctx context.Context, lf *lineFile, matcher *textMatcher, progress scanProgress) ([][]matchRef, error) {
	chunks, err := lf.chunkPlan()
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	matches := make([][]matchRef, len(chunks))
	err = lf.parallelChunks(ctx, runtime.NumCPU(), func(done, total int64) {
		if progress != nil {
			progress(done, total)
		}
	}, func(ci int, chunk []byte, start int64) error {
		var ms []matchRef
		iterLines(chunk, func(off int64, line []byte) {
			if matcher.match(line) {
				ms = append(ms, matchRef{off: start + off, ln: int64(len(line))})
			}
		})
		matches[ci] = ms
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}
