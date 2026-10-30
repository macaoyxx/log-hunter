<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { GetContext } from '../../wailsjs/go/main/App'
import { logscan } from '../../wailsjs/go/models'
import HighlightText from './HighlightText.vue'
import ContextMenu from './ContextMenu.vue'
import { SLOT_NAMES } from '../constants'

const props = defineProps<{
  target: { fileId: number; offset: number }
  fileName: string
  highlightKeywords: string[]
  highlightRegex: string
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'jump', offset: number): void
}>()

const rows = ref<logscan.ContextRow[]>([])
const before = ref(100)
const after = ref(100)
const loading = ref(false)
const error = ref('')
const copied = ref('')
const bodyEl = ref<HTMLElement | null>(null)
// 放大铺满/还原（面板期间状态，关闭重开回默认；长日志行排查用）
const maximized = ref(false)
async function load() {
  loading.value = true
  error.value = ''
  try {
    rows.value = await GetContext(props.target.fileId, props.target.offset, before.value, after.value)
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
  // 目标行滚到视口中央：点击行跳转/调整行数后立刻看到定位在哪
  await nextTick()
  bodyEl.value?.querySelector('.crow.target')?.scrollIntoView({ block: 'center' })
}
onMounted(load)
// 面板常驻（非模态）：点击行跳转 = target 变化，此时必须重新加载
watch(() => props.target.offset, () => load())

function fmtTs(ns: number): string {
  if (!ns) return ''
  const d = new Date(ns / 1e6)
  const p = (x: number, n = 2) => String(x).padStart(n, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`
}

const LEVEL_NAMES = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL']

function jump(off: number) {
  if (off === props.target.offset) return
  emit('jump', off)
}

async function copyText(text: string, tag: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copied.value = tag + '已复制'
  } catch {
    copied.value = '复制失败'
  }
  setTimeout(() => (copied.value = ''), 1500)
}

// 复制目标行原文
function copyRow() {
  const row = rows.value.find(r => r.isTarget)
  copyText(row?.text ?? '', '本行')
}

// 复制上下文全部行原文（按顺序拼接，与日志文件一致）
function copyAll() {
  copyText(rows.value.map(r => r.text).join('\n'), '全部 ')
}

// ---------- 右键格式标记 ----------
// 5 个颜色槽位（标记1~5），每槽一组词；标记后详情内所有相同文本同步着色。
// 面板期间保留（jump 换目标行、调前后行数不丢），关闭面板组件卸载即清空
const MARK_SLOTS = SLOT_NAMES.length
const markSlots = ref<string[][]>(Array.from({ length: MARK_SLOTS }, () => []))
const menu = ref<{ x: number; y: number; sel: string } | null>(null)

// 扁平化标记词传给每行高亮组件
const marks = computed(() =>
  markSlots.value.flatMap((words, color) => words.map(word => ({ word, color })))
)
// 使用中的槽位序号（决定清除区列出哪些）
const usedSlots = computed(() =>
  markSlots.value.map((w, i) => (w.length ? i : -1)).filter(i => i >= 0)
)

// 详情区右键：无论有无选中都弹菜单（无选中时相关项置灰）
function onCtx(e: MouseEvent) {
  menu.value = { x: e.clientX, y: e.clientY, sel: (window.getSelection()?.toString() ?? '').trim() }
}
function copySel() {
  if (menu.value?.sel) copyText(menu.value.sel, '选中')
  menu.value = null
}
function applyMark(color: number) {
  const w = menu.value?.sel
  if (!w) return
  // 同词换色：先从所有槽位移除，再入所选槽
  for (let i = 0; i < MARK_SLOTS; i++) markSlots.value[i] = markSlots.value[i].filter(x => x !== w)
  markSlots.value[color].push(w)
  markSlots.value = [...markSlots.value]
  menu.value = null
}
function clearSlot(color: number) {
  markSlots.value[color] = []
  markSlots.value = [...markSlots.value]
  menu.value = null
}
function clearAllMarks() {
  markSlots.value = Array.from({ length: MARK_SLOTS }, () => [])
  menu.value = null
}
</script>

<template>
  <div class="panel" :class="{ maximized }">
    <div class="head">
      <div class="title">
        <span class="t-icon">🔎</span>
        <span class="t-name">{{ fileName }}</span>
        <span class="t-sub">这一行前后发生了什么</span>
      </div>
      <div class="ctrl">
        <label>前 <input type="number" min="0" max="500" v-model.number="before" @change="load" class="num" /> 行</label>
        <label>后 <input type="number" min="0" max="500" v-model.number="after" @change="load" class="num" /> 行</label>
        <button class="ghost" @click="copyRow" title="只复制目标行原文">⧉ 本行</button>
        <button class="ghost" @click="copyAll" title="复制当前显示的全部上下文行（含前后）">⧉ 全部</button>
        <span v-if="copied" class="copied-tip">{{ copied }}</span>
        <button class="ghost" @click="load" :disabled="loading" title="重新读取">⟳</button>
        <button class="ghost" @click="maximized = !maximized" :title="maximized ? '还原窗口' : '放大铺满'">{{ maximized ? '⤢' : '⛶' }}</button>
        <button class="ghost" @click="emit('close')" title="关闭">✕</button>
      </div>
    </div>
    <div class="body" ref="bodyEl" @contextmenu.prevent="onCtx($event)">
      <div v-if="loading" class="msg">正在读取…</div>
      <div v-else-if="error" class="msg err">{{ error }}</div>
      <div v-else-if="!rows.length" class="msg">这一行附近没有其他内容</div>
      <div
        v-for="(r, i) in rows" :key="i"
        class="crow" :class="{ target: r.isTarget }"
        :title="r.isTarget ? '' : '点这一行，把上下文中心移到这里'"
        @click="jump(r.offset)"
      >
        <span class="c-line">{{ r.lineNo }}</span>
        <span class="c-ts">{{ fmtTs(r.ts) }}</span>
        <span class="c-lvl">
          <i v-if="r.level >= 0" class="lvl" :class="'lvl-' + r.level">{{ LEVEL_NAMES[r.level] }}</i>
        </span>
        <span class="c-text">
          <HighlightText :text="r.text" :keywords="highlightKeywords" :regex="highlightRegex" :marks="marks" />
        </span>
      </div>
    </div>
    <ContextMenu
      v-if="menu" :x="menu.x" :y="menu.y" :sel-text="menu.sel" :used-slots="usedSlots"
      @copy="copySel" @mark="applyMark" @clear-slot="clearSlot" @clear-all="clearAllMarks"
      @close="menu = null"
    />
    <div class="foot">提示：点击任意行移视角；选中文本右键加标记；⛶ 可放大铺满；行内容为完整原文（左右滚动查看）</div>
  </div>
</template>

<style scoped>
.panel {
  position: fixed;
  top: 0; right: 0; bottom: 0;
  width: min(760px, 64vw);
  background: var(--bg-panel);
  border-left: 1px solid var(--border-strong);
  box-shadow: var(--shadow-panel);
  display: flex;
  flex-direction: column;
  z-index: 100;
  animation: slide-in .18s ease-out;
  transition: width .18s ease;
}
/* 放大铺满：长日志行排查 */
.panel.maximized { width: 100vw; }
@keyframes slide-in {
  from { transform: translateX(30px); opacity: 0; }
  to { transform: translateX(0); opacity: 1; }
}
.head {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
}
.title { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.t-icon { font-size: 14px; }
.t-name { font-weight: 700; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.t-sub { color: var(--text-dim); font-size: 11.5px; flex: none; }
.ctrl { display: flex; align-items: center; gap: 8px; color: var(--text-dim); font-size: 12px; flex: none; }
.ctrl label { display: flex; align-items: center; gap: 4px; }
.copied-tip { color: var(--amber); font-size: 11.5px; flex: none; }
.num { width: 48px; text-align: center; font-family: var(--mono); padding: 3px 4px; }
.body { flex: 1; overflow: auto; padding: 4px 0; }
.msg { padding: 40px; text-align: center; color: var(--text-dim); }
.msg.err { color: var(--danger); }
.crow {
  display: flex;
  align-items: center;
  width: max-content;
  min-width: 100%;
  padding: 4px 14px;
  font-family: var(--mono);
  font-size: 12px;
  cursor: pointer;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.crow:hover { background: var(--bg-hover); }
.crow.target {
  background: var(--amber-active);
  box-shadow: inset 4px 0 0 var(--amber);
  outline: 1px solid var(--amber-line);
  outline-offset: -1px;
  font-weight: 600;
}
.crow.target .c-line { color: var(--amber); font-weight: 700; }
.c-line { width: 62px; flex: none; color: var(--text-faint); text-align: right; margin-right: 12px; }
.c-ts { width: 160px; flex: none; color: var(--text-dim); font-size: 11.5px; }
.c-lvl { width: 60px; flex: none; }
.c-text {
  flex: none;
  white-space: pre;
  color: var(--text);
}
.foot {
  flex: none;
  padding: 6px 14px;
  border-top: 1px solid var(--border);
  background: var(--bg-panel);
  color: var(--text-faint);
  font-size: 11.5px;
}
</style>
