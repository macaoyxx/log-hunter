<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { GetResults, ExportResults, SaveFileDialog, CopyLineText, CopyText } from '../../wailsjs/go/main/App'
import { logscan } from '../../wailsjs/go/models'
import HighlightText from './HighlightText.vue'

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

// 列宽（表头与行共用）
const W_FILE = 200, W_LINE = 86, W_TS = 122, W_LVL = 64, PAD = 20
const FIXED_W = W_FILE + W_LINE + W_TS + W_LVL + PAD

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
    alert('复制失败：' + e)
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
    alert('复制失败：' + e)
    closeRowMenu()
  }
}
if (typeof document !== 'undefined') {
  document.addEventListener('mousedown', (e: MouseEvent) => {
    if (rowMenu.value && !(e.target as HTMLElement)?.closest?.('.row-menu')) closeRowMenu()
  })
}
function onKeydownEsc(e: KeyboardEvent) {
  if (e.key === 'Escape' && rowMenu.value) closeRowMenu()
}

const total = computed(() => props.stats?.total ?? 0)

// 画布宽度：固定列 + 最长行估算（每 rune 按 12px 上界，绝不裁剪内容；短行不撑宽），
// 封顶 MAX_CANVAS_W（见其注释——超长行撑出的超大层会拖死渲染）
const canvasW = computed(() => {
  const maxLen = props.stats?.maxLineLen ?? 0
  return Math.max(viewW.value, Math.min(FIXED_W + maxLen * 12, MAX_CANVAS_W))
})

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
function clickRow(i: number) { selIdx.value = i }
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

async function doExport(fmt: 'txt' | 'csv') {
  const p = await SaveFileDialog(`loghunter导出.${fmt}`, fmt)
  if (!p) return
  try {
    const n = await ExportResults(p, fmt)
    alert(`已导出 ${n.toLocaleString()} 条 → ${p}`)
  } catch (e) {
    alert('导出失败：' + e)
  }
}
</script>

<template>
  <div class="result">
    <div v-if="stats" class="statusbar">
      <span class="hit">找到 <b>{{ stats.total.toLocaleString() }}</b> 条<span class="cost">· {{ stats.costMs }} ms</span></span>
      <span class="viewswitch" role="group" title="多文件结果的排列方式：平铺=按拖入顺序逐文件拼接；时间合并=按日志时间归并成单一时间线（无时间戳的行跟随上一条有时间日志，无时间文件置尾）">
        <button :class="{ on: !merged }" :disabled="querying || merging" @click="emit('toggle-merged', false)">平铺</button>
        <button :class="{ on: merged }" :disabled="querying || merging" @click="emit('toggle-merged', true)">时间合并</button>
      </span>
      <span v-if="merging" class="merging-tip">排序中…</span>
      <span v-if="stats.truncated" class="trunc"
        :title="`命中过多，只保留前 ${(stats.resultLimit ?? 2_000_000).toLocaleString()} 条（按内存自适应）`">⚠ 已截断</span>
      <span v-if="merged && noTimeNames.length" class="notime" :title="'无时间列，命中置尾：\n' + noTimeNames.join('\n')">
        ⚠ {{ noTimeNames.length }} 个文件无时间戳 · 置尾
      </span>
      <span v-if="stats.skippedNoTime > 0" class="notime"
        title="这些文件没有时间列，无法参与时间范围筛选；想查它们请清空时间条件">已跳过 {{ stats.skippedNoTime }} 个无时间文件</span>
      <span class="lvlstats">
        <button v-for="c in levelChips" :key="c.lv" class="lvl-stat" :class="'lvlbtn-' + c.lv"
          :title="`点击只看 ${c.name}`" @click="emit('level-click', c.lv)">
          {{ c.name }} <b>{{ c.n.toLocaleString() }}</b>
        </button>
      </span>
      <span class="spacer"></span>
      <span class="nav">
        <button class="ghost" :disabled="selIdx <= 0" @click="prevRow" title="上一条（↑）">◀ 上一条</button>
        <span class="jump">
          第 <input type="number" min="1" :max="total" v-model="jumpTo" @keydown.enter.prevent="doJump" /> / {{ total.toLocaleString() }} 条
        </span>
        <button class="ghost" :disabled="selIdx < 0 || selIdx >= total - 1" @click="nextRow" title="下一条（↓）">下一条 ▶</button>
      </span>
      <button @click="doExport('txt')">导出 TXT</button>
      <button @click="doExport('csv')">导出 CSV</button>
    </div>
    <div v-else class="statusbar">
      <span class="dim">{{ querying ? '正在扫描…' : '等待查询' }}</span>
    </div>

    <div v-if="total === 0" class="viewport-empty">{{ emptyHint }}</div>
    <div v-else class="tablearea">
      <!-- 表头固定在滚动容器外（不依赖 sticky），横向随滚动位移同步 -->
      <div class="head-outer">
        <div class="head-row" :style="{ width: canvasW + 'px', transform: `translateX(${-scrollLeft}px)` }">
          <span class="h-file">文件</span>
          <span class="h-line">行号</span>
          <span class="h-ts">时间</span>
          <span class="h-lvl">级别</span>
          <span class="h-text">内容（左右滚动查看全文，双击行看上下文）</span>
        </div>
      </div>
      <div class="viewport" ref="viewport" tabindex="0" @scroll.passive="onScroll" @keydown="onKeydown">
        <div class="canvas" :style="{ height: segCanvasH + 'px', width: canvasW + 'px' }">
          <!-- key 含视图模式与查询代次：切换重排后容器重建，播放一次淡入过渡 -->
          <div class="rows" :key="(merged ? 'm' : 'f') + '-' + (stats?.total ?? 0)">
            <div v-for="(vr, slot) in visibleRows" :key="slot" class="row" :class="{ sel: vr.gi === selIdx }"
              :style="{ top: ((vr.gi - segStart) * ROW_H) + 'px' }"
              @click="clickRow(vr.gi)" @dblclick="openRow(vr.gi)" @contextmenu="openRowMenu($event, vr.gi)">
              <span class="c-file" :title="fileNames.get(vr.row?.fileId ?? -1) ?? ''">
                {{ fileNames.get(vr.row?.fileId ?? -1) ?? '' }}
              </span>
              <span class="c-line">{{ vr.row?.lineNo ?? '' }}</span>
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
          <button v-if="hasNextSeg" class="seg-tip" :style="{ top: (segCanvasH - 44) + 'px' }" @click="gotoSeg(segEnd)">
            已到本段末尾（第 {{ segEnd.toLocaleString() }} 条）· 点击继续显示后面的结果 ▶
          </button>
          <button v-else-if="hasPrevSeg" class="seg-tip" :style="{ top: (segCanvasH - 44) + 'px' }" @click="gotoSeg(0)">
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
.hit b { color: var(--amber); font-size: 14px; font-family: var(--mono); }
.cost { color: var(--text-dim); font-family: var(--mono); margin-left: 4px; font-size: 11.5px; }
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
.viewswitch button.on { background: var(--amber); color: #1a1407; font-weight: 700; }
.viewswitch button:disabled { opacity: .5; cursor: default; }
.spacer { flex: 1; }
.dim { color: var(--text-dim); }
.lvlstats { display: flex; gap: 4px; }
.lvl-stat { font-size: 11px; padding: 2px 7px; font-family: var(--mono); border: none; background: transparent; }
.lvl-stat b { font-weight: 700; }
.lvlbtn-0 { color: var(--lvl-0-fg); } .lvlbtn-0:hover { background: var(--lvl-0-bg); }
.lvlbtn-1 { color: var(--lvl-1-fg); } .lvlbtn-1:hover { background: var(--lvl-1-bg); }
.lvlbtn-2 { color: var(--lvl-2-fg); } .lvlbtn-2:hover { background: var(--lvl-2-bg); }
.lvlbtn-3 { color: var(--lvl-3-fg); } .lvlbtn-3:hover { background: var(--lvl-3-bg); }
.lvlbtn-4 { color: var(--lvl-4-fg); } .lvlbtn-4:hover { background: var(--lvl-4-bg); }
.lvlbtn-5 { color: var(--lvl-5-fg); } .lvlbtn-5:hover { background: var(--lvl-5-bg); }
.nav { display: flex; align-items: center; gap: 6px; }
.jump { color: var(--text-dim); display: flex; align-items: center; gap: 4px; font-size: 11.5px; }
.jump input { width: 90px; padding: 3px 6px; font-family: var(--mono); }

.viewport-empty {
  flex: 1;
  display: flex; align-items: center; justify-content: center;
  color: var(--text-dim); font-size: 14px;
}
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
.h-file { width: 140px; flex: none; }
.h-line { width: 86px; flex: none; text-align: right; }
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
  color: var(--amber);
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
.row.sel {
  background: var(--amber-active);
  box-shadow: inset 4px 0 0 var(--amber);
  outline: 1px solid var(--amber-line);
  outline-offset: -1px;
}
.row.sel .c-line { color: var(--amber); font-weight: 700; }
.row.sel .c-file { color: var(--amber); }
.c-file {
  width: 140px; flex: none; color: var(--text-dim);
  overflow: hidden; text-overflow: ellipsis;
}
.c-line { width: 86px; flex: none; color: var(--text-faint); text-align: right; }
.c-ts { width: 180px; flex: none; color: var(--text-dim); font-size: 11.5px; padding-left: 12px; }
.c-lvl { width: 64px; flex: none; }
.c-text {
  flex: none;
  white-space: pre;
  color: var(--text);
}

.seg-tip {
  position: absolute; left: 50%; transform: translateX(-50%);
  color: var(--amber); font-size: 12.5px;
  padding: 8px 22px;
  background: var(--bg-elev);
  border: 1px solid var(--amber-line);
  border-radius: 18px;
  cursor: pointer;
}
.seg-tip:hover { background: var(--amber); color: #1a1407; }

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
.row-menu-item.copied { color: var(--amber); font-weight: 600; }
</style>
