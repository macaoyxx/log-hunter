import {createApp} from 'vue'
import App from './App.vue'
import './style.css';

// 昼夜主题启动预设（挂载前执行防闪色）：默认 auto 跟随系统
{
  const pref = localStorage.getItem('lh-theme') || 'auto'
  const dark = window.matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.dataset.theme =
    pref === 'auto' ? (dark ? 'dark' : 'light') : pref
}

createApp(App).mount('#app')
