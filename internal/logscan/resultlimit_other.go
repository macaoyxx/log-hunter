//go:build !windows

package logscan

// totalPhysicalMem 非 Windows 平台兜底：按 8GB 假设换算（本项目仅发布 Windows 产物，
// 其他平台仅供开发调试，无需精确）
func totalPhysicalMem() int64 {
	return 8 << 30
}

// SystemMemory 非 Windows 兜底：总量 8GB、可用 4GB（仅供开发调试展示）
func SystemMemory() (total, avail int64) {
	return 8 << 30, 4 << 30
}
