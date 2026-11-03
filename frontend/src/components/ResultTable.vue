<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { GetResults, ExportResults, SaveFileDialog, CopyLineText, CopyText, CancelExport } from '../../wailsjs/go/main/App'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import { logscan } from '../../wailsjs/go/models'
import HighlightText from './HighlightText.vue'
import AppIcon from './AppIcon.vue'

const props = defineProps<{
  stats: logscan.QueryStats | null
  querying: boolean
  progress: { scanned: number; total: number; matched: number } | null
  fileCount: number
  fileNames: Map<number, string>
  highlightKeywords: string[]
  highlightRegex: string
  merged: boolean
  merging: boolean
  noTimeNames: string[]
}>()
const emit = defineEmits<{
  (e: 'open-context', fileId: number, offset: number): void
  (e: 'level-click', level: number): void
  (e: 'toggle-merged', merged: boolean): void
}>()

const ROW_H = 30
const OVERSCAN = 15
const PAGE = 300
const MAX_CANVAS_H = 30_000_000 // 纵向画布上限（浏览器 ~33M px）
const SEG_ROWS = Math.floor(MAX_CANVAS_H / ROW_H) - 1000 // 每段行数（约 99 万）
// 横向画布上限：极长行（如整行 SQL dump 数十万字节）按 12px/rune 撑开会是数百万
// px 宽的超大层，WebView 光栅化直接卡死界面；封顶后超出部分横向滚动不可达，
// 看全文走双击行的上下文面板（其内单行同样有 64KB 防御截断）
const MAX_CANVAS_W = 200_000

// 列宽（表头与行共用）。文件列动态：按会话文件名集合 hug 内容（中略后最长文本
// 估宽，mono 11px ≈ 6.7px/字符 + 圆点与间距余量），clamp [64,240] 防极端；
// .c-file 必须带 min-width:0——flex 子项默认 min-width:auto 会用整段 nowrap
// 文本把 width 顶开，ellipsis 失效、列后拖出大段空白（实测踩坑）
const W_FILE = 200, W_LINE = 64, W_TS = 122, W_LVL = 64, PAD = 20
const FIXED_W = W_LINE + W_TS + W_LVL + PAD

const viewport = ref<HTMLElement | null>(null)
const scrollTop = ref(0)
const scrollLeft = ref(0)
const viewH = ref(600)
const viewW = ref(900)
// 结果行缓存：shallowRef——Map 只做浅引用（行对象内容不可变），批次到达整体替换
// 触发一次更新；深层代理会让每批数据触发全部可见行重渲染
const rowsCache = shallowRef(new Map<number, logscan.ResultRow>())
const selIdx = ref(-1)
const jumpTo = ref('')
const segStart = ref(0)
let fetchSeq = 0
let rafPending = false

// ---- 右键「复制」菜单 ----
// 复制走后端取完整原文/选中文本并直写系统剪贴板（前端剪贴板 API 对大文本会截断）；
// 反馈与详情面板一致：菜单内「✓ 已复制」后收起
const rowMenu = ref<{ x: number; y: number; row: logscan.ResultRow; sel: string } | null>(null)
const rowMenuCopied = ref<'' | 'row' | 'sel'>('')
let rowMenuCloseTimer: number | undefined // 反馈自动收起定时器（重开菜单先清，防误关新菜单）
function openRowMenu(e: MouseEvent, i: number) {
  const r = rowsCache.value.get(i)
  if (!r) return
  e.preventDefault()
  rowMenuCopied.value = ''
  window.clearTimeout(rowMenuCloseTimer)
  rowMenu.value = { x: e.clientX, y: e.clientY, row: r, sel: (window.getSelection()?.toString() ?? '').trim() }
}
function closeRowMenu() { rowMenu.value = null }
function rowMenuCopiedOK(kind: 'row' | 'sel') {
  rowMenuCopied.value = kind
  window.clearTimeout(rowMenuCloseTimer)
  rowMenuCloseTimer = window.setTimeout(closeRowMenu, 700) // 「已复制」短暂反馈后收起
}
// fixed 锚定鼠标坐标，贴近视口右/下缘时收回避免溢出
const rowMenuStyle = computed(() => {
  const m = rowMenu.value
  if (!m) return {}
  return {
    left: Math.max(8, Math.min(m.x, window.innerWidth - 200)) + 'px',
    top: Math.max(8, Math.min(m.y, window.innerHeight - 70)) + 'px',
  }
})
async function copyRow() {
  const m = rowMenu.value
  if (!m) return
  try {
    await CopyLineText(m.row.fileId, m.row.offset) // 后端取完整原文并直写系统剪贴板
    rowMenuCopiedOK('row')
  } catch (e) {
    showToast('复制失败：' + e)
    closeRowMenu()
  }
}

async function copySelText() {
  const sel = rowMenu.value?.sel
  if (!sel) return
  try {
    await CopyText(sel)
    rowMenuCopiedOK('sel')
  } catch (e) {
    showToast('复制失败：' + e)
    closeRowMenu()
  }
}
// 导出下拉（单按钮合并入口）
const expOpen = ref(false)
if (typeof document !== 'undefined') {
  document.addEventListener('mousedown', (e: MouseEvent) => {
    if (rowMenu.value && !(e.target as HTMLElement)?.closest?.('.row-menu')) closeRowMenu()
    if (expOpen.value && !(e.target as HTMLElement)?.closest?.('.exp-wrap')) expOpen.value = false
  })
}
function onKeydownEsc(e: KeyboardEvent) {
  if (e.key === 'Escape' && rowMenu.value) closeRowMenu()
}

const total = computed(() => props.stats?.total ?? 0)

// 文件列固定宽（120px ≈ mono 11px 下 16 字符 + 圆点与间距）：超长文件名中略保留首尾。
// .c-file 必须 min-width:0——flex 子项默认 min-width:auto 会用整段 nowrap 文本把
// width 顶开、ellipsis 失效（实测踩坑），这才是此前「列宽不生效」的根因
const FILE_COL_W = 160
const FILE_TRUNC = 19 // 中略尾部保留 9 字符：轮转名尾缀（如 02.4.log）完整可读

// 画布宽度：按「当前渲染窗口」实测（可伸可缩）——等价于表格按内容自动撑开。
// 不用 stats.maxLineLen 预估（真实日志常有个别 100KB+ 离群长行，按文件级最大行长
// 预支画布会得到几十万 px 空白滚动区）；也不用单调递增（宽行滚过后画布永久停在
// 高位，日常区滚动条再也回不来——实测踩坑）。改为随当前渲染的最宽行实时伸缩：
// 长行滚入视口画布长大，滚出渲染窗口即缩回，滚动条始终反映眼前内容；
// MAX_CANVAS_W 封顶截断语义不变（超长行全文走详情面板）
const seenMaxPx = ref(0) // 当前渲染窗口内最宽内容行（内容区 px；重查询/切视图时重置）
const canvasW = computed(() =>
  Math.max(viewW.value, Math.min(FIXED_W + FILE_COL_W + seenMaxPx.value + 64, MAX_CANVAS_W)))
// 画布收缩时滚动位置可能越界，主动钳回最右可滚处（浏览器虽会 clamp，主动做防跳变）
watch(canvasW, () => {
  const el = viewport.value
  if (!el) return
  const maxScroll = Math.max(0, canvasW.value - el.clientWidth)
  if (el.scrollLeft > maxScroll) el.scrollLeft = maxScroll
})
// 批次渲染后测量：当前渲染窗口最宽行（直接赋值，允许宽行滚出后缩回）
function measureRenderedRows() {
  const el = viewport.value
  if (!el) return
  let widest = 0
  el.querySelectorAll<HTMLElement>('.c-text').forEach(n => {
    const w = n.offsetWidth
    if (w > widest) widest = w
  })
  seenMaxPx.value = widest
}

// ---- 分段 ----
const segEnd = computed(() => Math.min(segStart.value + SEG_ROWS, total.value))
const segCanvasH = computed(() => (segEnd.value - segStart.value) * ROW_H)
const hasPrevSeg = computed(() => segStart.value > 0)
const hasNextSeg = computed(() => segEnd.value < total.value)
function gotoSeg(start: number) {
  segStart.value = Math.max(0, Math.min(start, Math.max(0, total.value - 1)))
  scrollTop.value = 0
  nextTick(() => viewport.value?.scrollTo({ top: 0 }))
}

// ---- 可见窗口（固定行高） ----
const visible = computed(() => {
  if (total.value === 0) return []
  const first = Math.max(segStart.value, segStart.value + Math.floor(scrollTop.value / ROW_H) - OVERSCAN)
  const count = Math.ceil(viewH.value / ROW_H) + OVERSCAN * 2
  const last = Math.min(segEnd.value, first + count)
  const idx: number[] = []
  for (let i = first; i < last; i++) idx.push(i)
  return idx
})

// 槽位复用渲染数据：槽位号（0..N）作 v-for key，内容为 {全局行号, 行数据}——滚动
// 滑窗时只 patch 文本不 mount/unmount 行组件（此前按全局行号作 key，每帧滑出的
// 行卸载、滑入的行全新挂载，是滚动开销大头）
const visibleRows = computed(() =>
  visible.value.map(gi => ({ gi, row: rowsCache.value.get(gi) })))

// 无高亮条件时纯文本渲染，跳过 HighlightText 组件实例化（全览浏览常态场景）
const hasHl = computed(() => props.highlightKeywords.length > 0 || !!props.highlightRegex)

// ---- 数据窗口拉取 ----
async function loadVisible() {
  if (!total.value || visible.value.length === 0) return
  const idxs = visible.value
  const gaps: [number, number][] = []
  let start = -1
  for (const i of idxs) {
    if (!rowsCache.value.has(i)) {
      if (start < 0) start = i
    } else if (start >= 0) {
      gaps.push([start, i - 1]); start = -1
    }
  }
  if (start >= 0) gaps.push([start, idxs[idxs.length - 1]])
  if (!gaps.length) return
  const seq = fetchSeq
  for (const [a, b] of gaps) {
    for (let s = a; s <= b; s += PAGE) {
      const n = Math.min(PAGE, b - s + 1)
      try {
        const rows = await GetResults(s, n)
        if (seq !== fetchSeq) return
        if (rows?.length) {
          const m = rowsCache.value
          rows.forEach((r, k) => m.set(s + k, r))
          rowsCache.value = new Map(m)
          nextTick(measureRenderedRows) // 渲染后精化画布宽
        }
      } catch { /* 单次拉取失败不阻断 */ }
    }
  }
}

// 视图重置：清缓存、回段首；pull=true 时主动拉首屏（新查询 / 切换视图模式共用）
async function resetView(pull: boolean) {
  rowsCache.value = new Map()
  fetchSeq++
  selIdx.value = -1
  segStart.value = 0
  seenMaxPx.value = 0 // 画布实测宽度重置，随渲染重新长大
  viewport.value?.scrollTo({ top: 0, left: 0 })
  scrollTop.value = 0
  if (!pull) return
  await nextTick()
  loadVisible()
}

// 新查询：清缓存、回段首、主动拉首屏
watch(() => props.stats, s => resetView(!!s && s.total > 0))
// 视图模式切换（平铺/时间合并）：后端已重排完成，清缓存按新序重拉
watch(() => props.merged, () => resetView(total.value > 0))
watch(visible, () => loadVisible())

let resizeObs: ResizeObserver | null = null
watch(viewport, (el, _, onCleanup) => {
  resizeObs?.disconnect()
  if (!el) return
  const measure = () => {
    viewH.value = el.clientHeight || 600
    viewW.value = el.clientWidth || 900
  }
  measure()
  resizeObs = new ResizeObserver(measure)
  resizeObs.observe(el)
  onCleanup(() => resizeObs?.disconnect())
})

function onScroll() {
  if (rafPending) return
  rafPending = true
  requestAnimationFrame(() => {
    rafPending = false
    const el = viewport.value
    scrollTop.value = el?.scrollTop ?? 0
    scrollLeft.value = el?.scrollLeft ?? 0
  })
}

// ---- 行交互 ----
// 单击只选中（配 ↑↓/Enter 与跳转），双击行才打开上下文面板（设计定稿交互）
function clickRow(i: number) { selIdx.value = i }
// 文件名中段省略：保留首尾（超长轮转名一眼认出头尾）
function midTrunc(s: string, max: number): string {
  if (s.length <= max) return s
  const h = Math.ceil((max - 1) / 2), t = max - 1 - h
  return s.slice(0, h) + '…' + s.slice(s.length - t)
}
// 文件身份色（与指挥轨一致：按拖入序循环三色）
function fileDot(id: number): string {
  return `var(--file-${id % 3})`
}
// 选中行变化时跳转框数值跟随（键盘 ↑↓ / 点击行 / 跳转按钮都统一）
watch(selIdx, i => { if (i >= 0) jumpTo.value = String(i + 1) })
function openRow(i: number) {
  const r = rowsCache.value.get(i)
  if (!r) return
  emit('open-context', r.fileId, r.offset)
}
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && rowMenu.value) { closeRowMenu(); return }
  if (total.value === 0) return
  if (selIdx.value < 0 && e.key !== 'ArrowDown') return
  if (e.key === 'ArrowDown') { e.preventDefault(); selIdx.value = Math.min(total.value - 1, selIdx.value + 1); ensureVisible(selIdx.value) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); selIdx.value = Math.max(0, selIdx.value - 1); ensureVisible(selIdx.value) }
  else if (e.key === 'Enter') { e.preventDefault(); openRow(selIdx.value) }
}
function ensureVisible(i: number) {
  const el = viewport.value
  if (!el) return
  if (i < segStart.value || i >= segEnd.value) {
    gotoSeg(Math.floor(i / SEG_ROWS) * SEG_ROWS)
    return
  }
  const top = (i - segStart.value) * ROW_H
  if (top < el.scrollTop) el.scrollTo({ top })
  else if (top + ROW_H > el.scrollTop + el.clientHeight) el.scrollTo({ top: top + ROW_H - el.clientHeight })
}

// ---- 跳转 / 逐条导航 ----
function jumpToIndex(i: number) {
  i = Math.max(0, Math.min(total.value - 1, i))
  if (i < segStart.value || i >= segEnd.value) gotoSeg(Math.floor(i / SEG_ROWS) * SEG_ROWS)
  nextTick(() => viewport.value?.scrollTo({ top: (i - segStart.value) * ROW_H }))
  selIdx.value = i
  jumpTo.value = String(i + 1) // 输入框数值跟随当前条，不清空
}
function doJump() {
  const n = parseInt(jumpTo.value, 10)
  if (!n || n < 1 || n > total.value) return
  jumpToIndex(n - 1)
}
function prevRow() { if (selIdx.value > 0) jumpToIndex(selIdx.value - 1) }
function nextRow() { if (selIdx.value < total.value - 1) jumpToIndex(selIdx.value + 1) }

// ---- 展示 ----
function fmtTs(ns: number): string {
  if (!ns) return ''
  const d = new Date(ns / 1e6)
  const p = (x: number, n = 2) => String(x).padStart(n, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`
}
const LEVEL_NAMES = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL']

const levelChips = computed(() => {
  const lc = props.stats?.levelCounts
  if (!lc) return []
  const out: { lv: number; name: string; n: number }[] = []
  for (let i = 0; i < 6; i++) if (lc[i] > 0) out.push({ lv: i, name: LEVEL_NAMES[i], n: lc[i] })
  return out
})

const emptyHint = computed(() => {
  if (props.querying) return '正在扫描…'
  if (!props.stats) return props.fileCount ? '↑ 输入关键字，回车查询' : '↑ 先把日志文件拖进来'
  return '没有匹配的行 —— 试试放宽时间范围，或去掉一个筛选条件（点上方胶囊的 ×）'
})

// ---- 导出（模态框：进度/取消确认/结果三态，遮罩阻塞其他操作）----
const exporting = ref(false)
const exportFmt = ref<'txt' | 'csv'>('txt')
const exportProg = ref({ done: 0, total: 0 })
const exportState = ref<null | { ok: boolean; title: string; detail: string }>(null)
const confirmCancel = ref(false)      // 取消确认子态
let cancelRequested = false           // 本次导出是否请求过取消（区分取消 vs 失败）
function onExportProgress(p: { done: number; total: number }) {
  exportProg.value = p
}
onMounted(() => EventsOn('export:progress', onExportProgress))
onUnmounted(() => EventsOff('export:progress'))
const exportPct = computed(() => {
  const { done, total } = exportProg.value
  return total > 0 ? Math.min(100, (done / total) * 100) : 0
})

async function doExport(fmt: 'txt' | 'csv') {
  const p = await SaveFileDialog(`loghunter导出.${fmt}`, fmt)
  if (!p) return
  exportFmt.value = fmt
  exportProg.value = { done: 0, total: total.value }
  exportState.value = null
  confirmCancel.value = false
  cancelRequested = false
  exporting.value = true
  try {
    const n = await ExportResults(p, fmt)
    exportState.value = { ok: true, title: `已导出 ${n.toLocaleString()} 条`, detail: p }
  } catch (e) {
    if (cancelRequested) {
      exportState.value = { ok: false, title: '已取消', detail: `已导出约 ${exportProg.value.done.toLocaleString()} 条，保留在文件中` }
    } else {
      exportState.value = { ok: false, title: '导出失败', detail: String(e) }
    }
  } finally {
    exporting.value = false
    confirmCancel.value = false
  }
}
function confirmCancelExport() {
  cancelRequested = true
  confirmCancel.value = false
  CancelExport().catch(() => {}) // 后端置取消标志，ExportResults 下一批检查后停笔
}
function closeExport() {
  exporting.value = false
  exportState.value = null
  confirmCancel.value = false
}

// ---- toast（瞬态消息，替换原生 alert）----
const toast = ref<null | { msg: string; kind: 'danger' | 'ok' }>(null)
let toastTimer: number | undefined
function showToast(msg: string, kind: 'danger' | 'ok' = 'danger') {
  toast.value = { msg, kind }
  window.clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => { toast.value = null }, 2500)
}
</script>

<template>
  <div class="result">
    <div v-if="stats" class="statusbar">
      <span class="hitnum">{{ stats.total.toLocaleString() }}</span><span class="hitlab">命中</span>
      <span class="cost">{{ stats.costMs }} ms</span>
      <span class="vsep"></span>
      <span class="viewswitch" role="group" title="多文件结果的排列方式：平铺=按拖入顺序逐文件拼接；时间合并=按日志时间归并成单一时间线（无时间戳的行跟随上一条有时间日志，无时间文件置尾）">
        <button :class="{ on: !merged }" :disabled="querying || merging" @click="emit('toggle-merged', false)">平铺</button>
        <button :class="{ on: merged }" :disabled="querying || merging" @click="emit('toggle-merged', true)">时间合并</button>
      </span>
      <span v-if="merging" class="merging-tip">排序中…</span>
      <span class="vsep"></span>
      <span class="lvlchips">
        <button v-for="c in levelChips" :key="c.lv" class="lvc" :class="'lvc-' + c.lv"
          :title="`只看 ${c.name}（${c.n.toLocaleString()} 条）`" @click="emit('level-click', c.lv)">
          <i></i>{{ c.n.toLocaleString() }}
        </button>
      </span>
      <!-- 分隔线随警示条件渲染，无警示时不留孤线 -->
      <span v-if="stats.truncated || (merged && noTimeNames.length) || stats.skippedNoTime > 0" class="vsep"></span>
      <span v-if="stats.truncated" class="trunc"
        :title="`命中过多，只保留前 ${(stats.resultLimit ?? 2_000_000).toLocaleString()} 条（按内存自适应）`">⚠ 已截断</span>
      <span v-if="merged && noTimeNames.length" class="notime" :title="'无时间列，命中置尾：\n' + noTimeNames.join('\n')">
        ⚠ {{ noTimeNames.length }} 个文件无时间戳 · 置尾
      </span>
      <span v-if="stats.skippedNoTime > 0" class="notime"
        title="这些文件没有时间列，无法参与时间范围筛选；想查它们请清空时间条件">已跳过 {{ stats.skippedNoTime }} 个无时间文件</span>
      <span class="spacer"></span>
      <span class="navgrp">
        <button class="icon-only" :disabled="selIdx <= 0" @click="prevRow" title="上一条（↑）"><AppIcon name="chevron-left" :size="14" /></button>
        <span class="jump">
          <input type="number" min="1" :max="total" v-model="jumpTo" @keydown.enter.prevent="doJump" />
          <span class="of">/ {{ total.toLocaleString() }}</span>
        </span>
        <button class="icon-only" :disabled="selIdx < 0 || selIdx >= total - 1" @click="nextRow" title="下一条（↓）"><AppIcon name="chevron-right" :size="14" /></button>
      </span>
      <div class="exp-wrap">
        <button :class="{ open: expOpen }" title="导出查询结果" @click="expOpen = !expOpen">
          <AppIcon name="download" :size="12" />导出
        </button>
        <div v-if="expOpen" class="exp-pop">
          <button class="pk" @click="expOpen = false; doExport('txt')">导出 TXT</button>
          <button class="pk" @click="expOpen = false; doExport('csv')">导出 CSV</button>
        </div>
      </div>
    </div>
    <div v-else class="statusbar">
      <span class="dim">{{ querying ? '正在扫描…' : '等待查询' }}</span>
    </div>

    <div v-if="total === 0" class="viewport-empty">
      <template v-if="querying">
        <!-- 扫描中骨架屏：行陆续落格的预期感 -->
        <div class="skel"><i style="width:32%"></i><i style="width:50%"></i><i style="width:16%"></i><i style="width:12%"></i><i style="width:86%"></i></div>
        <div class="skel"><i style="width:24%"></i><i style="width:36%"></i><i style="width:16%"></i><i style="width:12%"></i><i style="width:92%"></i></div>
        <div class="skel"><i style="width:38%"></i><i style="width:44%"></i><i style="width:16%"></i><i style="width:12%"></i><i style="width:70%"></i></div>
        <div class="skel-note">正在扫描 · 已找到 {{ (progress?.matched ?? 0).toLocaleString() }} 条</div>
      </template>
      <template v-else>{{ emptyHint }}</template>
    </div>
    <div v-else class="tablearea">
      <!-- 表头固定在滚动容器外（不依赖 sticky），横向随滚动位移同步 -->
      <div class="head-outer">
        <div class="head-row" :style="{ width: canvasW + 'px', transform: `translateX(${-scrollLeft}px)` }">
          <span class="h-file">文件</span>
          <span class="h-line">行号</span>
          <span class="h-ts">时间</span>
          <span class="h-lvl">级别</span>
          <span class="h-text">内容（左右滚动看全文 · 双击行看上下文）</span>
        </div>
      </div>
      <div class="viewport" ref="viewport" tabindex="0" @scroll.passive="onScroll" @keydown="onKeydown">
        <div class="canvas" :style="{ height: segCanvasH + 'px', width: canvasW + 'px' }">
          <!-- key 含视图模式与查询代次：切换重排后容器重建，播放一次淡入过渡 -->
          <div class="rows" :key="(merged ? 'm' : 'f') + '-' + (stats?.total ?? 0)">
            <div v-for="(vr, slot) in visibleRows" :key="slot" class="row"
              :class="{ sel: vr.gi === selIdx, 'is-err': vr.row?.level === 4, 'is-fatal': vr.row?.level === 5 }"
              :style="{ top: ((vr.gi - segStart) * ROW_H) + 'px' }"
              @click="clickRow(vr.gi)" @dblclick="openRow(vr.gi)" @contextmenu="openRowMenu($event, vr.gi)">
              <span class="c-file" :title="fileNames.get(vr.row?.fileId ?? -1) ?? ''">
                <i class="fdot" :style="{ background: fileDot(vr.row?.fileId ?? -1) }"></i>{{ midTrunc(fileNames.get(vr.row?.fileId ?? -1) ?? '', FILE_TRUNC) }}
              </span>
              <span class="c-line" :title="String(vr.row?.lineNo ?? '')">{{ vr.row?.lineNo ?? '' }}</span>
              <span class="c-ts">{{ vr.row ? fmtTs(vr.row.ts) : '' }}</span>
              <span class="c-lvl">
                <i v-if="vr.row && vr.row.level >= 0" class="lvl" :class="'lvl-' + vr.row.level">
                  {{ LEVEL_NAMES[vr.row.level] }}
                </i>
              </span>
              <span class="c-text">
                <HighlightText v-if="hasHl" :text="vr.row?.preview ?? ''"
                  :keywords="highlightKeywords" :regex="highlightRegex" />
                <template v-else>{{ vr.row?.preview ?? '' }}</template>
              </span>
            </div>
          </div>
          <button v-if="hasNextSeg" class="seg-tip"
            :style="{ top: (segCanvasH - 56) + 'px', left: (scrollLeft + viewW / 2) + 'px' }"
            @click="gotoSeg(segEnd)">
            已显示前 {{ segEnd.toLocaleString() }} / {{ total.toLocaleString() }} 条 · 点击继续加载 ▶
          </button>
          <button v-else-if="hasPrevSeg" class="seg-tip"
            :style="{ top: (segCanvasH - 56) + 'px', left: (scrollLeft + viewW / 2) + 'px' }"
            @click="gotoSeg(0)">
            ↩ 回到开头
          </button>
        </div>
      </div>

      <!-- 右键复制菜单：fixed 锚定鼠标坐标，视口边缘翻转；反馈「✓ 已复制」后收起 -->
      <div v-if="rowMenu" class="row-menu" :style="rowMenuStyle" @mousedown.stop>
        <button v-if="rowMenuCopied === 'sel'" class="row-menu-item copied" disabled>✓ 已复制</button>
        <button v-else class="row-menu-item" :disabled="!rowMenu.sel"
          :title="rowMenu.sel ? '复制鼠标选中的文本' : '先选中文本再复制'" @click="copySelText">
          ⧉ 复制选中文本
        </button>
        <button v-if="rowMenuCopied === 'row'" class="row-menu-item copied" disabled>✓ 已复制</button>
        <button v-else class="row-menu-item" @click="copyRow">⧉ 复制整行（完整原文）</button>
      </div>
    </div>
  </div>

  <!-- 导出模态框：遮罩阻塞其他操作 + 进度/取消确认/结果三态 -->
  <div v-if="exporting || exportState" class="export-mask">
    <div class="export-dlg">
      <div class="export-title">导出 {{ exportFmt.toUpperCase() }}</div>
      <template v-if="confirmCancel">
        <div class="export-confirm">确定取消导出？已导出的部分会保留在文件中。</div>
        <div class="export-btns">
          <button class="ghost" @click="confirmCancel = false">继续导出</button>
          <button class="danger" @click="confirmCancelExport">确定取消</button>
        </div>
      </template>
      <template v-else-if="exporting">
        <div class="export-bar"><div class="export-fill" :style="{ width: exportPct + '%' }"></div></div>
        <div class="export-count">已 {{ exportProg.done.toLocaleString() }} / {{ exportProg.total.toLocaleString() }} 条 · {{ exportPct.toFixed(0) }}%</div>
        <div class="export-btns">
          <button class="ghost" @click="confirmCancel = true">取消</button>
        </div>
      </template>
      <template v-else-if="exportState">
        <div class="export-result" :class="exportState.ok ? 'ok' : 'err'">{{ exportState.title }}</div>
        <div class="export-path">{{ exportState.detail }}</div>
        <div class="export-btns">
          <button class="primary" @click="closeExport">完成</button>
        </div>
      </template>
    </div>
  </div>

  <!-- toast：瞬态消息（复制失败等） -->
  <transition name="toast">
    <div v-if="toast" class="toast" :class="toast.kind">{{ toast.msg }}</div>
  </transition>
</template>

<style scoped>
.result {
  flex: 1; min-height: 0;
  display: flex; flex-direction: column;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}
.statusbar {
  flex: none;
  display: flex; align-items: center; gap: 10px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border);
  font-size: 12.5px;
  flex-wrap: wrap;
}
/* 状态栏方案 A：分组单行 —— 大命中数 | 视图 | 级别徽标(点=筛选) | 导航(纯图标) | 导出 */
.hitnum { font-family: var(--mono); font-size: 15px; font-weight: 600; color: var(--text); letter-spacing: -.01em; }
.hitlab { font-size: 11px; color: var(--text-faint); }
.cost { color: var(--text-faint); font-family: var(--mono); font-size: 11px; }
.vsep { width: 1px; height: 18px; background: var(--border); flex: none; }
/* 紧凑级别徽标：色点+数字（悬停显级别名），点击筛选 */
.lvlchips { display: flex; gap: 4px; align-items: center; flex: 1; min-width: 0; overflow: hidden; }
.lvc {
  display: inline-flex; align-items: center; gap: 5px; padding: 2px 8px;
  border-radius: 999px; border: 1px solid transparent; background: transparent;
  cursor: pointer; font-family: var(--mono); font-size: 10.5px; color: var(--text-dim); white-space: nowrap;
}
.lvc i { width: 7px; height: 7px; border-radius: 50%; flex: none; background: var(--lvl-fg, var(--text-faint)); }
.lvc:hover { background: var(--bg-hover); border-color: var(--border-strong); }
.lvc-0 { --lvl-fg: var(--lvl-0-fg); } .lvc-1 { --lvl-fg: var(--lvl-1-fg); }
.lvc-2 { --lvl-fg: var(--lvl-2-fg); } .lvc-3 { --lvl-fg: var(--lvl-3-fg); }
.lvc-4 { --lvl-fg: var(--lvl-4-fg); } .lvc-5 { --lvl-fg: var(--lvl-5-fg); }
.navgrp { display: flex; align-items: center; gap: 2px; flex: none; }
.icon-only {
  background: transparent; border: none; color: var(--text-dim); padding: 5px 7px; border-radius: 5px;
}
.icon-only:hover:not(:disabled) { background: var(--bg-hover); border: none; color: var(--text); }
.icon-only:disabled { opacity: .35; cursor: default; }
.jump { display: inline-flex; align-items: baseline; gap: 5px; font-family: var(--mono); font-size: 11.5px; color: var(--text-dim); padding: 0 4px; white-space: nowrap; }
.jump input {
  width: auto; min-width: 60px; max-width: 90px; background: transparent; border: none;
  border-bottom: 1px dashed var(--border-strong); border-radius: 0;
  color: var(--text); font-weight: 600; font-size: 12px; font-family: var(--mono);
  padding: 0 2px; text-align: center; outline: none;
}
.jump input:focus { border-bottom-color: var(--accent-line); }
.jump .of { color: var(--text-faint); }
.trunc { color: var(--lvl-3-fg); }
.notime { color: var(--text-dim); font-size: 11.5px; }
/* 视图模式切换：两态分段，激活态沿用琥珀选中惯例（同日历选中格/段末按钮） */
.viewswitch {
  display: inline-flex;
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
}
.viewswitch button {
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-size: 11.5px;
  padding: 3px 10px;
  cursor: pointer;
}
.viewswitch button:hover:not(.on):not(:disabled) { background: var(--bg-hover); }
.viewswitch button.on { background: var(--accent-strong); color: var(--accent-ink); font-weight: 700; }
.viewswitch button:disabled { opacity: .5; cursor: default; }
.spacer { flex: 1; }
.dim { color: var(--text-dim); }
/* 导出下拉（浮层表面层级） */
.exp-wrap { position: relative; flex: none; }
.exp-wrap > button.open { color: var(--accent-strong); border-color: var(--accent-line); }
.exp-pop {
  position: absolute; top: calc(100% + 6px); right: 0; z-index: 80;
  min-width: 128px; padding: 5px;
  background: var(--bg-float); border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm); box-shadow: var(--shadow-pop), var(--sheen-light);
}
.exp-pop .pk {
  display: block; width: 100%; text-align: left;
  background: none; border: none; border-radius: 5px;
  padding: 6px 10px; font-size: 12px; color: var(--text); cursor: pointer;
}
.exp-pop .pk:hover { background: var(--bg-float-hover); border: none; }

.viewport-empty {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  color: var(--text-dim);
  font-size: 13px;
  padding: 20px 40px;
}
/* 扫描中骨架屏：预示行将陆续落格 */
.skel {
  display: grid;
  grid-template-columns: 150px 90px 180px 70px 1fr;
  gap: 12px;
  width: 100%;
  max-width: 860px;
  height: 26px;
  align-items: center;
}
.skel i {
  height: 11px;
  border-radius: 5px;
  background: linear-gradient(90deg, var(--bg-elev) 30%, var(--bg-hover) 50%, var(--bg-elev) 70%);
  background-size: 200% 100%;
  animation: skel-shimmer 1.3s linear infinite;
}
.skel-note { font-size: 11.5px; color: var(--text-faint); margin-top: 8px; font-family: var(--mono); }
@keyframes skel-shimmer { to { background-position: -200% 0; } }
@media (prefers-reduced-motion: reduce) { .skel i { animation: none; } }
.tablearea {
  flex: 1; min-height: 0;
  display: flex; flex-direction: column;
}
/* 表头固定在滚动容器外，横向位移由 transform 同步 */
.head-outer {
  flex: none;
  overflow: hidden;
  border-bottom: 2px solid var(--border-strong);
  background: var(--bg-panel);
}
.head-row {
  display: flex; align-items: center;
  height: 32px;
  padding: 0 10px;
  font-size: 12px; font-weight: 700;
  color: var(--text);
  user-select: none;
  will-change: transform;
}
.h-file { width: 160px; flex: none; min-width: 0; overflow: hidden; }
.h-line { width: 64px; flex: none; text-align: left; }
.h-ts { width: 180px; flex: none; padding-left: 12px; }
.h-lvl { width: 64px; flex: none; }
.h-text { flex: none; color: var(--text-faint); font-weight: 400; font-size: 11px; }

.viewport { flex: 1; min-height: 0; overflow: auto; position: relative; outline: none; }
.canvas { position: relative; min-width: 100%; }

/* 视图切换/新查询后的内容淡入（180ms，克制的单一过渡）；系统减动效偏好时关闭 */
@keyframes rows-fadein {
  from { opacity: .35; }
  to { opacity: 1; }
}
.rows { animation: rows-fadein .18s ease; }
@media (prefers-reduced-motion: reduce) {
  .rows { animation: none; }
}
.merging-tip {
  color: var(--accent-strong);
  font-size: 11.5px;
  animation: merging-pulse 1s ease-in-out infinite;
}
@keyframes merging-pulse {
  50% { opacity: .45; }
}

/* 行 */
.row {
  position: absolute; left: 0;
  width: 100%;
  display: flex; align-items: center;
  height: 30px;
  padding: 0 10px;
  font-family: var(--mono);
  font-size: 12px;
  border-bottom: 1px solid var(--border);
  cursor: default;
  white-space: nowrap;
  contain: layout style; /* 渲染边界隔离：行内容变化不外溢引发容器级重排 */
}
.row:hover { background: var(--bg-hover); }
/* ERROR/FATAL 左缘信号条（选中态样式在其后，覆盖优先） */
.row.is-err { box-shadow: inset 2px 0 0 var(--lvl-4-fg); }
.row.is-fatal {
  box-shadow: inset 2px 0 0 var(--lvl-5-fg);
  background: color-mix(in srgb, var(--lvl-5-fg) 4%, transparent);
}
.row.sel {
  background: var(--accent-active);
  box-shadow: inset 4px 0 0 var(--accent-strong);
  outline: 1px solid var(--accent-line);
  outline-offset: -1px;
}
.row.sel .c-line { color: var(--accent-strong); font-weight: 700; }
.row.sel .c-file { color: var(--accent-strong); }
.c-file {
  width: 160px; flex: none; min-width: 0; color: var(--text-dim);
  overflow: hidden; text-overflow: ellipsis;
  display: flex; align-items: center; gap: 6px;
  padding-right: 16px; /* 与行号列的固定列间距（拉开 .log 与行号，避免读成 log1） */
  white-space: nowrap;
}
.c-file .fdot { width: 7px; height: 7px; border-radius: 50%; flex: none; }
/* 行号：定宽左对齐，超长（亿行级大文件）省略、悬停 title 看全；
   min-width:0 防 flex 撑乱（同 c-file 的坑） */
.c-line {
  width: 64px; flex: none; min-width: 0; color: var(--text-faint);
  text-align: left; overflow: hidden; text-overflow: ellipsis;
}
.c-ts { width: 180px; flex: none; color: var(--text-dim); font-size: 11.5px; padding-left: 12px; }
.c-lvl { width: 64px; flex: none; }
.c-text {
  flex: none;
  white-space: pre;
  color: var(--text);
}

/* 分段续载按钮：悬浮于可视区中央——left 由模板按 scrollLeft+视口宽/2 动态注入，
   横向滚多远都始终可见可点（按画布 50% 定位会随滚动跑出视口，实测踩坑） */
.seg-tip {
  position: absolute; transform: translateX(-50%);
  color: var(--accent-strong); font-size: 12.5px;
  padding: 8px 22px;
  background: var(--bg-elev);
  border: 1px solid var(--accent-line);
  border-radius: 18px;
  cursor: pointer;
  box-shadow: var(--shadow-pop);
}
.seg-tip:hover { background: var(--accent-strong); color: var(--accent-ink); }

/* 右键「复制整行」菜单（浮层表面层级） */
.row-menu {
  position: fixed;
  z-index: 200;
  min-width: 186px;
  padding: 4px;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: 8px;
  box-shadow: var(--shadow-pop);
}
.row-menu-item {
  display: block;
  width: 100%;
  padding: 6px 10px;
  border: none;
  background: none;
  border-radius: 5px;
  color: var(--text);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.row-menu-item:hover { background: var(--bg-float-hover); }
.row-menu-item.copied { color: var(--accent-strong); font-weight: 600; }

/* 导出模态框：复用 about/update 弹框模式（遮罩 + 浮层卡片），遮罩阻塞其他操作 */
.export-mask {
  position: fixed; inset: 0; z-index: 400;
  background: var(--mask);
  display: flex; align-items: center; justify-content: center;
}
.export-dlg {
  width: 360px;
  padding: 20px 22px 18px;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  display: flex; flex-direction: column; gap: 12px;
}
.export-title { font-weight: 700; font-size: 14px; }
.export-bar { height: 8px; background: var(--bg-elev); border-radius: 4px; overflow: hidden; }
.export-fill { height: 100%; background: var(--accent-strong); transition: width .15s; }
.export-count { color: var(--text-dim); font-size: 12px; font-family: var(--mono); }
.export-result { font-size: 13px; font-weight: 600; }
.export-result.ok { color: var(--ok); }
.export-result.err { color: var(--danger); }
.export-confirm { color: var(--text); font-size: 12.5px; line-height: 1.5; }
.export-path { color: var(--text-dim); font-size: 11.5px; font-family: var(--mono); word-break: break-all; }
.export-btns { display: flex; justify-content: flex-end; gap: 8px; }

/* toast：瞬态消息 */
.toast {
  position: fixed; top: 16px; right: 16px; z-index: 500;
  max-width: 420px;
  padding: 9px 14px;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop);
  font-size: 12.5px;
  color: var(--text);
}
.toast.danger { border-color: var(--danger); }
.toast.ok { border-color: var(--ok); }
.toast-enter-active, .toast-leave-active { transition: opacity .18s ease, transform .18s ease; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateY(-6px); }
</style>
