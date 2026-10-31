<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { GetResults, ExportResults, SaveFileDialog } from '../../wailsjs/go/main/App'
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

// 列宽（表头与行共用）
const W_FILE = 200, W_LINE = 86, W_TS = 122, W_LVL = 64, PAD = 20
const FIXED_W = W_FILE + W_LINE + W_TS + W_LVL + PAD

const viewport = ref<HTMLElement | null>(null)
const scrollTop = ref(0)
const scrollLeft = ref(0)
const viewH = ref(600)
const viewW = ref(900)
const rowsCache = ref(new Map<number, logscan.ResultRow>())
const selIdx = ref(-1)
const jumpTo = ref('')
const segStart = ref(0)
let fetchSeq = 0
let rafPending = false

const total = computed(() => props.stats?.total ?? 0)

// 画布宽度：固定列 + 最长行估算（每 rune 按 12px 上界，绝不裁剪内容；短行不撑宽）
const canvasW = computed(() => {
  const maxLen = props.stats?.maxLineLen ?? 0
  return Math.max(viewW.value, FIXED_W + maxLen * 12)
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
        <button :class="{ on: !merged }" :disabled="querying" @click="emit('toggle-merged', false)">平铺</button>
        <button :class="{ on: merged }" :disabled="querying" @click="emit('toggle-merged', true)">时间合并</button>
      </span>
      <span v-if="stats.truncated" class="trunc" title="命中过多，只保留前 200 万条">⚠ 已截断</span>
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
          <div class="rows">
            <div v-for="i in visible" :key="i" class="row" :class="{ sel: i === selIdx }"
              :style="{ top: ((i - segStart) * ROW_H) + 'px' }"
              @click="clickRow(i)" @dblclick="openRow(i)">
              <span class="c-file" :title="fileNames.get(rowsCache.get(i)?.fileId ?? -1) ?? ''">
                {{ fileNames.get(rowsCache.get(i)?.fileId ?? -1) ?? '' }}
              </span>
              <span class="c-line">{{ rowsCache.get(i)?.lineNo ?? '' }}</span>
              <span class="c-ts">{{ rowsCache.get(i) ? fmtTs(rowsCache.get(i)!.ts) : '' }}</span>
              <span class="c-lvl">
                <i v-if="rowsCache.get(i) && rowsCache.get(i)!.level >= 0" class="lvl" :class="'lvl-' + rowsCache.get(i)!.level">
                  {{ LEVEL_NAMES[rowsCache.get(i)!.level] }}
                </i>
              </span>
              <span class="c-text">
                <HighlightText :text="rowsCache.get(i)?.preview ?? ''"
                  :keywords="highlightKeywords" :regex="highlightRegex" />
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
</style>
