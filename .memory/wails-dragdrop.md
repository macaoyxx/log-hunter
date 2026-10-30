# Wails 拖放链路陷阱：必须走 OnFileDrop 注册

## 是什么 / 规则

Wails v2 的文件拖放，前端**必须调用 `OnFileDrop(callback, useDropTarget)` 完成注册**，直接 `EventsOn('wails:file-drop', ...)` 监听事件**永远收不到拖放**。

`OnFileDrop()`（wailsjs/runtime）内部做三件事，缺一不可：

| 事项 | 说明 |
|---|---|
| 挂 `window` 级 dragover/dragleave/drop 监听 | dragover 里 `e.preventDefault()`——**不 preventDefault 则 WebView2 显示禁止光标、无法放下** |
| drop 时调 `chrome.webview.postMessageWithAdditionalObjects('file:drop:x:y', files)` | Windows 上文件**路径解析的桥接**，Go 侧据此 EventsEmit `wails:file-drop` |
| 订阅 `wails:file-drop` 事件并回调 | 我们真正消费的入口 |

`useDropTarget` 参数：`true`（默认）= 只有点在带 `--wails-drop-target: drop` CSS 的元素上才回调；`false` = 窗口任意位置可放（本项目用 false）。`OnFileDropOff()` 成对清理（监听器 + 事件订阅）。

## 关键文件

- `frontend/src/App.vue`（onMounted 内 `OnFileDrop(...)` 注册 + 注释；onUnmounted 内 `OnFileDropOff()`）——注释已写明防退化说明，改代码前先读
- Wails v2.17 源码参考：`internal/frontend/runtime/desktop/draganddrop.js`（机制唯一事实源）

## 为什么这么做（踩坑）

LogHunter v0.0.1 起用 `EventsOn('wails:file-drop')` 直听，拖放链路从未接通——dragover 从未 preventDefault（禁止光标）、Windows 文件桥接从未发起。**持续两个版本没人发现**，因为「点击选择文件」一直可用，用户和开发者都以为拖拽正常；直到 v0.0.2 用户实测才暴露。定位靠读 Wails 源码（draganddrop.js L248-250：监听只在 OnFileDrop 注册时挂上）。

教训：① Wails 事件名存在 ≠ 链路通，注册型 API 不能用 EventsOn 绕过；② 「功能没验证过」和「功能正常」之间的差距，核心交互路径（拖放）必须有真实手测覆盖。
