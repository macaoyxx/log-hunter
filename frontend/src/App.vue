<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { EventsOn, EventsOff, OnFileDrop, OnFileDropOff } from '../wailsjs/runtime/runtime'
import {
  AddFiles, ClearAll, ListFiles, Query, Cancel, RemoveFile,
} from '../wailsjs/go/main/App'
import { logscan, main } from '../wailsjs/go/models'
import FileBar from './components/FileBar.vue'
import QueryBar from './components/QueryBar.vue'
import ResultTable from './components/ResultTable.vue'
import ContextPanel from './components/ContextPanel.vue'

// ---------- 全局状态 ----------
const files = ref<logscan.FileEntry[]>([])
const selected = ref<Set<number>>(new Set())
const loadingFiles = ref(false)
const fileProg = ref<{ name: string; done: number; total: number; fileNo: number } | null>(null)

const querying = ref(false)
const queryProg = ref<{ scanned: number; total: number; matched: number } | null>(null)
const stats = ref<logscan.QueryStats | null>(null)
const queryError = ref('')
const fileNotice = ref('') // 加载提示（zip 内跳过项等，非错误）

// 最近一次查询的文本条件（命中高亮用）
const hlKeywords = ref<string[]>([])
const hlRegex = ref('')

// 上下文侧滑面板
const ctxTarget = ref<{ fileId: number; offset: number } | null>(null)

const queryBarRef = ref<InstanceType<typeof QueryBar> | null>(null)

// ---------- 昼夜主题（默认 auto 跟随系统，选择持久化） ----------
type ThemePref = 'auto' | 'light' | 'dark'
const themePref = ref<ThemePref>((localStorage.getItem('lh-theme') as ThemePref) || 'auto')
const THEME_ICONS: Record<ThemePref, string> = { auto: '🌗', light: '☀️', dark: '🌙' }
const THEME_TITLES: Record<ThemePref, string> = {
  auto: '主题：自动（跟随系统）', light: '主题：日间', dark: '主题：夜巡',
}
const themeMedia = window.matchMedia('(prefers-color-scheme: dark)')

// 解析当前偏好为实际主题并挂到 <html data-theme>；切换瞬间挂过渡类做背光过渡
function applyTheme() {
  const t = themePref.value === 'auto' ? (themeMedia.matches ? 'dark' : 'light') : themePref.value
  const root = document.documentElement
  root.classList.add('theme-transitioning')
  window.setTimeout(() => root.classList.remove('theme-transitioning'), 300)
  root.dataset.theme = t
}
function cycleTheme() {
  const order: ThemePref[] = ['auto', 'light', 'dark']
  themePref.value = order[(order.indexOf(themePref.value) + 1) % order.length]
  localStorage.setItem('lh-theme', themePref.value)
  applyTheme()
}
function onSystemThemeChange() {
  if (themePref.value === 'auto') applyTheme()
}

const fileNameMap = computed(() => {
  const m = new Map<number, string>()
  for (const f of files.value) m.set(f.id, f.name)
  return m
})

const canQuery = computed(() => !loadingFiles.value && selected.value.size > 0)

// ---------- 事件 ----------
function onFileProgress(p: any) {
  fileProg.value = { name: p.name, done: p.done, total: p.total, fileNo: p.fileNo ?? 0 }
}
function onQueryProgress(p: any) {
  queryProg.value = { scanned: p.scanned, total: p.total, matched: p.matched ?? 0 }
}

onMounted(() => {
  EventsOn('files:progress', onFileProgress)
  EventsOn('query:progress', onQueryProgress)
  // 拖放必须经 OnFileDrop 正规注册：其内部才会挂 dragover/drop 监听（preventDefault
  // 允许放下）并接通 Windows 的文件路径桥接；此前直接 EventsOn('wails:file-drop')
  // 事件链从未接通，拖拽从未生效。useDropTarget=false：窗口任意位置都可放
  OnFileDrop((_x, _y, paths) => {
    if (paths && paths.length > 0) addFiles(paths)
  }, false)
  themeMedia.addEventListener('change', onSystemThemeChange)
  refreshFiles()
})
onUnmounted(() => {
  EventsOff('files:progress')
  EventsOff('query:progress')
  OnFileDropOff()
  themeMedia.removeEventListener('change', onSystemThemeChange)
})

// ---------- 文件管理 ----------
async function refreshFiles() {
  files.value = await ListFiles()
  const ids = new Set(files.value.map(f => f.id))
  selected.value = new Set([...selected.value].filter(id => ids.has(id)))
}

async function addFiles(paths: string[]) {
  const wasEmpty = files.value.length === 0
  loadingFiles.value = true
  fileProg.value = null
  try {
    const res: main.AddFilesResult = await AddFiles(paths)
    await refreshFiles()
    for (const f of res.allFiles ?? []) selected.value.add(f.id)
    selected.value = new Set(selected.value)
    if (res.errors?.length) {
      queryError.value = '部分文件加载失败：\n' + res.errors.join('\n')
    }
    fileNotice.value = res.notices?.length ? res.notices.join('\n') : ''
  } catch (e) {
    queryError.value = '加载失败：' + e
  } finally {
    loadingFiles.value = false
    fileProg.value = null
  }
  // 首次加载：自动浏览全部，拖进来即见日志；追加文件不自动（不覆盖已输入条件的结果）
  if (wasEmpty && files.value.length > 0) doQuery(new main.QuerySpecJSON())
}

async function removeFile(id: number) {
  const hadResults = stats.value !== null
  await RemoveFile(id)
  await refreshFiles()
  if (ctxTarget.value && !fileNameMap.value.has(ctxTarget.value.fileId)) ctxTarget.value = null
  // 后端移除文件即作废全部查询结果。还有勾选文件且之前有结果：
  // 按最近一次条件自动重查剩余文件（fileIds 按当前勾选重填）；
  // 全删完（或无勾选/原本无结果）则清空结果表，相当于清空重来
  if (hadResults && files.value.length > 0 && selected.value.size > 0) {
    queryBarRef.value?.rerunLast()
  } else {
    stats.value = null
  }
}

async function clearAll() {
  await ClearAll()
  await refreshFiles()
  selected.value = new Set()
  stats.value = null
  queryError.value = ''
  fileNotice.value = ''
  ctxTarget.value = null
  hlKeywords.value = []
  hlRegex.value = ''
  queryBarRef.value?.reset() // 查询条件一并清空（历史保留）
}

function toggleSelect(id: number) {
  const s = new Set(selected.value)
  if (s.has(id)) s.delete(id); else s.add(id)
  selected.value = s
}

// ---------- 查询 ----------
function doQuery(spec: main.QuerySpecJSON) {
  if (selected.value.size === 0) { queryError.value = '请先拖入并勾选要查询的文件'; return }
  spec.fileIds = [...selected.value]
  runQuery(spec)
}

async function runQuery(spec: main.QuerySpecJSON) {
  querying.value = true
  queryError.value = ''
  queryProg.value = null
  stats.value = null
  ctxTarget.value = null
  hlKeywords.value = [...(spec.keywords ?? [])]
  hlRegex.value = spec.regex ?? ''
  try {
    stats.value = await Query(spec)
  } catch (e) {
    queryError.value = String(e)
  } finally {
    querying.value = false
    queryProg.value = null
  }
}

// 结果表级别统计点击 → 加该级别筛选并立即重查
function levelClick(lv: number) {
  queryBarRef.value?.addLevelAndSearch(lv)
}

function openContext(fileId: number, offset: number) {
  if (!fileNameMap.value.has(fileId)) return // 数据未就绪不打开，避免“文件不存在”
  ctxTarget.value = { fileId, offset }
}
</script>

<template>
  <div class="app">
    <header class="brand">
      <span class="logo">▮</span>
      <span class="name">LogHunter</span>
      <span class="tag">本地日志排查 · 拖进来就查</span>
      <button class="theme-btn" :title="THEME_TITLES[themePref]" @click="cycleTheme">{{ THEME_ICONS[themePref] }}</button>
    </header>

    <FileBar
      :files="files" :selected="selected" :loading="loadingFiles" :progress="fileProg"
      @add="addFiles" @remove="removeFile" @clear="clearAll" @toggle="toggleSelect"
    />
    <div v-if="fileNotice" class="notice-bar">
      <pre class="notice-text">{{ fileNotice }}</pre>
      <button class="notice-x" title="关闭提示" @click="fileNotice = ''">✕</button>
    </div>
    <QueryBar
      ref="queryBarRef"
      :disabled="!canQuery" :querying="querying" :progress="queryProg" :error="queryError"
      :files="files" :selected="selected"
      @search="doQuery" @cancel="Cancel"
    />
    <ResultTable
      :stats="stats" :querying="querying" :progress="queryProg"
      :file-count="files.length" :file-names="fileNameMap"
      :highlight-keywords="hlKeywords" :highlight-regex="hlRegex"
      @open-context="openContext" @level-click="levelClick"
    />
    <ContextPanel v-if="ctxTarget" :target="ctxTarget" :file-name="fileNameMap.get(ctxTarget.fileId)!"
      :highlight-keywords="hlKeywords" :highlight-regex="hlRegex"
      @close="ctxTarget = null"
      @jump="(off: number) => { if (ctxTarget) ctxTarget = { fileId: ctxTarget.fileId, offset: off } }" />
  </div>
</template>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100%;
  gap: 10px;
  padding: 12px 14px;
}
.brand {
  flex: none;
  display: flex;
  align-items: baseline;
  gap: 8px;
  user-select: none;
}
.logo { color: var(--amber); font-size: 15px; }
.name {
  font-family: var(--mono);
  font-weight: 700;
  font-size: 16px;
  letter-spacing: .04em;
}
.tag { color: var(--text-faint); font-size: 12px; }
.theme-btn {
  margin-left: auto;
  align-self: center;
  background: transparent;
  border: none;
  font-size: 15px;
  line-height: 1;
  padding: 4px 6px;
}
.theme-btn:hover { background: var(--bg-hover); border: none; }
.notice-bar {
  flex: none;
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 7px 12px;
  background: var(--amber-soft);
  border: 1px solid var(--amber-line);
  border-radius: var(--radius);
}
.notice-text {
  flex: 1;
  min-width: 0;
  font-family: var(--mono);
  font-size: 11.5px;
  line-height: 1.55;
  color: var(--text-dim);
  white-space: pre-wrap;
  word-break: break-all;
}
.notice-x {
  flex: none;
  background: none;
  border: none;
  color: var(--text-faint);
  font-size: 11px;
  cursor: pointer;
  padding: 2px;
}
.notice-x:hover { color: var(--text); }
</style>
