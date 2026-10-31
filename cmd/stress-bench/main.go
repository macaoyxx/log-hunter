// stress-bench 全功能压力基准：双模式（单文件 20G / 多文件 10×~2G 共 20G），
// 自带数据生成（多文件时间交叠模拟多节点），内存采样（工作集/堆）+ 全环节耗时。
// 用法:
//
//	go run ./cmd/stress-bench -mode single   # 单文件 20GB
//	go run ./cmd/stress-bench -mode multi    # 10 文件共 20GB（8 std 节点 + 2 json 服务）
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"
	"unsafe"

	"log-hunter/internal/logscan"
)

var (
	mode = flag.String("mode", "multi", "single=单文件 20GB；multi=10 文件共 20GB")
	dir  = flag.String("dir", `D:\code\bench-20g`, "数据与压测目录")
)

// ---------- 内存采样（Windows GetProcessMemoryInfo） ----------

func workingSetMB() float64 {
	var pmc struct {
		cb, PageFaultCount uint32
		PeakWorkingSetSize, WorkingSetSize, QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage,
		QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage, PagefileUsage, PeakPagefileUsage uintptr
	}
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	proc, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess").Call()
	syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo").Call(
		proc, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb))
	return float64(pmc.WorkingSetSize) / (1 << 20)
}

var mark = time.Now()

func step(name string) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("%-34s %10v   工作集 %8.1f MB   堆 %8.1f MB\n",
		name, time.Since(mark).Round(time.Millisecond), workingSetMB(), float64(ms.HeapAlloc)/(1<<20))
	mark = time.Now()
}

func sinceMark() time.Duration {
	d := time.Since(mark)
	mark = time.Now()
	return d
}

func main() {
	flag.Parse()
	os.MkdirAll(*dir, 0755)

	var paths []string
	switch *mode {
	case "single":
		paths = []string{filepath.Join(*dir, "app-single-20g.log")}
		ensureGen(paths[0], 20<<30, func() { genStdLog(paths[0], 20<<30, 42, 0) })
	case "multi":
		// 8 个 std 节点日志（时间基线分钟级错开交叠，模拟多节点同时段运行——
		// 时间合并视图的真实场景）+ 2 个 JSON 服务日志
		for i := 0; i < 8; i++ {
			paths = append(paths, filepath.Join(*dir, fmt.Sprintf("node-%02d.log", i+1)))
		}
		for i := 0; i < 2; i++ {
			paths = append(paths, filepath.Join(*dir, fmt.Sprintf("svc-%c.jsonl", 'a'+i)))
		}
		for i, p := range paths {
			if i < 8 {
				ensureGen(p, 2400<<20, func() { genStdLog(p, 2400<<20, int64(100+i), i) }) // 各 2.34GiB，seed 与时间偏移都不同
			} else {
				ensureGen(p, 800<<20, func() { genJSONLog(p, 800<<20, int64(200+i), i-8) }) // 各 0.78GiB（800MB）
			}
		}
	default:
		fmt.Println("mode 只支持 single / multi")
		os.Exit(1)
	}

	var totalBytes int64
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Println("文件缺失:", p, err)
			os.Exit(1)
		}
		totalBytes += fi.Size()
	}
	fmt.Printf("\n== [%s 模式] %d 文件 %.1f GB，开始压测 ==\n\n", *mode, len(paths), float64(totalBytes)/(1<<30))

	s := logscan.NewSession()
	defer s.ClearAll()
	runtime.GC()
	step("启动基线")

	_, errs, _ := s.AddFiles(context.Background(), paths, nil)
	if len(errs) > 0 {
		fmt.Println("加载错误:", errs)
		os.Exit(1)
	}
	step("① 加载+建索引")

	var ids []int64
	var tsMax int64
	var lines int64
	for _, f := range s.Files() {
		ids = append(ids, f.ID)
		lines += f.LineCnt
		if f.TSMax > tsMax {
			tsMax = f.TSMax
		}
	}
	fmt.Printf("   总行数 %d 万\n\n", lines/10000)

	ctx := context.Background()
	st, _ := s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids}, nil)
	step(fmt.Sprintf("② 全览（%d 万行）", st.Total/10000))

	s.GetResults(0, 300)
	step("③ 首屏 300 行")
	s.GetResults(st.Total/2, 300)
	step("④ 中段窗口")
	s.GetResults(st.Total-300, 300)
	step("⑤ 尾段窗口")

	st, _ = s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids, Keywords: []string{"ERROR"}}, nil)
	step(fmt.Sprintf("⑥ 关键字 ERROR（%d 条）", st.Total))
	s.GetResults(0, 300)
	step("   结果首屏")

	st, _ = s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids, Regex: `order-\d+`}, nil)
	step(fmt.Sprintf("⑦ 正则 order-\\d+（%d 条）", st.Total))

	start := tsMax - 10*60*int64(time.Second)
	st, _ = s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids, TSStart: start}, nil)
	step(fmt.Sprintf("⑧ 时间范围最后10分钟（%d 条）", st.Total))

	st, _ = s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids, TSStart: start, Keywords: []string{"ERROR"}}, nil)
	step(fmt.Sprintf("⑨ 时间+关键字（%d 条）", st.Total))

	st, _ = s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids, Levels: []int8{3}}, nil)
	step(fmt.Sprintf("⑩ 级别 WARN 过滤（%d 条）", st.Total))

	// 全览 → 合并重排是用户感知卡点：全量重排取 3 次中位
	s.RunQuery(ctx, &logscan.QuerySpec{FileIDs: ids}, nil)
	var costs []time.Duration
	for i := 0; i < 3; i++ {
		s.SetMerged(true)
		costs = append(costs, sinceMark())
		s.SetMerged(false)
	}
	sort.Slice(costs, func(a, b int) bool { return costs[a] < costs[b] })
	fmt.Printf("%-34s %10v（全量重排 3 次中位）\n", "⑪ 切时间合并", costs[1])

	s.SetMerged(true)
	s.GetResults(0, 300)
	step("   合并序首屏")
	s.GetResults(st.Total/2, 300)
	step("   合并序中段窗口")

	s.SetMerged(false)
	step("⑫ 切回平铺")
	fmt.Println("\n== 压测完成 ==")
}

// ensureGen 数据文件已存在且足量则复用（压测复跑不重造几十 GB）
func ensureGen(path string, size int64, gen func()) {
	if fi, err := os.Stat(path); err == nil && fi.Size() >= size {
		fmt.Printf("复用 %s（%.1f GB）\n", filepath.Base(path), float64(fi.Size())/(1<<30))
		return
	}
	gen()
}

// ---------- 数据生成（与 logscan-bench 同款内容模型，参数化 seed 与时间偏移） ----------

// genStdLog 标准文本格式节点日志：时间从 2024-06-15 08:00 起 + offset 分钟，多级别含中文
func genStdLog(path string, size int64, seed int64, offsetMin int) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Println("创建失败:", err)
		os.Exit(1)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()

	rng := rand.New(rand.NewSource(seed))
	ts := time.Date(2024, 6, 15, 8, 0, 0, 0, time.Local).Add(time.Duration(offsetMin) * time.Minute)
	var total int64
	services := []string{"order-svc", "pay-svc", "user-svc", "stock-svc", "gateway"}
	msgs := []string{
		"order created id=%d user=u%d amount=%.2f status=OK",
		"query user profile uid=%d cache=hit cost=%dms",
		"inventory deduct sku=SKU%d left=%d",
		"request served path=/api/v1/order/%d status=200 cost=%dms",
		"payment failed code=E502 order=%d msg=连接上游超时",
		"db slow sql select * from orders where id=%d cost=%dms",
	}
	for total < size {
		ts = ts.Add(time.Duration(rng.Intn(5)+1) * time.Millisecond)
		svc := services[rng.Intn(len(services))]
		var lv, msg string
		switch p := rng.Intn(100); {
		case p < 70:
			lv = "INFO"
			msg = fmt.Sprintf(msgs[rng.Intn(4)], rng.Intn(99999), rng.Intn(8888), rng.Float64()*500, rng.Intn(200))
		case p < 85:
			lv = "WARN"
			msg = fmt.Sprintf("retry queue depth=%d task=%d", rng.Intn(50), rng.Intn(9999))
		case p < 97:
			lv = "ERROR"
			if rng.Intn(2) == 0 {
				msg = fmt.Sprintf("payment failed code=E502 order=%d msg=连接上游超时", rng.Intn(99999))
			} else {
				msg = fmt.Sprintf("rpc call failed target=%s timeout after %dms", svc, 3000+rng.Intn(2000))
			}
		default:
			lv = "FATAL"
			msg = "service crashed signal=SIGSEGV core dumped"
		}
		line := fmt.Sprintf("%s %-5s [%s] %s\n", ts.Format("2006-01-02 15:04:05.000"), lv, svc, msg)
		w.WriteString(line)
		total += int64(len(line))
	}
	fmt.Printf("生成 %s（%.1f GB）\n", filepath.Base(path), float64(size)/(1<<30))
}

// genJSONLog JSON Lines 服务日志
func genJSONLog(path string, size int64, seed int64, offsetMin int) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Println("创建失败:", err)
		os.Exit(1)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()

	rng := rand.New(rand.NewSource(seed))
	ts := time.Date(2024, 6, 15, 8, 0, 0, 0, time.UTC).Add(time.Duration(offsetMin) * time.Minute)
	var total int64
	for total < size {
		ts = ts.Add(time.Duration(rng.Intn(4)+1) * time.Millisecond)
		lv := "INFO"
		if p := rng.Intn(10); p < 7 {
			lv = "INFO"
		} else if p < 9 {
			lv = "WARN"
		} else {
			lv = "ERROR"
		}
		line := fmt.Sprintf(`{"timestamp":"%s","level":"%s","service":"notify-svc","msg":"推送任务处理","taskId":%d,"cost":%d}`+"\n",
			ts.UTC().Format("2006-01-02T15:04:05.000Z07:00"), lv, rng.Intn(999999), rng.Intn(500))
		w.WriteString(line)
		total += int64(len(line))
	}
	fmt.Printf("生成 %s（%.1f GB）\n", filepath.Base(path), float64(size)/(1<<30))
}
