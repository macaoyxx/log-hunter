package logscan

import "strings"

// Level 归一化后的日志级别
type Level int8

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
	LevelUnknown Level = -1
)

// LevelCount 可供前端筛选的级别数量（不含 Unknown）
const LevelCount = 6

var levelNames = [LevelCount]string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}

// String 返回级别显示名
func (l Level) String() string {
	if l >= 0 && int(l) < LevelCount {
		return levelNames[l]
	}
	return ""
}

// normalizeLevel 把各种级别写法（WARNING/E/ERR/CRITICAL…）归一化，无法识别返回 LevelUnknown
func normalizeLevel(s string) Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRACE", "TRC":
		return LevelTrace
	case "DEBUG", "DBG":
		return LevelDebug
	case "INFO", "INF", "NOTICE", "I":
		return LevelInfo
	case "WARN", "WARNING", "W":
		return LevelWarn
	case "ERROR", "ERR", "SEVERE", "E":
		return LevelError
	case "FATAL", "FTL", "CRIT", "CRITICAL", "F", "C":
		return LevelFatal
	}
	return LevelUnknown
}
