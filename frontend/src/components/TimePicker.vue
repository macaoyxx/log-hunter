<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// 轻量日期时间选择器：文本输入 + 自绘弹层日历
// v-model 格式与 datetime-local 一致："YYYY-MM-DDTHH:mm[:ss]"，空串 = 不限
const model = defineModel<string>({ default: '' })
const props = defineProps<{ placeholder?: string }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)

// 弹层内部状态
const viewY = ref(2026)
const viewM = ref(0) // 0-11
const hour = ref('00')
const minute = ref('00')
const second = ref('00')
const useSec = ref(false)

// 初始化弹层状态：从 modelValue 或当前时间
function initFromModel() {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(model.value || '')
  const d = m ? new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +(m[6] ?? 0)) : new Date()
  viewY.value = d.getFullYear()
  viewM.value = d.getMonth()
  hour.value = String(d.getHours()).padStart(2, '0')
  minute.value = String(d.getMinutes()).padStart(2, '0')
  second.value = String(d.getSeconds()).padStart(2, '0')
  useSec.value = !!m?.[6]
}

watch(open, v => { if (v) initFromModel() })

function toggle() {
  open.value = !open.value
}

// 点击组件外部时关闭
if (typeof document !== 'undefined') {
  document.addEventListener('click', (e: MouseEvent) => {
    if (open.value && root.value && !root.value.contains(e.target as Node)) open.value = false
  })
}

const WEEK = ['一', '二', '三', '四', '五', '六', '日']

// 当月日历格：前置留空 + 1..31
const grid = computed(() => {
  const first = new Date(viewY.value, viewM.value, 1)
  const days = new Date(viewY.value, viewM.value + 1, 0).getDate()
  const lead = (first.getDay() + 6) % 7 // 周一开头
  const cells: (number | null)[] = Array(lead).fill(null)
  for (let d = 1; d <= days; d++) cells.push(d)
  return cells
})

const monthLabel = computed(() => `${viewY.value} 年 ${viewM.value + 1} 月`)

function shiftMonth(n: number) {
  viewM.value += n
  if (viewM.value < 0) { viewM.value = 11; viewY.value-- }
  else if (viewM.value > 11) { viewM.value = 0; viewY.value++ }
}

const today = new Date()

function cellCls(d: number) {
  const c: string[] = []
  const isToday = d === today.getDate() && viewM.value === today.getMonth() && viewY.value === today.getFullYear()
  const selected = selDate.value === fmtDate(d)
  if (isToday) c.push('today')
  if (selected) c.push('sel')
  return c
}

const selDate = computed(() => {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(model.value || '')
  return m ? m[0] : ''
})

function fmtDate(d: number): string {
  return `${viewY.value}-${String(viewM.value + 1).padStart(2, '0')}-${String(d).padStart(2, '0')}`
}

function pickDay(d: number) {
  apply(fmtDate(d))
}

// 归一：1-2 位数字且不超过 max 返回补零两位串，否则 null（非法输入）
function normNum(v: string, max: number): string | null {
  return /^\d{1,2}$/.test(v) && +v <= max ? v.padStart(2, '0') : null
}

function apply(date?: string) {
  const ds = date ?? selDate.value
  if (!ds) return
  // 非法输入（清空/非数字/越界，如 "ab"、时 25）回退 model 现值对应字段（无则 00），
  // 防止拼出 "2026-10-31Tab:30" 之类脏时间串写入 v-model、查询时报格式错误
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(model.value || '')
  const h = normNum(hour.value, 23) ?? m?.[4] ?? '00'
  const mi = normNum(minute.value, 59) ?? m?.[5] ?? '00'
  const s = normNum(second.value, 59) ?? m?.[6] ?? '00'
  hour.value = h
  minute.value = mi
  second.value = s // 输入框同步回显归一后的值
  model.value = useSec.value ? `${ds}T${h}:${mi}:${s}` : `${ds}T${h}:${mi}`
}

function clearVal() {
  model.value = ''
  open.value = false
}

const display = computed(() => (model.value || '').replace('T', ' '))
</script>

<template>
  <div class="tp" ref="root">
    <button class="tp-btn" :class="{ has: !!model }" @click="toggle" :title="model || '不限'">
      <span class="tp-val">{{ display || (placeholder || '时间') }}</span>
      <span v-if="model" class="tp-clear" @click.stop="clearVal">✕</span>
      <span v-else class="tp-cal">📅</span>
    </button>
    <div v-if="open" class="tp-pop">
      <div class="tp-head">
        <button class="tp-nav" @click="shiftMonth(-1)">‹</button>
        <span class="tp-month">{{ monthLabel }}</span>
        <button class="tp-nav" @click="shiftMonth(1)">›</button>
      </div>
      <div class="tp-grid">
        <span v-for="w in WEEK" :key="w" class="tp-wk">{{ w }}</span>
        <span v-for="(c, i) in grid" :key="i" class="tp-cell" :class="c ? cellCls(c) : ['empty']"
          @click="c && pickDay(c)">{{ c || '' }}</span>
      </div>
      <div class="tp-time">
        <!-- 时/分/秒修改即写回 v-model（失焦/回车触发 change）：修复此前只在点日期格时
             才 apply，导致改完时分点「好」关闭后值仍是打开时旧值（看似无法改时分）。
             注意 apply() 必须显式空参调用——直接引用 apply 会把原生 Event 当 date 传入 -->
        <input class="tp-num" type="text" v-model="hour" maxlength="2" @change="apply()" /> :
        <input class="tp-num" type="text" v-model="minute" maxlength="2" @change="apply()" />
        <template v-if="useSec"> :
          <input class="tp-num" type="text" v-model="second" maxlength="2" @change="apply()" />
        </template>
        <button class="tp-sec-toggle ghost" @click="useSec = !useSec; apply()" :title="useSec ? '精确到分' : '精确到秒'">
          {{ useSec ? '−秒' : '+秒' }}
        </button>
        <span class="tp-spacer"></span>
        <button class="tp-ok primary" @click="apply(); open = false">好</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tp { position: relative; display: inline-block; }
.tp-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 158px;
  font-family: var(--mono);
  font-size: 12px;
  padding: 6px 10px;
}
.tp-btn.has { border-color: var(--amber-line); color: var(--amber); }
.tp-val { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tp-clear { color: var(--text-dim); }
.tp-clear:hover { color: var(--danger); }
.tp-cal { opacity: .7; font-size: 12px; }

.tp-pop {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  z-index: 60;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: 10px;
  box-shadow: var(--shadow-pop);
  padding: 12px;
  width: 262px;
}
.tp-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.tp-month { font-size: 13px; font-weight: 600; }
.tp-nav { padding: 2px 10px; font-size: 15px; line-height: 1.2; }
.tp-grid { display: grid; grid-template-columns: repeat(7, 1fr); gap: 2px; }
.tp-wk {
  text-align: center;
  color: var(--text-faint);
  font-size: 11px;
  padding: 3px 0;
}
.tp-cell {
  text-align: center;
  padding: 5px 0;
  font-family: var(--mono);
  font-size: 12px;
  border-radius: 5px;
  cursor: pointer;
  color: var(--text);
}
.tp-cell.empty { cursor: default; }
.tp-cell:hover { background: var(--bg-float-hover); }
.tp-cell.today { color: var(--amber); font-weight: 700; }
.tp-cell.sel { background: var(--amber); color: var(--amber-ink); font-weight: 700; }
.tp-time {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--border);
  font-family: var(--mono);
}
.tp-num { width: 34px; text-align: center; padding: 4px 2px; font-family: var(--mono); }
.tp-sec-toggle { padding: 3px 8px; font-size: 11px; }
.tp-spacer { flex: 1; }
.tp-ok { padding: 4px 14px; font-size: 12px; }
</style>
