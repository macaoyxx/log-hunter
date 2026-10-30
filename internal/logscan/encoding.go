package logscan

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// TextEncoding 文件文本编码
type TextEncoding int

const (
	EncUTF8 TextEncoding = iota
	EncGBK
)

// String 返回编码名
func (e TextEncoding) String() string {
	if e == EncGBK {
		return "GBK"
	}
	return "UTF-8"
}

// detectEncoding 抽样探测编码：合法 UTF-8 优先；否则 GBK 解码成功则 GBK；兜底 UTF-8
func detectEncoding(sample []byte) TextEncoding {
	if utf8.Valid(sample) {
		return EncUTF8
	}
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(sample)
	if err == nil && !bytes.ContainsRune(out, '\uFFFD') {
		return EncGBK
	}
	return EncUTF8
}

// decodeLine 把一行原始字节解码为 UTF-8 字符串，去掉行尾 \r
func decodeLine(enc TextEncoding, b []byte) string {
	if enc != EncUTF8 {
		if out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
			b = out
		}
	}
	return strings.TrimSuffix(string(b), "\r")
}

// encodeKeyword 把用户输入的 UTF-8 关键字转为目标编码下的字节序列（GBK 文件按 GBK 字节匹配）
func encodeKeyword(enc TextEncoding, s string) []byte {
	if enc == EncUTF8 {
		return []byte(s)
	}
	if out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s)); err == nil {
		return out
	}
	return []byte(s)
}
