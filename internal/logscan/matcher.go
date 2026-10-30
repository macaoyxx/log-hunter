package logscan

import (
	"bytes"
	"regexp"
)

// textMatcher 文本条件匹配器：包含词/排除词/正则，关键字已按文件编码预转换
type textMatcher struct {
	includes   [][]byte
	excludes   [][]byte
	re         *regexp.Regexp
	includeAnd bool
	hasInclude bool
}

// CompileRegexForValidation 校验正则语法（供输入框即时校验用）
func CompileRegexForValidation(s string) (*regexp.Regexp, error) {
	return regexp.Compile(s)
}

// newTextMatcher 按查询条件和目标文件编码构造匹配器；正则非法时返回错误
func newTextMatcher(keywords, excludes []string, regex string, includeAnd bool, enc TextEncoding) (*textMatcher, error) {
	m := &textMatcher{includeAnd: includeAnd}
	for _, k := range keywords {
		if k != "" {
			m.includes = append(m.includes, encodeKeyword(enc, k))
		}
	}
	for _, k := range excludes {
		if k != "" {
			m.excludes = append(m.excludes, encodeKeyword(enc, k))
		}
	}
	m.hasInclude = len(m.includes) > 0
	if regex != "" {
		// GBK 文件按 GBK 字节匹配正则（ASCII 语义不受影响；含中文类 \x{4e00} 的正则不适用 GBK 文件）
		if enc != EncUTF8 {
			regex = string(encodeKeyword(enc, regex))
		}
		re, err := regexp.Compile(regex)
		if err != nil {
			return nil, err
		}
		m.re = re
	}
	return m, nil
}

// match 判断一行是否命中：排除词一票否决；正则（若有）必须命中；包含词按 AND/OR 语义
func (m *textMatcher) match(line []byte) bool {
	for _, ex := range m.excludes {
		if bytes.Contains(line, ex) {
			return false
		}
	}
	if m.re != nil && !m.re.Match(line) {
		return false
	}
	if !m.hasInclude {
		return m.re != nil // 只有正则时由正则决定；两者皆空不算文本条件（调用方不应传入）
	}
	if m.includeAnd {
		for _, in := range m.includes {
			if !bytes.Contains(line, in) {
				return false
			}
		}
		return true
	}
	for _, in := range m.includes {
		if bytes.Contains(line, in) {
			return true
		}
	}
	return false
}

// empty 是否完全没有文本条件
func (m *textMatcher) empty() bool { return !m.hasInclude && len(m.excludes) == 0 && m.re == nil }
