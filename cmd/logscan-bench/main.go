// logscan-bench 性能验收：生成混合格式大日志，测量索引/查询/上下文耗时
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"log-hunter/internal/logscan"
)

func main() {
	dir := flag.String("dir", filepath.Join(os.TempDir(), "loghunter-bench"), "测试日志目录")
	sizeMB := flag.Int("size", 1024, "生成日志总大小（MB）")
	flag.Parse()

	stdPath := filepath.Join(*dir, "app-std.log")
	jsonPath := filepath.Join(*dir, "svc-json.log")
	fmt.Printf("== 生成测试日志（%d MB，std:text 8 : json 2）==\n", *sizeMB)
	genStart := time.Now()
	n1 := genStdLog(stdPath, int64(*sizeMB)*8/10<<20)
	n2 := genJSONLog(jsonPath, int64(*sizeMB)*2/10<<20)
	fmt.Printf("生成完成：%s(%d 行) + %s(%d 行)，用时 %v\n\n",
		filepath.Base(stdPath), n1, filepath.Base(jsonPath), n2, time.Since(genStart).Round(time.Millisecond))

	s := logscan.NewSession()
	defer s.ClearAll()

	// 1. 加载+索引
	fmt.Println("== 加载并建索引 ==")
	t0 := time.Now()
	added, errs, _ := s.AddFiles(context.Background(), []string{stdPath, jsonPath}, nil)
	if len(errs) > 0 {
		fmt.Println("加载错误:", errs)
		os.Exit(1)
	}
	indexCost := time.Since(t0)
	for _, e := range added {
		fmt.Printf("  %s  %.1f MB  %d 万行  格式=%s  时间范围 %s ~ %s\n",
			e.Name, float64(e.Size)/(1<<20), e.LineCnt/10000, e.Format,
			time.Unix(0, e.TSMin).Format("15:04:05"), time.Unix(0, e.TSMax).Format("15:04:05"))
	}
	fmt.Printf("  索引总耗时: %v  (验收目标 <30s)\n\n", indexCost.Round(time.Millisecond))

	ids := make([]int64, len(added))
	for i, e := range added {
		ids[i] = e.ID
	}

	// 2. 纯关键字查询
	bench := func(name string, spec *logscan.QuerySpec, target time.Duration) {
		t0 := time.Now()
		st, err := s.RunQuery(context.Background(), spec, nil)
		if err != nil {
			fmt.Println("查询错误:", err)
			os.Exit(1)
		}
		flag := "✓"
		if st.CostMs > target.Milliseconds() {
			flag = "✗"
		}
		fmt.Printf("  %-28s 命中 %8d 条  %8v  %s (目标 <%v)\n", name, st.Total, time.Since(t0).Round(time.Millisecond), flag, target)
	}

	fmt.Println("== 查询 ==")
	bench("关键字 ERROR", &logscan.QuerySpec{FileIDs: ids, Keywords: []string{"ERROR"}}, 3*time.Second)
	bench("多关键字 AND(E502)", &logscan.QuerySpec{FileIDs: ids, Keywords: []string{"pay-svc", "E502"}, KeywordAnd: true}, 3*time.Second)
	bench("多关键字 OR(pay-svc|E502)", &logscan.QuerySpec{FileIDs: ids, Keywords: []string{"pay-svc", "E502"}}, 3*time.Second)
	bench("正则 code=E\\d+", &logscan.QuerySpec{FileIDs: ids, Regex: `code=E\d+`}, 3*time.Second)
	bench("排除 INFO(高频词)", &logscan.QuerySpec{FileIDs: ids, Excludes: []string{"status=OK"}}, 3*time.Second)

	// 时间条件：取文件时间范围的中段 1 分钟
	var mid int64
	for _, e := range added {
		mid = e.TSMin + (e.TSMax-e.TSMin)/2
		break
	}
	start := mid
	end := mid + int64(time.Minute)
	bench("时间范围 1 分钟", &logscan.QuerySpec{FileIDs: ids, TSStart: start, TSEnd: end}, time.Second)
	bench("时间+关键字 ERROR", &logscan.QuerySpec{FileIDs: ids, TSStart: start, TSEnd: end, Keywords: []string{"ERROR"}}, time.Second)
	bench("级别 ERROR+FATAL", &logscan.QuerySpec{FileIDs: ids, Levels: []int8{int8(logscan.LevelError), int8(logscan.LevelFatal)}}, 3*time.Second)

	// 3. 上下文
	fmt.Println("== 上下文 ==")
	st, _ := s.RunQuery(context.Background(), &logscan.QuerySpec{FileIDs: ids, Keywords: []string{"E502"}}, nil)
	if st.Total > 0 {
		r := s.GetResults(st.Total/2, 1)[0]
		t := time.Now()
		rows, err := s.GetContext(r.FileID, r.Offset, 50, 50)
		if err != nil {
			fmt.Println("上下文错误:", err)
		} else {
			fmt.Printf("  前后各 50 行 → %d 行  %v  (目标 <50ms)\n", len(rows), time.Since(t).Round(time.Millisecond))
		}
	}

	fmt.Println("\n== 验收完成 ==")
}

// genStdLog 生成标准文本格式日志（时间递增，含中文与多级别）
func genStdLog(path string, size int64) int64 {
	os.MkdirAll(filepath.Dir(path), 0755)
	f, _ := os.Create(path)
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()

	rng := rand.New(rand.NewSource(42))
	ts := time.Date(2024, 6, 15, 8, 0, 0, 0, time.Local)
	var n, total int64
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
			msg = fmt.Sprintf("service crashed signal=SIGSEGV core dumped")
		}
		line := fmt.Sprintf("%s %-5s [%s] %s\n", ts.Format("2006-01-02 15:04:05.000"), lv, svc, msg)
		w.WriteString(line)
		total += int64(len(line))
		n++
	}
	return n
}

// genJSONLog 生成 JSON Lines 格式日志
func genJSONLog(path string, size int64) int64 {
	os.MkdirAll(filepath.Dir(path), 0755)
	f, _ := os.Create(path)
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()

	rng := rand.New(rand.NewSource(7))
	ts := time.Date(2024, 6, 15, 8, 0, 0, 0, time.UTC)
	var n, total int64
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
		n++
	}
	return n
}
