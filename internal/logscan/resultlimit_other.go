//go:build !windows

package logscan

// totalPhysicalMem 非 Windows 平台兜底：按 8GB 假设换算（本项目仅发布 Windows 产物，
// 其他平台仅供开发调试，无需精确）
func totalPhysicalMem() int64 {
	return 8 << 30
}
