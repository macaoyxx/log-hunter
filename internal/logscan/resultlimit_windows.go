//go:build windows

package logscan

import (
	"syscall"
	"unsafe"
)

// totalPhysicalMem 返回物理内存总字节数（Windows GlobalMemoryStatusEx，标准库 syscall，零新依赖）
func totalPhysicalMem() int64 {
	// 结构体布局须与 MEMORYSTATUSEX 一致；dwLength 必须先置 sizeof
	var m struct {
		dwLength               uint32
		dwMemoryLoad           uint32
		ullTotalPhys           uint64
		ullAvailPhys           uint64
		ullTotalPageFile       uint64
		ullAvailPageFile       uint64
		ullTotalVirtual        uint64
		ullAvailVirtual        uint64
		ullAvailExtendedVirtual uint64
	}
	m.dwLength = uint32(unsafe.Sizeof(m))
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0 // 调用失败走 calcResultLimit 的下限保底
	}
	return int64(m.ullTotalPhys)
}
