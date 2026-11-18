import {createApp} from 'vue'
import App from './App.vue'
import './style.css';

// 前端错误可见层：WebView2 正式版静默吞掉 console（无 devtools），渲染/异步错误
// 若无出口则表现为「点了没反应」无从排查——右下角红条显示最近错误，点击复制全文
{
  let box: HTMLElement | null = null
  function showErr(msg: string) {
    if (!document.body) { document.addEventListener('DOMContentLoaded', () => showErr(msg), { once: true }); return }
    if (!box) {
      box = document.createElement('div')
      box.style.cssText = 'position:fixed;right:10px;bottom:10px;z-index:99999;max-width:440px;max-height:200px;overflow:auto;background:#7f1d1d;color:#fff;font:11px/1.5 Consolas,monospace;padding:8px 10px;border-radius:6px;box-shadow:0 4px 16px rgba(0,0,0,.45);white-space:pre-wrap;word-break:break-all;cursor:pointer'
      box.title = '点击复制错误信息'
      box.addEventListener('click', () => {
        navigator.clipboard?.writeText(box?.textContent ?? '').catch(() => {})
      })
      document.body.appendChild(box)
    }
    box.textContent = (box.textContent ? box.textContent + '\n———\n' : '')
      + new Date().toLocaleTimeString() + ' ' + msg
  }
  window.addEventListener('error', e =>
    showErr('ERROR: ' + e.message + ' @' + (e.filename || '') + ':' + (e.lineno || 0)))
  window.addEventListener('unhandledrejection', e => {
    const r: unknown = e.reason
    showErr('REJECT: ' + (r && typeof r === 'object' && 'stack' in r && typeof (r as Error).stack === 'string'
      ? String((r as Error).stack).slice(0, 500) : String(r)))
  })
}

// 主题启动预设（挂载前执行防闪色）：外观默认 auto 跟随系统；主题色默认冰蓝
{
  const pref = localStorage.getItem('lh-theme') || 'auto'
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.dataset.theme =
    pref === 'auto' ? (dark ? 'dark' : 'light') : pref
  // 未知取值不挂属性，style.css 基底即冰蓝
  const palette = localStorage.getItem('lh-palette')
  if (palette) document.documentElement.dataset.palette = palette
}

// Vue 渲染/异步错误不冒泡到 window.onerror（Vue 内部捕获后只走 errorHandler），
// 必须经 app 级 errorHandler 汇入错误可见层
{
  const app = createApp(App)
  app.config.errorHandler = (err, _inst, info) => {
    const e = err as Error
    window.dispatchEvent(new CustomEvent('lh-vue-error', {
      detail: 'VUE(' + info + '): ' + (e && e.stack ? String(e.stack).slice(0, 500) : String(err)),
    }))
  }
  window.addEventListener('lh-vue-error', ev => {
    const d = (ev as CustomEvent).detail
    // 复用下方错误层的展示（其监听器在本文件前段注册，直接再派生一个 error 事件）
    window.dispatchEvent(new ErrorEvent('error', { message: typeof d === 'string' ? d : String(d) }))
  })
  app.mount('#app')
}
