<script setup lang="ts">
// 时间直方图 —— 结果集按时间桶的分布（Kibana Discover 同款位置：结果区上方横条）。
// 设计约束（v0.2.0 预览已确认）：级别信号色堆叠（无级别行为底部中性灰段，全部无级别
// 时自然退化为单色总量条）；悬停桶详情；拖框缩小时间范围 → 时间胶囊 + 立即重查；
// 双击跳到该时刻（清除时间条件走「清除时间」按钮/胶囊 ×）。颜色全走 style.css token
import { computed, onMounted, onUnmounted, reactive, ref, shallowRef, watch } from 'vue'
import { GetTimeHistogram } from '../../wailsjs/go/main/App'
import { logscan } from '../../wailsjs/go/models'
import AppIcon from './AppIcon.vue'

const props = defineProps<{
  stats: logscan.QueryStats | null
  merged: boolean
}>()
const emit = defineEmits<{
  (e: 'brush', startNs: number, endNs: number): void
  (e: 'jump', ns: number): void
}>()

// ---- 数据拉取：查询完成（stats 换新对象）或视图模式切换即重拉。合并/平铺在查询结果
// 下只重排同一批行（分布本不变），但零物化全览切合并会按结果上限截断行集合
// （materializeBrowseLocked），口径随之变化必须重拉——查询模式下多一次聚合是可接受
// 代价（重排本身即几百 ms）。代次 seq 防旧请求迟到覆盖新结果 ----
const hist = shallowRef<logscan.Histogram | null>(null)
let pullSeq = 0
function pull() {
  if (!props.stats) { hist.value = null; return }
  const seq = ++pullSeq
  GetTimeHistogram().then(h => { if (seq === pullSeq) hist.value = h }).catch(() => {})
}
watch([() => props.stats, () => props.merged], pull, { immediate: true })

// ---- 宽度自适应（指挥轨收起/窗口缩放都触发） ----
const wrap = ref<HTMLElement | null>(null)
const svgW = ref(900)
let ro: ResizeObserver | undefined
function measure() {
  // 26 = 卡片左右 padding 12×2 + border 2
  if (wrap.value) svgW.value = Math.max(320, wrap.value.clientWidth - 26)
}
onMounted(() => {
  measure()
  ro = new ResizeObserver(measure)
  if (wrap.value) ro.observe(wrap.value)
})
onUnmounted(() => ro?.disconnect())

// ---- 几何 ----
const H = 64          // 条形区高
const AXIS = 18       // 轴标签高
const nB = computed(() => hist.value?.buckets?.length ?? 0)
const span = computed(() => (hist.value?.step ?? 0) * nB.value)
const bw = computed(() => nB.value > 0 ? svgW.value / nB.value : 0)
const visible = computed(() => !!hist.value?.hasData && nB.value > 0)
// 轴标签精度：桶宽 <1min 标到秒；≥4 天带日期
const withSec = computed(() => (hist.value?.step ?? 0) < 60_000_000_000)
const withDate = computed(() => (hist.value?.step ?? 0) >= 86_400_000_000_000 * 4)

// 级别段顺序：无级别（中性）垫底 → TRACE → FATAL 冒头（异常在顶部视觉重心）
const SEGS = [6, 0, 1, 2, 3, 4, 5]
const segOpacity = (lv: number) => (lv === 6 ? 0.9 : lv <= 1 ? 0.55 : lv === 2 ? 0.75 : lv === 3 ? 0.85 : lv === 4 ? 0.9 : 1)

type Bar = { x: number; segs: { y: number; h: number; lv: number }[] }
const bars = computed<Bar[]>(() => {
  const h = hist.value
  if (!h || !visible.value) return []
  const maxT = Math.max(1, h.maxTotal)
  const out: Bar[] = []
  for (let i = 0; i < nB.value; i++) {
    const c = h.buckets[i]
    const segs: { y: number; h: number; lv: number }[] = []
    let y = H
    for (const lv of SEGS) {
      if (c[lv] <= 0) continue
      const hh = c[lv] / maxT * H
      y -= hh
      segs.push({ y, h: Math.max(hh, 0.75), lv })
    }
    out.push({ x: i * bw.value, segs })
  }
  return out
})

// ---- 轴：每 k 桶一个刻度（k 让总刻度 ≤ 10） ----
const ticks = computed(() => {
  const h = hist.value
  if (!h || !visible.value) return [] as { x: number; label: string }[]
  const k = Math.max(1, Math.ceil(nB.value / 10))
  const out: { x: number; label: string }[] = []
  for (let i = 0; i < nB.value; i += k) {
    out.push({ x: i * bw.value + bw.value / 2, label: fmtTs(h.start + i * h.step) })
  }
  return out
})
const p2 = (x: number) => String(x).padStart(2, '0')
function fmtTs(ns: number): string {
  const d = new Date(ns / 1e6)
  const hm = `${p2(d.getHours())}:${p2(d.getMinutes())}`
  const head = withDate.value ? `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ` : ''
  return head + hm + (withSec.value ? `:${p2(d.getSeconds())}` : '')
}

// ---- 汇总信息 ----
const totalRows = computed(() => {
  const h = hist.value
  return h ? h.buckets.reduce((a, c) => a + c[0] + c[1] + c[2] + c[3] + c[4] + c[5] + c[6], 0) : 0
})
// 全部行都无级别（plain 退化形态）：峰值小字用中性色（没有信号就没有彩色）
const isPlain = computed(() => {
  const h = hist.value
  if (!h) return false
  let lv = 0, un = 0
  for (const c of h.buckets) {
    lv += c[0] + c[1] + c[2] + c[3] + c[4] + c[5]
    un += c[6]
  }
  return lv === 0 && un > 0
})
// 峰值标注：最高桶（分布足够密时才有意义，稀疏几桶会到处是峰）
const peak = computed(() => {
  const h = hist.value
  if (!h || !visible.value || nB.value < 10 || h.maxTotal < 50) return null
  const total = (c: number[]) => c[0] + c[1] + c[2] + c[3] + c[4] + c[5] + c[6]
  let idx = h.buckets.findIndex(c => total(c) === h.maxTotal)
  if (idx < 0) idx = 0
  return { x: idx * bw.value + bw.value / 2, n: h.maxTotal, plain: isPlain.value }
})

// ---- 悬停（拖框期间不显示） ----
const LVL_NAMES = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL']
const tip = reactive({ i: -1, x: 0, y: 0 })
const tipShow = computed(() => tip.i >= 0 && !dragging.value)
const tipBucket = computed(() => hist.value && tip.i >= 0 ? hist.value.buckets[tip.i] : null)
function onMove(e: PointerEvent) {
  if (dragging.value) { onDragMove(e); return }
  const svg = e.currentTarget as SVGSVGElement
  const box = svg.getBoundingClientRect()
  tip.i = Math.max(0, Math.min(nB.value - 1, Math.floor((e.clientX - box.left) / bw.value)))
  const card = svg.parentElement!.getBoundingClientRect()
  tip.x = e.clientX - card.left + 14
  tip.y = e.clientY - card.top - 8
}
function onLeave() { tip.i = -1 }

// ---- 拖框（brush）：释放后把选区时间范围交给查询链（setTimeRangeAndSearch） ----
const dragging = ref<{ x0: number; x1: number } | null>(null)
function onDown(e: PointerEvent) {
  const svg = e.currentTarget as SVGSVGElement
  const box = svg.getBoundingClientRect()
  dragging.value = { x0: e.clientX - box.left, x1: e.clientX - box.left }
  // 捕获让拖出 SVG 也能收到 move/up；合成/注入事件的 pointerId 可能不在活动指针表，
  // setPointerCapture 会抛 NotFoundError 中断拖拽——退化为普通监听（慢速拖拽足够）
  try { svg.setPointerCapture(e.pointerId) } catch { /* pointerId 非活动指针，忽略捕获 */ }
  tip.i = -1
}
function onDragMove(e: PointerEvent) {
  const d = dragging.value
  if (!d) return
  const box = (e.currentTarget as SVGSVGElement).getBoundingClientRect()
  d.x1 = Math.max(0, Math.min(svgW.value, e.clientX - box.left))
}
function onUp() {
  const d = dragging.value
  dragging.value = null
  const h = hist.value
  if (!d || !h) return
  const x0 = Math.min(d.x0, d.x1), x1 = Math.max(d.x0, d.x1)
  if (x1 - x0 < bw.value) return // 至少跨一个桶
  const s = h.start + Math.floor(x0 / svgW.value * span.value)
  const e2 = h.start + Math.ceil(x1 / svgW.value * span.value)
  if (e2 - s >= 10_000_000) emit('brush', s, e2) // ≥10ms 才有意义
}
// 双击 = 跳转定位：结果表滚到该时刻之后最早一条（清除时间条件走「清除时间」按钮/胶囊 ×）
function onDblclick(e: MouseEvent) {
  const h = hist.value
  if (!h) return
  const svg = e.currentTarget as SVGSVGElement
  const box = svg.getBoundingClientRect()
  const x = Math.max(0, Math.min(svgW.value, e.clientX - box.left))
  const ns = h.start + Math.round(x / svgW.value * span.value)
  emit('jump', ns)
}
function reset() { emit('brush', 0, 0) }
</script>

<template>
  <div v-if="visible" class="tsh" ref="wrap">
    <div class="tsh-head">
      <span class="tsh-title">时间分布</span>
      <span class="tsh-sub">{{ totalRows.toLocaleString() }} 行 · {{ fmtTs(hist!.start) }} ~ {{ fmtTs(hist!.start + span) }}</span>
      <span class="sp"></span>
      <span v-if="hist!.noTs > 0" class="tsh-nots" :title="`${hist!.noTs.toLocaleString()} 行无时间戳（含无时间格式文件的命中行），未计入分布`">{{ hist!.noTs.toLocaleString() }} 行无时间戳未计入</span>
      <span class="tsh-tip">拖框缩小时间范围 · 双击跳到该时刻</span>
      <button class="tsh-reset" title="清除时间条件并重新查询（回到完整时间范围）" @click="reset"><AppIcon name="x" :size="10" />清除时间</button>
    </div>
    <svg class="tsh-svg" :width="svgW" :height="H + AXIS" :viewBox="`0 0 ${svgW} ${H + AXIS}`"
      @pointermove="onMove" @pointerleave="onLeave" @pointerdown="onDown"
      @pointerup="onUp" @dblclick="onDblclick">
      <line v-for="t in ticks" :key="'g' + t.x" class="gridline" :x1="t.x" y1="0" :x2="t.x" :y2="H" />
      <text v-for="t in ticks" :key="'t' + t.x" class="axis-t" :x="Math.max(24, Math.min(t.x, svgW - 24))" :y="H + 14" text-anchor="middle">{{ t.label }}</text>
      <text v-if="peak" class="peak-t" :class="{ plain: peak.plain }" :x="Math.max(Math.min(peak.x, svgW - 56), 56)" y="9" text-anchor="middle">峰值 {{ peak.n.toLocaleString() }}</text>
      <!-- 级别堆叠条 -->
      <g v-for="(b, i) in bars" :key="i">
        <rect v-for="(s, j) in b.segs" :key="j" :x="b.x + 0.5" :y="s.y" :width="Math.max(bw - 1, 0.8)" :height="s.h" rx="1"
          :fill="s.lv === 6 ? 'var(--scroll-thumb)' : `var(--lvl-${s.lv}-fg)`" :opacity="segOpacity(s.lv)" />
      </g>
      <!-- 悬停桶高亮框 -->
      <rect v-if="tipShow" class="hover-box" :x="tip.i * bw" y="0" :width="bw" :height="H" rx="2" />
      <!-- 拖框蒙层 -->
      <rect v-if="dragging && Math.abs(dragging.x1 - dragging.x0) > 2" class="brush-box"
        :x="Math.min(dragging.x0, dragging.x1)" y="0" :width="Math.abs(dragging.x1 - dragging.x0)" :height="H" rx="2" />
    </svg>
    <!-- 悬停桶详情 -->
    <div v-if="tipShow && tipBucket" class="tsh-tooltip" :style="{ left: Math.min(tip.x, svgW - 180) + 'px', top: tip.y + 'px' }">
      <div class="tt-t">{{ fmtTs(hist!.start + tip.i * hist!.step) }} ~ {{ fmtTs(hist!.start + (tip.i + 1) * hist!.step) }}</div>
      <div class="tt-total">{{ (tipBucket[0] + tipBucket[1] + tipBucket[2] + tipBucket[3] + tipBucket[4] + tipBucket[5] + tipBucket[6]).toLocaleString() }} 行</div>
      <div v-for="lv in [5, 4, 3, 2, 1, 0]" :key="lv" class="tt-row" :class="{ zero: tipBucket[lv] === 0 }">
        <i :style="{ background: `var(--lvl-${lv}-fg)` }"></i>{{ LVL_NAMES[lv] }}<span class="n">{{ tipBucket[lv].toLocaleString() }}</span>
      </div>
      <div class="tt-row" :class="{ zero: tipBucket[6] === 0 }">
        <i class="unk"></i>无级别<span class="n">{{ tipBucket[6].toLocaleString() }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tsh {
  flex: none; display: flex; flex-direction: column; gap: 2px; position: relative;
  background: var(--bg-panel); border: 1px solid var(--border); border-radius: var(--radius);
  box-shadow: var(--sheen); padding: 8px 12px 6px; user-select: none;
}
.tsh-head { display: flex; align-items: center; gap: 8px; }
.tsh-title { font-size: 10px; letter-spacing: .09em; text-transform: uppercase; color: var(--text-faint); font-weight: 600; }
.tsh-sub { font-family: var(--mono); font-size: 10px; color: var(--text-faint); }
.tsh-head .sp { flex: 1; }
.tsh-nots { font-size: 10.5px; color: var(--text-faint); }
.tsh-tip { font-size: 10.5px; color: var(--text-faint); }
.tsh-reset {
  display: inline-flex; align-items: center; gap: 3px; border: 1px solid var(--accent-line);
  background: var(--accent-soft); color: var(--accent-strong); border-radius: 999px;
  padding: 1px 9px; font-size: 10.5px; font-family: var(--mono); cursor: pointer;
}
.tsh-reset:hover { background: var(--accent-active); border-color: var(--accent-line); }
.tsh-svg { display: block; cursor: crosshair; touch-action: none; }
.gridline { stroke: var(--border); stroke-width: 1; opacity: .55; }
.axis-t { font-family: var(--mono); font-size: 9.5px; fill: var(--text-faint); }
.peak-t { font-family: var(--mono); font-size: 11px; fill: var(--lvl-4-fg); font-weight: 600; letter-spacing: .02em; }
.peak-t.plain { fill: var(--text-dim); }
.hover-box { fill: var(--bg-hover); stroke: var(--border-strong); stroke-width: 1; }
.brush-box { fill: var(--accent-active); stroke: var(--accent-line); stroke-width: 1; }

.tsh-tooltip {
  position: absolute; z-index: 30; pointer-events: none; min-width: 168px;
  background: var(--bg-float); border: 1px solid var(--border-strong); border-radius: var(--radius-sm);
  box-shadow: var(--shadow-pop), var(--sheen-light); padding: 7px 10px;
  transform: translateY(-100%);
}
.tsh-tooltip .tt-t { font-family: var(--mono); font-size: 10px; color: var(--text-faint); margin-bottom: 4px; }
.tsh-tooltip .tt-total { font-family: var(--mono); font-size: 14px; font-weight: 600; margin-bottom: 5px; }
.tsh-tooltip .tt-row { display: flex; align-items: center; gap: 6px; font-family: var(--mono); font-size: 10px; color: var(--text-dim); line-height: 1.75; }
.tsh-tooltip .tt-row i { width: 6px; height: 6px; border-radius: 99px; flex: none; }
.tsh-tooltip .tt-row i.unk { background: var(--scroll-thumb-hover); }
.tsh-tooltip .tt-row .n { margin-left: auto; padding-left: 14px; }
.tsh-tooltip .tt-row.zero { opacity: .35; }
</style>
