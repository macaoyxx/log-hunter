package logscan

import (
	"context"
	"runtime"
)

// LineIndex 内存行索引：平行数组紧凑存储每行的偏移/时间戳/级别，不落盘
type LineIndex struct {
	offsets []int64
	ts      []int64 // unixNano，0 = 该行无时间戳（多行堆栈等）
	levels  []int8
}

// LineCount 总行数
func (ix *LineIndex) LineCount() int { return len(ix.offsets) }

// LineNoByOffset 二分查找 offset 所在行的行号（1-based）
func (ix *LineIndex) LineNoByOffset(offset int64) int64 {
	return int64(sortSearchInts(ix.offsets, offset)) + 1
}

// TSRange 返回有时间的行的最小/最大时间戳（无任何时间行返回 0,0）
func (ix *LineIndex) TSRange() (min, max int64) {
	for _, t := range ix.ts {
		if t == 0 {
			continue
		}
		if min == 0 || t < min {
			min = t
		}
		if t > max {
			max = t
		}
	}
	return min, max
}

// filterCandidates 按时间范围与级别筛选候选行下标（0-based）；无时间/级别条件时返回 nil 表示不过滤。
// 无时间戳的行（多行堆栈）在时间过滤下被排除——它们附属于上一条有时间戳的日志
func (ix *LineIndex) filterCandidates(tsStart, tsEnd int64, levels map[Level]bool) []int32 {
	if tsStart == 0 && tsEnd == 0 && levels == nil {
		return nil
	}
	out := make([]int32, 0, len(ix.offsets)/8+8)
	for i, t := range ix.ts {
		if tsStart > 0 && (t == 0 || t < tsStart) {
			continue
		}
		if tsEnd > 0 && (t == 0 || t > tsEnd) {
			continue
		}
		if levels != nil && !levels[Level(ix.levels[i])] {
			continue
		}
		out = append(out, int32(i))
	}
	return out
}

// indexProgress 索引构建进度回调（done/total 为字节）
type indexProgress func(done, total int64)

// chunkCols 单块提取的行元数据（offsets/ts/levels 等长）
type chunkCols struct {
	offsets []int64
	ts      []int64
	levels  []int8
}

// buildIndex 并行扫描整个文件构建行索引：各块并发提取行首元数据，按块序合并进平行数组
func buildIndex(ctx context.Context, lf *lineFile, parser *LineParser, progress indexProgress) (*LineIndex, error) {
	chunks, err := lf.chunkPlan()
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return &LineIndex{}, nil
	}
	cols := make([]chunkCols, len(chunks))
	workers := runtime.NumCPU()
	err = lf.parallelChunks(ctx, workers, func(done, total int64) {
		if progress != nil {
			progress(done, total)
		}
	}, func(ci int, chunk []byte, start int64) error {
		var c chunkCols
		iterLines(chunk, func(off int64, line []byte) {
			ts, lv := parser.ParseLine(line)
			c.offsets = append(c.offsets, start+off)
			c.ts = append(c.ts, ts)
			c.levels = append(c.levels, int8(lv))
		})
		cols[ci] = c
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 按块序合并（块内、块间 offset 均升序）
	var total int
	for _, c := range cols {
		total += len(c.offsets)
	}
	ix := &LineIndex{
		offsets: make([]int64, 0, total),
		ts:      make([]int64, 0, total),
		levels:  make([]int8, 0, total),
	}
	for _, c := range cols {
		ix.offsets = append(ix.offsets, c.offsets...)
		ix.ts = append(ix.ts, c.ts...)
		ix.levels = append(ix.levels, c.levels...)
	}
	return ix, nil
}
