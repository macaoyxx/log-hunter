package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"log-hunter/internal/appconf"
	"log-hunter/internal/logscan"
	"log-hunter/internal/update"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 应用结构：Wails 绑定层，包装 logscan.Session 与配置存储
type App struct {
	ctx       context.Context
	session   *logscan.Session
	conf      *appconf.Store
	procStart time.Time    // 进程启动时刻（埋点起点，main 传入）
	startupAt time.Time    // Wails OnStartup 时刻（埋点中点）
	winShowing atomic.Bool // 显示动作防重（只允许一次 WindowShow 发起）
	winShown   atomic.Bool // 窗口已显示且稳定（显示后留 300ms 消化焦点消息再置位；
	                       // 对话框弹出前必须等待它——窗口显示在途撞上文件对话框
	                       // 模态会让 WebView2 Focus 报参数错直接 Fatal 闪退）
}

// NewApp 创建应用实例；配置存储打开失败降级为内存态（不阻断启动）
func NewApp(procStart time.Time) *App {
	conf, err := appconf.Open()
	if err != nil {
		conf = nil
	}
	return &App{session: logscan.NewSession(), conf: conf, procStart: procStart}
}

// startup Wails 生命周期：保存上下文；清理上次自动更新残留的 .old；
// 3 秒兜底显示窗口——正常显示点是前端就绪后的 ReportStartupReady，
// 前端异常时兜底保证不至于永远无窗（WindowShow 重复调用无害）
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.startupAt = time.Now()
	go func() {
		if exe, err := os.Executable(); err == nil {
			update.CleanupOld(exe)
		}
		// 清扫崩溃/强杀残留的解压临时根（锁探针 + 年龄护栏防误删活实例，见 unzip.go）
		logscan.SweepStaleTemp()
	}()
	go func() {
		time.Sleep(3 * time.Second)
		// 仅当前端从未发起显示时兜底——无条件显示会撞上用户刚弹出的系统模态
		// 对话框（启动秒点「从电脑选择」的 3 秒窗口），WebView2 Focus 致命闪退
		if !a.winShowing.Load() {
			a.showWindowOnce()
		}
	}()
}

// showWindowOnce 发起一次窗口显示，并在显示后留 300ms 焦点消息消化期再置
// 「稳定」标记——对话框弹出前等待该标记，杜绝显示在途与模态对话框重叠
func (a *App) showWindowOnce() {
	if !a.winShowing.CompareAndSwap(false, true) {
		return
	}
	runtime.WindowShow(a.ctx)
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.winShown.Store(true)
	}()
}

// waitWindowShown 等待窗口显示稳定（上限 4s：显示从未发起时由 3s 兜底补位后放行）
func (a *App) waitWindowShown() {
	deadline := time.Now().Add(4 * time.Second)
	for !a.winShown.Load() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
}

// ReportStartupReady 前端界面就绪上报：显示窗口（隐藏启动的正式显示点），并把
// 启动耗时埋点追加写入 data/startup.log（进程→OnStartup→界面就绪两段区间；
// 写失败静默——诊断数据不该影响启动）
func (a *App) ReportStartupReady() {
	a.showWindowOnce()
	readyAt := time.Now()
	procToStartup := a.startupAt.Sub(a.procStart).Milliseconds()
	startupToReady := readyAt.Sub(a.startupAt).Milliseconds()
	line := fmt.Sprintf("%s 进程→启动 %dms · 界面就绪 %dms · 合计 %dms\r\n",
		readyAt.Format("2006-01-02 15:04:05"), procToStartup, startupToReady,
		procToStartup+startupToReady)
	if cfgPath, err := appconf.DefaultPath(); err == nil {
		_ = os.MkdirAll(filepath.Dir(cfgPath), 0755)
		if f, err := os.OpenFile(filepath.Join(filepath.Dir(cfgPath), "startup.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			_, _ = f.WriteString(line)
			_ = f.Close()
		}
	}
}

// LastStartupCost 读最近一次启动耗时埋点（关于弹窗展示；无记录返回空串）
func (a *App) LastStartupCost() string {
	cfgPath, err := appconf.DefaultPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "startup.log"))
	if err != nil || len(b) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n")
	return lines[len(lines)-1]
}

// shutdown 退出时释放全部资源
func (a *App) shutdown(ctx context.Context) { a.session.ClearAll() }

// AddFilesResult 批量添加文件的结果
type AddFilesResult struct {
	Files    []*logscan.FileEntry `json:"files"`
	Errors   []string             `json:"errors"`
	Notices  []string             `json:"notices"`  // 非错误提示（zip 内二进制/加密等跳过项）
	AllFiles []*logscan.FileEntry `json:"allFiles"` // 当前会话全部文件
}

// AddFiles 拖拽添加文件：探测格式并建索引（.zip 自动解压后加载其中文本文件），
// 进度经 "files:progress" 事件推送
func (a *App) AddFiles(paths []string) (*AddFilesResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("没有文件")
	}
	files, errs, notices := a.session.AddFiles(a.ctx, paths, func(p logscan.FileProgress) {
		runtime.EventsEmit(a.ctx, "files:progress", p)
	})
	return &AddFilesResult{Files: files, Errors: errs, Notices: notices, AllFiles: a.session.Files()}, nil
}

// RemoveFile 移除单个文件
func (a *App) RemoveFile(id int64) { a.session.RemoveFile(id) }

// ClearAll 一键清空全部文件与结果
func (a *App) ClearAll() { a.session.ClearAll() }

// ListFiles 当前文件列表
func (a *App) ListFiles() []*logscan.FileEntry { return a.session.Files() }

// QuerySpecJSON 前端查询条件（时间为 datetime-local 字符串，空串不限）
type QuerySpecJSON struct {
	FileIDs    []int64  `json:"fileIds"`
	Keywords   []string `json:"keywords"`
	Excludes   []string `json:"excludes"`
	Regex      string   `json:"regex"`
	KeywordAnd bool     `json:"keywordAnd"`
	TSStart    string   `json:"tsStart"` // "2006-01-02T15:04[:05]"
	TSEnd      string   `json:"tsEnd"`
	Levels     []int8   `json:"levels"`
}

// parseLocalTime 解析 datetime-local 字符串为 unixNano；空串返回 0
func parseLocalTime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UnixNano(), nil
		}
	}
	return 0, fmt.Errorf("无法解析时间: %q", s)
}

// Query 执行查询（条件全空 = 浏览全部行），进度经 "query:progress" 事件推送
func (a *App) Query(spec *QuerySpecJSON) (*logscan.QueryStats, error) {
	if spec == nil || len(spec.FileIDs) == 0 {
		return nil, fmt.Errorf("请先选择要查询的文件")
	}
	start, err := parseLocalTime(spec.TSStart)
	if err != nil {
		return nil, err
	}
	end, err := parseLocalTime(spec.TSEnd)
	if err != nil {
		return nil, err
	}
	if start > 0 && end > 0 && end < start {
		return nil, fmt.Errorf("结束时间不能早于开始时间")
	}
	s := &logscan.QuerySpec{
		FileIDs: spec.FileIDs, Keywords: spec.Keywords, Excludes: spec.Excludes,
		Regex: spec.Regex, KeywordAnd: spec.KeywordAnd,
		TSStart: start, TSEnd: end, Levels: spec.Levels,
	}
	return a.session.RunQuery(a.ctx, s, func(p logscan.QueryProgress) {
		runtime.EventsEmit(a.ctx, "query:progress", p)
	})
}

// GetResults 分页取结果窗口（虚拟滚动用）
func (a *App) GetResults(offset, limit int) []logscan.ResultRow {
	return a.session.GetResults(offset, limit)
}

// SetMerged 切换结果视图模式：false=平铺（文件加入序+行号），true=按时间归并；
// 有结果时后端即时重排，不重新扫描文件
func (a *App) SetMerged(merged bool) {
	a.session.SetMerged(merged)
}

// GetContext 取指定行的上下文
func (a *App) GetContext(fileID, offset int64, before, after int64) ([]logscan.ContextRow, error) {
	return a.session.GetContext(fileID, offset, before, after)
}

// GetLineText 读指定行完整原文（不截断）——结果行右键「复制整行」用
func (a *App) GetLineText(fileID, offset int64) (string, error) {
	return a.session.GetLineText(fileID, offset)
}

// CopyText 统一后端写剪贴板——前端 navigator.clipboard / Wails 前端 ClipboardSetText
// 对大文本（超长 SQL 行）实测会截断，复制动作全部收进 Go 侧直写系统剪贴板
func (a *App) CopyText(text string) error {
	return runtime.ClipboardSetText(a.ctx, text)
}

// CopyLineText 读指定行完整原文并直接写入剪贴板（右键复制整行的一步到位入口）
func (a *App) CopyLineText(fileID, offset int64) error {
	text, err := a.session.GetLineText(fileID, offset)
	if err != nil {
		return err
	}
	return runtime.ClipboardSetText(a.ctx, text)
}

// ValidateRegex 校验正则；返回空串表示合法，否则为错误说明
func (a *App) ValidateRegex(s string) string {
	if s == "" {
		return ""
	}
	if _, err := logscan.CompileRegexForValidation(s); err != nil {
		return err.Error()
	}
	return ""
}

// ExportResults 导出当前结果（format: txt | csv），返回导出条数
func (a *App) ExportResults(path, format string) (int, error) {
	if path == "" {
		return 0, fmt.Errorf("未选择保存位置")
	}
	return a.session.ExportResults(path, format)
}

// SaveFileDialog 导出用：弹出系统保存对话框，返回所选路径（取消返回空串）
func (a *App) SaveFileDialog(defaultName string, ext string) string {
	a.waitWindowShown() // 窗口显示在途弹对话框会撞模态焦点消息导致闪退
	p, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Filters: []runtime.FileFilter{
			{DisplayName: ext + " 文件", Pattern: "*." + ext},
		},
	})
	if err != nil {
		return ""
	}
	return p
}

// PickFiles 弹系统多选文件对话框，返回所选路径（取消返回空数组）
func (a *App) PickFiles() []string {
	a.waitWindowShown() // 窗口显示在途弹对话框会撞模态焦点消息导致闪退
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择日志文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "日志文件", Pattern: "*.log;*.txt;*.out;*.log.*;*.trace;*.zip"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return []string{}
	}
	return paths
}

// Cancel 取消进行中的加载或查询
func (a *App) Cancel() { a.session.CancelAll() }

// ---------- 版本 / 检查更新 / 配置持久化 ----------

// GetVersion 当前版本（dev 构建返回 "dev"，发版产物为注入的 x.y.z）
func (a *App) GetVersion() string { return version }

// UpdateInfo 检查更新结果：Err 非空表示检查失败（网络等），此时其余字段无意义
type UpdateInfo struct {
	Latest    string `json:"latest"`
	HasUpdate bool   `json:"hasUpdate"`
	Err       string `json:"err"`
	// 自动更新资产（Release 未匹配到时为空，前端回落手动下载链接）
	AssetURL  string `json:"assetUrl"`
	AssetName string `json:"assetName"`
	AssetSize int64  `json:"assetSize"`
	Sha256URL string `json:"sha256Url"`
}

const releasesAPI = "https://api.github.com/repos/macaoyxx/log-hunter/releases/latest"

// releaseInfo CheckUpdate/StartUpdate 共用的 Release 查询结果
type releaseInfo struct {
	TagName string
	Assets  []releaseAsset
}

type releaseAsset struct {
	Name string
	URL  string
	Size int64
}

// fetchLatestRelease 查询 GitHub 最新 Release（5s 超时，UA 为 GitHub 硬性要求）
func fetchLatestRelease() (releaseInfo, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, releasesAPI, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("User-Agent", "log-hunter")
	resp, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return releaseInfo{}, fmt.Errorf("响应解析失败")
	}
	if strings.TrimPrefix(body.TagName, "v") == "" {
		return releaseInfo{}, fmt.Errorf("未找到发布版本")
	}
	out := releaseInfo{TagName: strings.TrimPrefix(body.TagName, "v")}
	for _, a := range body.Assets {
		out.Assets = append(out.Assets, releaseAsset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return out, nil
}

// pickUpdateAssets 从 Release 资产中匹配本平台更新包与校验文件（未匹配返回零值）
func pickUpdateAssets(assets []releaseAsset) (zip, sum releaseAsset) {
	for _, a := range assets {
		switch {
		case strings.HasPrefix(a.Name, "log-hunter-v") && strings.HasSuffix(a.Name, "-windows-amd64.zip"):
			zip = a
		case strings.HasSuffix(a.Name, ".sha256"):
			sum = a
		}
	}
	return
}

// CheckUpdate 对比 GitHub 最新 Release 与当前版本，并解析可用的自动更新资产
func (a *App) CheckUpdate() UpdateInfo {
	rel, err := fetchLatestRelease()
	if err != nil {
		return UpdateInfo{Err: err.Error()}
	}
	zip, sum := pickUpdateAssets(rel.Assets)
	info := UpdateInfo{Latest: rel.TagName,
		AssetURL: zip.URL, AssetName: zip.Name, AssetSize: zip.Size, Sha256URL: sum.URL}
	// dev 构建（迭代号-dev 后缀）无法可靠比较版本，只报最新版不判更新
	if strings.HasSuffix(version, "-dev") {
		return UpdateInfo{Latest: rel.TagName}
	}
	info.HasUpdate = cmpVersion(rel.TagName, version) > 0
	return info
}

// StartUpdate 应用内自动更新：查最新 Release → 下载（进度经 update:progress 事件）→
// sha256 校验（资产存在时强制；v0.0.5 前的历史 Release 无 checksum，过渡跳过）→
// 解压 → rename 自替换。成功后由前端调 Restart 进入新版本；失败时旧版本不受影响
func (a *App) StartUpdate() error {
	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("检查更新失败: %w", err)
	}
	if strings.HasSuffix(version, "-dev") {
		return fmt.Errorf("开发版不支持自动更新，请手动下载")
	}
	if cmpVersion(rel.TagName, version) <= 0 {
		return fmt.Errorf("已是最新版本")
	}
	zip, sum := pickUpdateAssets(rel.Assets)
	if zip.URL == "" {
		return fmt.Errorf("该版本没有 Windows 更新包，请手动下载")
	}
	dir, err := os.MkdirTemp("", "log-hunter-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) // 替换成功后新 exe 已落位原名，临时物全清
	zipPath := filepath.Join(dir, zip.Name)
	if err := update.Download(a.ctx, zip.URL, zipPath, func(p update.DownloadProgress) {
		runtime.EventsEmit(a.ctx, "update:progress", p)
	}); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	if sum.URL != "" {
		sumPath := filepath.Join(dir, sum.Name)
		if err := update.Download(a.ctx, sum.URL, sumPath, nil); err != nil {
			return fmt.Errorf("下载校验文件失败: %w", err)
		}
		b, err := os.ReadFile(sumPath)
		if err != nil {
			return err
		}
		fields := strings.Fields(string(b)) // sha256sum 格式："哈希  文件名"
		if len(fields) == 0 {
			return fmt.Errorf("校验文件格式异常")
		}
		if err := update.VerifySHA256(zipPath, fields[0]); err != nil {
			return fmt.Errorf("完整性校验失败（请手动下载重试）: %w", err)
		}
	}
	newExe := filepath.Join(dir, "log-hunter.exe")
	if err := update.ExtractExe(zipPath, newExe); err != nil {
		return err
	}
	cur, err := os.Executable()
	if err != nil {
		return err
	}
	return update.Apply(newExe, cur)
}

// Restart 启动新版本进程并退出当前应用（更新替换完成后由前端调用）
func (a *App) Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := exec.Command(exe).Start(); err != nil {
		return fmt.Errorf("启动新版本失败: %w", err)
	}
	runtime.Quit(a.ctx)
	return nil
}

// cmpVersion 语义化版本比较（a.b.c 数字逐段），返回 >0 表示 a 更新
func cmpVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// store 配置存储（nil 时各方法静默降级为无持久化，不阻断功能）
func (a *App) store() *appconf.Store { return a.conf }

// GetConfig 读配置（主题 + 查询历史）
func (a *App) GetConfig() appconf.Conf {
	if s := a.store(); s != nil {
		return s.Get()
	}
	return appconf.Conf{}
}

// SetTheme 持久化主题偏好
func (a *App) SetTheme(theme string) {
	if s := a.store(); s != nil {
		_ = s.SetTheme(theme)
	}
}

// SaveHistory 保存一条查询历史（同标签去重置顶，上限 20）
func (a *App) SaveHistory(label, specJSON string) {
	if s := a.store(); s != nil {
		_ = s.PushHistory(label, specJSON)
	}
}
