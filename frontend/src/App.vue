<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { EventsOn, EventsOff, OnFileDrop, OnFileDropOff, BrowserOpenURL } from '../wailsjs/runtime/runtime'
import {
  AddFiles, ClearAll, ListFiles, Query, Cancel, RemoveFile,
  GetVersion, CheckUpdate, StartUpdate, Restart, GetConfig, SetTheme, SetMerged,
  ReportStartupReady, LastStartupCost,
} from '../wailsjs/go/main/App'
import { logscan, main } from '../wailsjs/go/models'
import FileBar from './components/FileBar.vue'
import appicon from './assets/appicon.png'
import QueryBar from './components/QueryBar.vue'
import ResultTable from './components/ResultTable.vue'
import ContextPanel from './components/ContextPanel.vue'

// ---------- 全局状态 ----------
const files = ref<logscan.FileEntry[]>([])
const selected = ref<Set<number>>(new Set())
const loadingFiles = ref(false)
const fileProg = ref<{ name: string; done: number; total: number; fileNo: number } | null>(null)
const fileTotal = ref(0) // 本次拖入文件总数（进度显示「第 N/M 个」用）

const querying = ref(false)
const queryProg = ref<{ scanned: number; total: number; matched: number } | null>(null)
const stats = ref<logscan.QueryStats | null>(null)
const queryError = ref('')
const fileNotice = ref('') // 加载提示（zip 内跳过项等，非错误）

// 结果视图模式：false=平铺（默认）；true=按时间归并（后端重排，不重扫文件）
const mergedMode = ref(false)
// 排序切换进行中（全量重排几百 ms，期间按钮禁用并提示，避免「点了没反应」感）
const merging = ref(false)
// 勾选中的无时间格式文件名（合并视图置尾提示用）
const noTimeNames = computed(() =>
  files.value.filter(f => !f.hasTime && selected.value.has(f.id)).map(f => f.name))

// 最近一次查询的文本条件（命中高亮用）
const hlKeywords = ref<string[]>([])
const hlRegex = ref('')

// 上下文侧滑面板
const ctxTarget = ref<{ fileId: number; offset: number } | null>(null)

const queryBarRef = ref<InstanceType<typeof QueryBar> | null>(null)

// ---------- 昼夜主题（默认 auto 跟随系统；config.json 为权威持久化，localStorage 作第一道防闪与迁移源） ----------
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
  SetTheme(themePref.value) // 文件级持久化（config.json）
  applyTheme()
}
function onSystemThemeChange() {
  if (themePref.value === 'auto') applyTheme()
}

// ---------- 头部菜单：检查更新 / 关于 ----------
const appVersion = ref('')
const menuOpen = ref(false)
const menuRoot = ref<HTMLElement | null>(null)
const aboutOpen = ref(false)
const aboutStartup = ref('') // 最近一次启动耗时埋点（关于弹窗展示）
// 检查更新状态：null 未检查 | 'checking' | 结果（含自动更新资产）
const updateInfo = ref<main.UpdateInfo | null | 'checking'>(null)
// 自动更新流程：null 关闭 | 'confirm' 确认 | 'downloading' 下载中 | 'ready' 就绪 | 'error' 失败
const updFlow = ref<null | 'confirm' | 'downloading' | 'ready' | 'error'>(null)
const updError = ref('')
const updProgress = ref<{ received: number; total: number } | null>(null)

function fmtMB(n: number): string {
  return n > 0 ? (n / 1024 / 1024).toFixed(1) + ' MB' : ''
}

function doCheckUpdate() {
  if (updateInfo.value === 'checking') return // 检查中防重复触发（后发先至会覆盖结果）
  updateInfo.value = 'checking'
  CheckUpdate().then(u => { updateInfo.value = u }).catch(e => {
    updateInfo.value = { latest: '', hasUpdate: false, err: String(e) } as main.UpdateInfo
  })
}

// 自动更新三段式：确认 → 下载替换（进度）→ 就绪后由用户选立即/稍后重启
function beginUpdate() { updFlow.value = 'confirm'; menuOpen.value = false }
function confirmUpdate() {
  updFlow.value = 'downloading'
  updProgress.value = null
  StartUpdate().then(() => { updFlow.value = 'ready' }).catch(e => {
    updError.value = String(e)
    updFlow.value = 'error'
  })
}
function restartNow() {
  Restart().catch(() => { /* 启动失败留在就绪态，用户可手动重启 */ })
}
function closeUpdFlow() {
  // 下载中不可关（关闭会丢失进度反馈，更新仍在后台进行）；就绪态保留稍后语义
  if (updFlow.value === 'downloading') return
  updFlow.value = null
  updError.value = ''
}

function openRepo() {
  BrowserOpenURL('https://github.com/macaoyxx/log-hunter')
}
function openReleases() {
  BrowserOpenURL('https://github.com/macaoyxx/log-hunter/releases/latest')
}

// 浮层展示用的检查结果快照（排除 checking 态）
const updInfo = computed(() => {
  const u = updateInfo.value
  return u && u !== 'checking' ? u : null
})
const updPct = computed(() => {
  const p = updProgress.value
  if (!p || p.total <= 0) return 0
  return Math.min(100, Math.round((p.received / p.total) * 100))
})

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
function onUpdateProgress(p: any) {
  updProgress.value = { received: p.received ?? 0, total: p.total ?? 0 }
}

onMounted(() => {
  EventsOn('files:progress', onFileProgress)
  EventsOn('query:progress', onQueryProgress)
  EventsOn('update:progress', onUpdateProgress)
  // 拖放必须经 OnFileDrop 正规注册：其内部才会挂 dragover/drop 监听（preventDefault
  // 允许放下）并接通 Windows 的文件路径桥接；此前直接 EventsOn('wails:file-drop')
  // 事件链从未接通，拖拽从未生效。useDropTarget=false：窗口任意位置都可放
  OnFileDrop((_x, _y, paths) => {
    if (paths && paths.length > 0) addFiles(paths)
  }, false)
  themeMedia.addEventListener('change', onSystemThemeChange)
  // 菜单浮层：点外部关闭
  document.addEventListener('click', onMenuOutside)
  refreshFiles()
  // 版本号 + 主题持久化校正（config.json 权威；localStorage 先行防闪）
  GetVersion().then(v => { appVersion.value = v }).catch(() => {})
  GetConfig().then(cfg => {
    if (cfg.theme === 'auto' || cfg.theme === 'light' || cfg.theme === 'dark') {
      if (cfg.theme !== themePref.value) {
        themePref.value = cfg.theme
        localStorage.setItem('lh-theme', cfg.theme)
        applyTheme()
      }
    } else {
      SetTheme(themePref.value) // 首次：localStorage 旧值迁移入库
    }
  }).catch(() => { /* 配置读取失败维持 localStorage 现值，不阻断 */ })
  // 启动收尾：界面就绪上报（后端显示窗口 + 写启动耗时埋点）、关于弹窗启动耗时
  ReportStartupReady().catch(() => {})
  LastStartupCost().then(s => { aboutStartup.value = s }).catch(() => {})
})
onUnmounted(() => {
  EventsOff('files:progress')
  EventsOff('query:progress')
  EventsOff('update:progress')
  OnFileDropOff()
  themeMedia.removeEventListener('change', onSystemThemeChange)
  document.removeEventListener('click', onMenuOutside)
})
function onMenuOutside(e: MouseEvent) {
  if (menuOpen.value && menuRoot.value && !menuRoot.value.contains(e.target as Node)) {
    menuOpen.value = false
    updateInfo.value = null
  }
}
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
  fileTotal.value = paths.length
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
    fileTotal.value = 0
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

// 查询请求代次：旧请求迟到的 catch/finally 不动新查询状态（停止后立刻重查时，
// 旧 finally 会误清 querying 导致按钮提前恢复、可并发触发下一次）
let querySeq = 0

async function runQuery(spec: main.QuerySpecJSON) {
  const seq = ++querySeq
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
    // 用户主动「停止」触发的取消（后端 context.Canceled）不是错误——静默归位，
    // 不打扰；查询中按钮已是「停止」，快速连点极易触发，属合法操作
    if (seq === querySeq && !String(e).includes('context canceled')) {
      queryError.value = String(e)
    }
  } finally {
    if (seq === querySeq) {
      querying.value = false
      queryProg.value = null
    }
  }
}

// 结果表级别统计点击 → 加该级别筛选并立即重查
function levelClick(lv: number) {
  queryBarRef.value?.addLevelAndSearch(lv)
}

// 切换结果视图（平铺/时间合并）：先等后端重排完成再更新状态，
// 触发结果表清缓存重拉——避免 GetResults 赶在重排前拿到旧序数据；
// 全量结果重排需几百 ms，merging 态让按钮即时给反馈
async function setMerged(m: boolean) {
  if (m === mergedMode.value || merging.value) return
  merging.value = true
  try {
    await SetMerged(m)
    mergedMode.value = m
  } catch (e) {
    queryError.value = '切换视图模式失败：' + e
  } finally {
    merging.value = false
  }
}

function openContext(fileId: number, offset: number) {
  if (!fileNameMap.value.has(fileId)) return // 数据未就绪不打开，避免“文件不存在”
  ctxTarget.value = { fileId, offset }
}
</script>

<template>
  <div class="app">
    <header class="brand">
      <span class="name">LogHunter</span>
      <span class="tag">本地日志排查 · 拖进来就查</span>
      <div ref="menuRoot" class="brand-ctrl">
        <button class="theme-btn" :title="THEME_TITLES[themePref]" @click="cycleTheme">{{ THEME_ICONS[themePref] }}</button>
        <button class="theme-btn" title="菜单" @click.stop="menuOpen = !menuOpen; updateInfo = null">≡</button>
        <div v-if="menuOpen" class="appmenu">
          <button class="mi" @click="doCheckUpdate">⟳ 检查更新</button>
          <button class="mi" @click="menuOpen = false; aboutOpen = true">ⓘ 关于</button>
          <div v-if="updateInfo === 'checking'" class="upd-msg">正在检查…</div>
          <template v-else-if="updInfo">
            <template v-if="updInfo.err">
              <div class="upd-msg">检查失败：{{ updInfo.err }}</div>
            </template>
            <template v-else-if="updInfo.hasUpdate">
              <button v-if="updInfo.assetUrl" class="mi upd-cta" @click="beginUpdate">
                ⬇ 自动更新到 v{{ updInfo.latest }}
              </button>
              <div class="upd-msg link" title="打开发布页手动下载" @click="openReleases">
                发现新版本 v{{ updInfo.latest }}
              </div>
            </template>
            <div v-else-if="updInfo.latest" class="upd-msg">
              {{ appVersion.endsWith('-dev') ? `当前开发版，最新发布 v${updInfo.latest}` : `已是最新 v${updInfo.latest}` }}
            </div>
          </template>
        </div>
      </div>
    </header>

    <!-- 关于弹窗 -->
    <div v-if="aboutOpen" class="about-mask" @click.self="aboutOpen = false">
      <div class="about">
        <img class="about-icon" :src="appicon" alt="LogHunter" />
        <div class="about-name">LogHunter</div>
        <div class="about-ver">v{{ appVersion }}</div>
        <div class="about-desc">本地大日志快速排查 · 拖进来就查</div>
        <div class="about-link" title="打开仓库页" @click="openRepo">github.com/macaoyxx/log-hunter</div>
        <div v-if="aboutStartup" class="about-startup" title="启动耗时埋点（data/startup.log）">{{ aboutStartup }}</div>
        <button class="ghost" @click="aboutOpen = false">关闭</button>
      </div>
    </div>

    <!-- 自动更新浮层：确认 / 下载中 / 就绪 / 失败 -->
    <div v-if="updFlow" class="upd-mask" @click.self="closeUpdFlow">
      <div class="upd-dlg">
        <template v-if="updFlow === 'confirm'">
          <div class="upd-title">下载并更新到 v{{ updInfo?.latest }}？</div>
          <div class="upd-desc">
            约 {{ fmtMB(updInfo?.assetSize ?? 0) }} · 下载并校验后自动替换本程序，完成后可立即或稍后重启
          </div>
          <div class="upd-btns">
            <button class="ghost" @click="closeUpdFlow">取消</button>
            <button class="primary" @click="confirmUpdate">下载并更新</button>
          </div>
        </template>
        <template v-else-if="updFlow === 'downloading'">
          <div class="upd-title">正在下载 v{{ updInfo?.latest }}…</div>
          <div class="dl-bar"><div class="dl-fill" :style="{ width: updPct + '%' }"></div></div>
          <div class="upd-desc">
            {{ fmtMB(updProgress?.received ?? 0) }}
            <template v-if="updProgress && updProgress.total > 0">/ {{ fmtMB(updProgress.total) }} · {{ updPct }}%</template>
          </div>
        </template>
        <template v-else-if="updFlow === 'ready'">
          <div class="upd-title">✓ 更新已就绪</div>
          <div class="upd-desc">新版本已替换完成，重启后生效</div>
          <div class="upd-btns">
            <button class="ghost" @click="closeUpdFlow">稍后手动重启</button>
            <button class="primary" @click="restartNow">立即重启</button>
          </div>
        </template>
        <template v-else-if="updFlow === 'error'">
          <div class="upd-title">更新失败</div>
          <div class="upd-desc err">{{ updError }}</div>
          <div class="upd-btns">
            <button class="ghost" @click="openReleases">手动下载</button>
            <button class="primary" @click="closeUpdFlow">关闭</button>
          </div>
        </template>
      </div>
    </div>

    <FileBar
      :files="files" :selected="selected" :loading="loadingFiles" :progress="fileProg" :file-total="fileTotal"
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
      :merged="mergedMode" :merging="merging" :no-time-names="noTimeNames"
      @open-context="openContext" @level-click="levelClick" @toggle-merged="setMerged"
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
.name {
  font-family: var(--mono);
  font-weight: 700;
  font-size: 16px;
  letter-spacing: .04em;
}
.tag { color: var(--text-faint); font-size: 12px; }
.brand-ctrl {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 2px;
  position: relative;
}
.theme-btn {
  background: transparent;
  border: none;
  font-size: 15px;
  line-height: 1;
  padding: 4px 6px;
}
.theme-btn:hover { background: var(--bg-hover); border: none; }
.appmenu {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  z-index: 150;
  min-width: 210px;
  padding: 5px;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  font-size: 12px;
  color: var(--text);
  user-select: none;
}
.mi {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 28px;
  padding: 0 10px;
  background: none;
  border: none;
  border-radius: 5px;
  color: var(--text);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.mi:hover { background: var(--bg-float-hover); }
.upd-msg {
  padding: 6px 10px 4px;
  color: var(--text-dim);
  font-size: 11.5px;
  border-top: 1px solid var(--border);
  margin-top: 4px;
  white-space: pre-wrap;
}
.upd-msg.link { color: var(--amber); cursor: pointer; }

/* 自动更新浮层（复用关于弹窗的浮层层级与表面 token） */
.upd-mask {
  position: fixed;
  inset: 0;
  z-index: 300;
  background: var(--mask);
  display: flex;
  align-items: center;
  justify-content: center;
}
.upd-dlg {
  width: 340px;
  padding: 22px 24px 18px;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.upd-title { font-weight: 700; font-size: 14px; }
.upd-desc { color: var(--text-dim); font-size: 12px; word-break: break-all; }
.upd-desc.err { color: var(--lvl-3-fg); }
.upd-btns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 6px; }
.dl-bar { height: 6px; border-radius: 3px; background: var(--bg-elev); overflow: hidden; }
.dl-fill { height: 100%; background: var(--amber); transition: width .15s; }
.upd-cta { color: var(--amber); font-weight: 600; }
.about-startup { color: var(--text-faint); font-size: 11px; font-family: var(--mono); }

/* 关于弹窗 */
.about-mask {
  position: fixed;
  inset: 0;
  z-index: 300;
  background: var(--mask);
  display: flex;
  align-items: center;
  justify-content: center;
}
.about {
  width: 300px;
  padding: 26px 24px 18px;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  text-align: center;
}
.about-icon { width: 52px; height: 52px; border-radius: 12px; }
.about-name { font-family: var(--mono); font-weight: 700; font-size: 17px; }
.about-ver { color: var(--amber); font-family: var(--mono); font-size: 12.5px; }
.about-desc { color: var(--text-dim); font-size: 12px; }
.about-link {
  color: var(--text-dim);
  font-size: 11.5px;
  font-family: var(--mono);
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 4px;
}
.about-link:hover { color: var(--amber); background: var(--bg-hover); }
.about button { margin-top: 12px; }
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
