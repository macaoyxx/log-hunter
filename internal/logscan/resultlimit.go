package logscan

// 结果条数上限按物理内存自适应：固定 200 万对大内存机器过于保守（用户反馈），
// 宽查询被过早截断。上限 = 物理内存 × 25% ÷ 每行结果内存估算，clamp 到
// [下限, 封顶]：下限保证小内存机器不劣于旧默认；封顶防止极端大内存机器
// 物化出无意义的天量结果（2000 万条 ≈ 1.28GB 结果内存，已覆盖所有合理排查）。
const (
	resultLimitFloor = 2_000_000  // 下限保底（对齐旧固定上限，不因内存小而更紧）
	resultLimitCap   = 20_000_000 // 封顶（约 1.28GB 结果内存）
	resultRowBytes   = 64         // ResultRow 结构体内存估算（字段+string 头+padding）
)

// calcResultLimit 由物理内存换算结果上限（纯函数便于单测边界）
func calcResultLimit(totalPhys int64) int {
	if totalPhys <= 0 {
		return resultLimitFloor
	}
	n := int(totalPhys / 4 / resultRowBytes) // 25% 内存换算
	if n < resultLimitFloor {
		return resultLimitFloor
	}
	if n > resultLimitCap {
		return resultLimitCap
	}
	return n
}

// MemorySoftLimit Go 运行时软内存上限（main 启动时经 debug.SetMemoryLimit 生效）：
// 物理内存×50%（clamp 2~16GB）。与结果条数上限各司其职——条数上限管「物化多少行」，
// 软上限管「GC 以多大堆为目标加压」：超限时 GC 自动更积极，防止工作集虚高不归还
func MemorySoftLimit() int64 {
	const floor, cap64 = int64(2 << 30), int64(16 << 30)
	n := totalPhysicalMem() / 2
	if n < floor {
		return floor
	}
	if n > cap64 {
		return cap64
	}
	return n
}

// calcMaxUnzipBytes 解压总量上限按物理内存自适应：解压文本落 %TEMP% 磁盘，内存只存
// 元数据索引（17B/行），真正的瓶颈是索引内存而非磁盘——8GB 文本在典型日志（行均
// ~150B）下索引仅 ≈1GB，大内存机器本可容纳远大于 8GB 的合法包；小内存机器则不宜
// 高于 8GB（病态短行时索引可逼近文本体积，有 OOM 风险）。上限 = 物理内存×1，
// clamp 到 [8GB 保底, 128GB 封顶]：保底 = 现状（小机器不更紧）；封顶 = 防超大内存
// 放行无意义天量（zip 炸弹防线仍在此）。系数 1 的理由：典型索引 ≈ 文本 10-20%，
// 文本≈物理内存时索引仅占 ~15% RAM，留足余量
func calcMaxUnzipBytes(totalPhys int64) int64 {
	const floor, cap64 = int64(8 << 30), int64(128 << 30)
	if totalPhys <= 0 || totalPhys < floor {
		return floor
	}
	if totalPhys > cap64 {
		return cap64
	}
	return totalPhys
}
