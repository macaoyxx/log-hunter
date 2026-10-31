package main

import (
	"embed"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// version 版本号：默认值为「当前迭代号-dev」（每轮迭代开始时 bump）；
// 发版由 release.ps1 经 -ldflags "-X main.version=x.y.z" 覆盖为正式号
var version = "0.0.5-dev"

// procStart 进程启动时刻（启动耗时埋点起点）
var procStart = time.Now()



func main() {
	app := NewApp(procStart)

	err := wails.Run(&options.App{
		Title:     "LogHunter 日志查询",
		Width:     1320,
		Height:    860,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 24, G: 26, B: 32, A: 1},
		// 隐藏启动：前端就绪后 ReportStartupReady 才显示窗口，消除 WebView2 加载期
		// 的空窗体感（Go 侧另有 3 秒兜底显示，防前端异常时永远无窗）
		StartHidden: true,
		OnStartup:   app.startup,
		OnShutdown:  app.shutdown,
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: false,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.SystemDefault, // 窗框跟随系统，与页面昼夜主题对齐
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
