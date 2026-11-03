import {createApp} from 'vue'
import App from './App.vue'
import './style.css';

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

createApp(App).mount('#app')
