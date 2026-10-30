package logscan

import "bytes"

// ContextRow 上下文行（目标行的前后行）
type ContextRow struct {
	Offset   int64  `json:"offset"`
	LineNo   int64  `json:"lineNo"`
	TS       int64  `json:"ts"`
	Level    int8   `json:"level"`
	Text     string `json:"text"`
	IsTarget bool   `json:"isTarget"`
}

// readContext 读取 offset 所在行的前后各 N 行：定位目标行行首后向两侧扩窗，窗口不足翻倍重读
func readContext(lf *lineFile, enc TextEncoding, idx *LineIndex, offset, before, after int64) []ContextRow {
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}

	// 第一步：定位目标行行首（往前找换行，窗口不足翻倍）
	lineStart := offset
	for w := int64(4096); ; w *= 4 {
		lo := offset - w
		if lo < 0 {
			lo = 0
		}
		buf, err := lf.readRange(lo, offset)
		if err != nil || len(buf) == 0 {
			if lo == 0 {
				break
			}
			continue
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			lineStart = lo + int64(i) + 1
			break
		}
		if lo == 0 {
			lineStart = 0
			break
		}
	}

	// 第二步：向前扩 before 行：lineStart 紧邻的换行是前文第 1 行的行尾，
	// 因此需要倒数 before+1 个换行才能框出 before 个前文行
	backStart := lineStart
	if before > 0 {
		for w := before*256 + 4096; ; w *= 4 {
			lo := lineStart - w
			if lo < 0 {
				lo = 0
			}
			buf, err := lf.readRange(lo, lineStart)
			if err != nil {
				break
			}
			if int64(bytes.Count(buf, []byte{'\n'})) > before || lo == 0 {
				backStart = lo + contextBackStart(buf, before)
				break
			}
		}
	}

	// 第三步：向后扩 after+1 行
	fwdEnd := lineStart
	for w := (after+1)*256 + 4096; ; w *= 4 {
		end := lineStart + w
		if end >= lf.size {
			end = lf.size
		}
		buf, err := lf.readRange(lineStart, end)
		if err != nil {
			break
		}
		if pos := nthNewline(buf, after+1); pos >= 0 {
			fwdEnd = lineStart + int64(pos)
			break
		}
		if end >= lf.size {
			fwdEnd = lf.size
			break
		}
	}

	// 第四步：一次读出 [backStart, fwdEnd) 并收集行
	buf, err := lf.readRange(backStart, fwdEnd)
	if err != nil || len(buf) == 0 {
		return nil
	}
	var rows []ContextRow
	iterLines(buf, func(off int64, line []byte) {
		abs := backStart + off
		r := ContextRow{
			Offset:   abs,
			Text:     capLine(decodeLine(enc, line)),
			IsTarget: abs == offset,
		}
		if idx != nil {
			r.LineNo = idx.LineNoByOffset(abs)
			if i := int(r.LineNo) - 1; i >= 0 && i < len(idx.ts) {
				r.TS = idx.ts[i]
				r.Level = idx.levels[i]
			}
		}
		rows = append(rows, r)
	})
	return rows
}

// contextBackStart 在 [窗口起点, 目标行行首) 数据中找 before 个前文行的窗口起点（相对下标）：
// 需要倒数 before+1 个换行（倒数第 1 个换行紧邻 lineStart，是前文第 1 行的行尾）；不足则顶到窗口头
func contextBackStart(buf []byte, before int64) int64 {
	need := before + 1
	cnt := int64(0)
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] == '\n' {
			cnt++
			if cnt == need {
				return int64(i + 1)
			}
		}
	}
	return 0
}

// nthNewline 返回第 n 个换行符的下标（0-based，n 从 1 起）；不足返回 -1
func nthNewline(buf []byte, n int64) int {
	cnt := int64(0)
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			cnt++
			if cnt == n {
				return i
			}
		}
	}
	return -1
}
