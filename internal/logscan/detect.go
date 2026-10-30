package logscan

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// tsLayouts 常见时间戳格式，探测期锁定其中一种供索引时使用（.999 小数秒可选，逗号版本为 Java 风格）
var tsLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05,999999999",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006/01/02 15:04:05.999999999",
	"02/Jan/2006:15:04:05 -0700",
	"Jan 2 15:04:05",
}

// builtinTemplates 内置行格式模板：从行首提取时间戳（ts）与级别（level）捕获组
var builtinTemplates = compileTemplates([]struct{ name, pattern string }{
	{"std", `^\s*(?P<ts>\d{4}[-/]\d{2}[-/]\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[., ]\d{1,9})?)(?:[^\dA-Za-z]+(?P<level>[A-Za-z]+))?`},
	{"bracket-level", `^\s*(?P<ts>\d{4}[-/]\d{2}[-/]\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[., ]\d{1,9})?)\s*(?:\[[^\]]*\]\s*)*\[(?P<level>[A-Za-z]+)\]`},
	{"bracket", `^\s*\[(?P<ts>\d{4}[-/]\d{2}[-/]\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[., ]\d{1,9})?)\]\s*\[(?P<level>[A-Za-z]+)\]`},
	{"level-first", `^\s*(?P<level>TRACE|TRC|DEBUG|DBG|INFO|INF|NOTICE|WARN(?:ING)?|ERROR|ERR|SEVERE|FATAL|FTL|CRIT(?:ICAL)?)[\s:\-|]+(?P<ts>\d{4}[-/]\d{2}[-/]\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[., ]\d{1,9})?)`},
	{"syslog", `^\s*(?P<ts>[A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2})`},
	{"httpd", `^\s*\[(?P<ts>\d{2}/[A-Z][a-z]{2}/\d{4}:\d{2}:\d{2}:\d{2}\s+[+-]\d{4})\]`},
})

// JSON 行的字段名候选
var jsonTSKeys = []string{"timestamp", "time", "ts", "@timestamp", "datetime", "date"}
var jsonLevelKeys = []string{"level", "lvl", "severity", "level_name", "loglevel"}

type namedTemplate struct {
	name         string
	re           *regexp.Regexp
	tsIdx, lvIdx int
	layout       string // 探测期锁定的时间 layout（仅探测副本上使用）
}

func compileTemplates(defs []struct{ name, pattern string }) []namedTemplate {
	out := make([]namedTemplate, 0, len(defs))
	for _, d := range defs {
		re := regexp.MustCompile(d.pattern)
		out = append(out, namedTemplate{
			name:  d.name,
			re:    re,
			tsIdx: re.SubexpIndex("ts"),
			lvIdx: re.SubexpIndex("level"),
		})
	}
	return out
}

// LineParser 行解析器：按识别出的格式从行首提取时间戳与级别
type LineParser struct {
	Kind     string // "text" | "json" | "plain"
	Template string // 模板名，plain 时为空
	layout   string // 探测期锁定的时间 layout，空则解析时逐个尝试
	re       *namedTemplate
	tsKey    string
	lvKey    string
	Loc      *time.Location
}

// HasTime 该格式是否含时间戳
func (p *LineParser) HasTime() bool { return p != nil && p.Kind != "plain" && (p.Kind == "json" || p.re.tsIdx >= 0) }

// parseTS 解析时间戳为 unixNano；解析失败返回 0
func (p *LineParser) parseTS(s string) int64 {
	if p.layout != "" {
		if t, err := time.ParseInLocation(p.layout, normalizeTS(s), p.Loc); err == nil {
			return t.UnixNano()
		}
		return 0
	}
	return p.tryLayouts(s)
}

// normalizeTS 规范化时间戳串：
// 1) "12:00:00 699"（空格毫秒，AgileBPM 等格式）→ "12:00:00.699"
// 2) 超长小数截到 9 位，避免 Parse 失败
func normalizeTS(s string) string {
	// 空格毫秒：秒后紧跟 空格+1~3 位数字 结尾（后面不能再有别的数字段）
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c == ' ' {
			// s[i+1:] 必须是纯数字且 1~3 位，且冒号在合理位置（HH:MM:SS）
			d := s[i+1:]
			if len(d) >= 1 && len(d) <= 3 && isDigits(d) && strings.Count(s[:i], ":") == 2 {
				return normalizeTS(s[:i] + "." + d)
			}
		}
		break
	}
	// 小数秒超长截断
	for i := 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == ',' {
			frac := s[i+1:]
			if len(frac) > 9 && isDigits(frac) {
				return s[:i+1] + frac[:9]
			}
		}
	}
	return s
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (p *LineParser) tryLayouts(s string) int64 {
	s = normalizeTS(s)
	for _, l := range tsLayouts {
		if t, err := time.ParseInLocation(l, s, p.Loc); err == nil {
			return t.UnixNano()
		}
	}
	return 0
}

// pickLayout 从样本时间串锁定 layout
func (p *LineParser) pickLayout(s string) {
	s = normalizeTS(s)
	for _, l := range tsLayouts {
		if _, err := time.ParseInLocation(l, s, p.Loc); err == nil {
			p.layout = l
			return
		}
	}
}

// ParseLine 从行首提取时间戳与级别；ts=0 表示无/解析失败，level=LevelUnknown 表示无级别
func (p *LineParser) ParseLine(line []byte) (int64, Level) {
	if p == nil || p.Kind == "plain" {
		return 0, LevelUnknown
	}
	if p.Kind == "json" {
		return p.parseJSONLine(line)
	}
	m := p.re.re.FindSubmatchIndex(line)
	if m == nil {
		return 0, LevelUnknown
	}
	var ts int64
	if p.re.tsIdx >= 0 && m[2*p.re.tsIdx] >= 0 {
		ts = p.parseTS(string(line[m[2*p.re.tsIdx]:m[2*p.re.tsIdx+1]]))
	}
	lv := LevelUnknown
	if p.re.lvIdx >= 0 && m[2*p.re.lvIdx] >= 0 {
		lv = normalizeLevel(string(line[m[2*p.re.lvIdx]:m[2*p.re.lvIdx+1]]))
	}
	return ts, lv
}

// parseJSONLine 解析 JSON Lines 行，提取时间戳与级别字段
func (p *LineParser) parseJSONLine(line []byte) (int64, Level) {
	var obj map[string]any
	if err := json.Unmarshal(trimCR(line), &obj); err != nil {
		return 0, LevelUnknown
	}
	var ts int64
	if v, ok := obj[p.tsKey]; ok {
		switch x := v.(type) {
		case string:
			ts = p.parseTS(x)
		case float64: // 数字时间戳：按量级区分秒/毫秒
			ns := int64(x)
			switch {
			case ns > 1e17: // 纳秒
			case ns > 1e14: // 微秒
				ns *= 1e3
			case ns > 1e11: // 毫秒
				ns *= 1e6
			default: // 秒
				ns *= 1e9
			}
			ts = ns
		}
	}
	lv := LevelUnknown
	if v, ok := obj[p.lvKey]; ok {
		switch x := v.(type) {
		case string:
			lv = normalizeLevel(x)
		case float64:
			if x >= 0 && x < LevelCount {
				lv = Level(x)
			}
		}
	}
	return ts, lv
}

func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}

// DetectFormat 从样本（文件前若干行）探测行格式，返回对应解析器；识别失败返回 plain
func DetectFormat(sample []byte, loc *time.Location) *LineParser {
	if loc == nil {
		loc = time.Local
	}
	lines := sampleLines(sample, maxSampleLines)
	if len(lines) == 0 {
		return &LineParser{Kind: "plain", Loc: loc}
	}

	// JSON 行探测：统计能解析出 ts/level 字段的行占比
	jsonHits, tsKey, lvKey := detectJSON(lines)
	if jsonHits*10/len(lines) >= 6 {
		p := &LineParser{Kind: "json", tsKey: tsKey, lvKey: lvKey, Loc: loc}
		if ts, _ := p.ParseLine(lines[0]); ts > 0 {
			// 用样本锁定 layout，避免每行全格式尝试
			if v := extractJSONString(lines[0], tsKey); v != "" {
				p.pickLayout(v)
			}
		}
		return p
	}

	// 文本模板探测：取综合得分最高且 >= 60% 的模板
	// （同时识别出有效级别的模板计双分——同样能匹配时间时，级别信息更全的模板胜出）
	var best *namedTemplate
	bestHits := 0
	for i := range builtinTemplates {
		src := &builtinTemplates[i]
		t := namedTemplate{name: src.name, re: src.re, tsIdx: src.tsIdx, lvIdx: src.lvIdx}
		hits := 0
		for _, ln := range lines {
			m := t.re.FindSubmatchIndex(ln)
			if m == nil {
				continue
			}
			// ts 组必须能解析成时间才算有效命中（避免 "2024-13-99" 之类误配）
			if t.tsIdx >= 0 && m[2*t.tsIdx] >= 0 {
				s := normalizeTS(string(ln[m[2*t.tsIdx]:m[2*t.tsIdx+1]]))
				if l := matchLayout(s, loc); l != "" {
					hits++
					if t.layout == "" {
						t.layout = l
					}
					// 级别组有效（能归一化）则双分
					if t.lvIdx >= 0 && m[2*t.lvIdx] >= 0 &&
						normalizeLevel(string(ln[m[2*t.lvIdx]:m[2*t.lvIdx+1]])) != LevelUnknown {
						hits++
					}
				}
			}
		}
		if hits > bestHits {
			best, bestHits = &t, hits
		}
	}
	if best != nil && bestHits*10/len(lines) >= 12 { // 双分制下阈值等比放宽
		return &LineParser{Kind: "text", Template: best.name, re: best, layout: best.layout, Loc: loc}
	}
	return &LineParser{Kind: "plain", Loc: loc}
}

func matchLayout(s string, loc *time.Location) string {
	for _, l := range tsLayouts {
		if _, err := time.ParseInLocation(l, s, loc); err == nil {
			return l
		}
	}
	return ""
}

func detectJSON(lines [][]byte) (hits int, tsKey, lvKey string) {
	for _, ln := range lines {
		var obj map[string]any
		if err := json.Unmarshal(trimCR(ln), &obj); err != nil {
			return 0, "", ""
		}
		tk, lk := "", ""
		for _, k := range jsonTSKeys {
			if _, ok := obj[k]; ok {
				tk = k
				break
			}
		}
		for _, k := range jsonLevelKeys {
			if _, ok := obj[k]; ok {
				lk = k
				break
			}
		}
		if tk == "" && lk == "" {
			continue
		}
		hits++
		if hits == 1 {
			tsKey, lvKey = tk, lk
		}
	}
	return hits, tsKey, lvKey
}

func extractJSONString(line []byte, key string) string {
	var obj map[string]any
	if err := json.Unmarshal(trimCR(line), &obj); err != nil {
		return ""
	}
	if v, ok := obj[key].(string); ok {
		return v
	}
	return ""
}

// sampleLines 取样本中最多 max 行非空行
func sampleLines(sample []byte, max int) [][]byte {
	var out [][]byte
	for len(sample) > 0 && len(out) < max {
		i := bytes.IndexByte(sample, '\n')
		var ln []byte
		if i < 0 {
			ln, sample = sample, nil
		} else {
			ln, sample = sample[:i], sample[i+1:]
		}
		if n := len(ln); n > 0 && ln[n-1] == '\r' {
			ln = ln[:n-1]
		}
		if len(bytes.TrimSpace(ln)) > 0 {
			out = append(out, ln)
		}
	}
	return out
}

// maxSampleLines 样本行数上限（跨区采样后样本更分散，取更多行保证代表性）
const maxSampleLines = 300
