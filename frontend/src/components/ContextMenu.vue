<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { SLOT_NAMES } from '../constants'

// 自绘右键菜单（详情面板格式标记用）：主菜单（复制 / 加标记▸ / 清除标记▸）+
// 二级子菜单（标记 1~5 槽位色项）。签名元素：通道色条——子菜单项左缘 3px 色条
// 取自示波器通道色标语义，hover 整行染该标记软色底（悬停即预览标记色）。
// fixed 定位锚定鼠标坐标；子菜单右缘弹出，视口溢出翻转到左侧（详情面板靠
// 屏幕右侧时高发）；outside mousedown、Escape、滚动关闭
const props = defineProps<{
  x: number          // 弹出横坐标（clientX）
  y: number          // 弹出纵坐标（clientY）
  selText: string    // 当前选中文本（空串 = 未选中，相关项置灰）
  usedSlots: number[] // 使用中的标记槽位（0-4）
}>()
const emit = defineEmits<{
  (e: 'copy'): void
  (e: 'mark', color: number): void
  (e: 'clearSlot', color: number): void
  (e: 'clearAll'): void
  (e: 'close'): void
}>()

const el = ref<HTMLElement | null>(null)
const px = ref(props.x)
const py = ref(props.y)

// 子菜单展开状态与定位（相对主菜单容器；flip = 视口右缘溢出翻转到左侧）
const open = ref<'mark' | 'clear' | null>(null)
const subX = ref(0)
const subY = ref(0)
const subFlip = ref(false)
const subEl = ref<HTMLElement | null>(null)
const markItem = ref<HTMLElement | null>(null)
const clearItem = ref<HTMLElement | null>(null)

let openTimer: number | undefined  // hover 防误滑展开（150ms）
let closeTimer: number | undefined // 移出延迟收起（200ms）
function cancelTimers() {
  window.clearTimeout(openTimer)
  window.clearTimeout(closeTimer)
}

// 展开子菜单并按父项位置与视口余量定位（右缘弹出，溢出翻转左侧）
async function openSub(which: 'mark' | 'clear') {
  open.value = which
  await nextTick()
  const host = which === 'mark' ? markItem.value : clearItem.value
  const main = el.value
  const sub = subEl.value
  if (!host || !main || !sub) return
  subY.value = host.offsetTop
  const gap = 6
  const rightX = main.offsetWidth + gap
  // 视口右缘放不下时翻转到主菜单左侧
  if (px.value + rightX + sub.offsetWidth > window.innerWidth - 8) {
    subX.value = -(sub.offsetWidth + gap)
    subFlip.value = true
  } else {
    subX.value = rightX
    subFlip.value = false
  }
}

// hover 父项：150ms 防误滑后展开/切换；hover 非父项（复制等）：200ms 后收起子菜单
function enterItem(which: 'mark' | 'clear' | null) {
  cancelTimers()
  if (which === null) {
    closeTimer = window.setTimeout(() => (open.value = null), 200)
  } else if (open.value !== which) {
    openTimer = window.setTimeout(() => openSub(which), 150)
  }
}

// 点击父项（箭头区域）：立即展开/收起切换
function toggleSub(which: 'mark' | 'clear') {
  if (open.value === which) {
    open.value = null
  } else {
    openSub(which)
  }
}

// 挂载后按菜单实际尺寸防视口溢出（右/下缘回退）
onMounted(async () => {
  await nextTick()
  const r = el.value?.offsetWidth ?? 0
  const b = el.value?.offsetHeight ?? 0
  if (px.value + r > window.innerWidth - 8) px.value = Math.max(8, window.innerWidth - r - 8)
  if (py.value + b > window.innerHeight - 8) py.value = Math.max(8, window.innerHeight - b - 8)
  document.addEventListener('mousedown', onOutside)
  document.addEventListener('keydown', onKey)
  document.addEventListener('scroll', onScroll, true) // capture：任意容器滚动都关
})
onUnmounted(() => {
  cancelTimers()
  document.removeEventListener('mousedown', onOutside)
  document.removeEventListener('keydown', onKey)
  document.removeEventListener('scroll', onScroll, true)
})
function onOutside(e: MouseEvent) {
  if (el.value && !el.value.contains(e.target as Node)) emit('close')
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
function onScroll() { emit('close') }
</script>

<template>
  <div
    ref="el" class="cmenu" :style="{ left: px + 'px', top: py + 'px' }"
    @contextmenu.prevent @mousedown.stop @mouseleave="enterItem(null)"
  >
    <button class="mi" :disabled="!selText" title="复制右键选中的文本" @click="emit('copy')">
      <span class="m-ico">⧉</span>复制选中文本
    </button>
    <div class="sep"></div>
    <button
      ref="markItem" class="mi has-sub" :class="{ active: open === 'mark' }" :disabled="!selText"
      @mouseenter="enterItem('mark')" @click.stop="toggleSub('mark')"
    >
      <span class="m-ico">◉</span>加标记<span class="m-arrow">▸</span>
    </button>
    <div class="sep"></div>
    <button
      ref="clearItem" class="mi has-sub" :class="{ active: open === 'clear' }" :disabled="!usedSlots.length"
      @mouseenter="enterItem('clear')" @click.stop="toggleSub('clear')"
    >
      <span class="m-ico">◌</span>清除标记<span class="m-arrow">▸</span>
    </button>

    <!-- 加标记子菜单：通道色条项，hover 整行染对应软色底即预览 -->
    <div
      v-if="open === 'mark'" ref="subEl" class="cmenu sub" :class="{ flip: subFlip }"
      :style="{ left: subX + 'px', top: subY + 'px' }"
      @mouseenter="cancelTimers" @mouseleave="enterItem(null)"
    >
      <button
        v-for="(name, i) in SLOT_NAMES" :key="i" class="mi ch" :class="'mark-' + i"
        :title="'用' + name + '色标记选中内容'"
        @click="emit('mark', i)"
      ><i class="ch-bar"></i>标记 {{ i + 1 }} · {{ name }}</button>
    </div>

    <!-- 清除子菜单：仅列出使用中的槽位 + 清除全部 -->
    <div
      v-else-if="open === 'clear'" ref="subEl" class="cmenu sub" :class="{ flip: subFlip }"
      :style="{ left: subX + 'px', top: subY + 'px' }"
      @mouseenter="cancelTimers" @mouseleave="enterItem(null)"
    >
      <button
        v-for="c in usedSlots" :key="c" class="mi ch" :class="'mark-' + c"
        :title="'清除所有用标记' + (c + 1) + '标的词'"
        @click="emit('clearSlot', c)"
      ><i class="ch-bar"></i>标记 {{ c + 1 }} · {{ SLOT_NAMES[c] }}</button>
      <div class="sep"></div>
      <button class="mi" title="清除全部标记" @click="emit('clearAll')">
        <span class="m-ico">✕</span>清除所有标记
      </button>
    </div>
  </div>
</template>

<style scoped>
.cmenu {
  position: fixed;
  z-index: 200;
  min-width: 164px;
  padding: 5px;
  background: var(--bg-elev);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: 0 6px 24px rgba(0, 0, 0, .45);
  font-size: 12px;
  color: var(--text);
  user-select: none;
  animation: menu-in .08s ease-out;
}
@keyframes menu-in {
  from { opacity: 0; transform: scale(.97); }
  to { opacity: 1; transform: scale(1); }
}
/* 子菜单：相对主菜单定位（左/顶由 style 注入） */
.sub {
  position: absolute;
  min-width: 128px;
  animation: sub-in .1s ease-out;
}
.sub.flip { animation-name: sub-in-flip; }
@keyframes sub-in {
  from { opacity: 0; transform: translateX(-4px); }
  to { opacity: 1; transform: translateX(0); }
}
@keyframes sub-in-flip {
  from { opacity: 0; transform: translateX(4px); }
  to { opacity: 1; transform: translateX(0); }
}
@media (prefers-reduced-motion: reduce) {
  .cmenu, .sub { animation: none; }
}
.mi {
  display: flex;
  align-items: center;
  gap: 8px;
  position: relative;
  width: 100%;
  height: 28px;
  padding: 0 10px 0 8px;
  background: none;
  border: none;
  border-radius: 5px;
  color: var(--text);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.mi:hover:not(:disabled) { background: var(--bg-hover); }
.mi:disabled { color: var(--text-faint); cursor: default; }
.mi:disabled .m-ico { opacity: .5; }
.m-ico {
  width: 18px;
  flex: none;
  text-align: center;
  font-family: var(--mono);
  color: var(--text-dim);
}
.m-arrow { margin-left: auto; color: var(--text-faint); font-size: 10px; }
/* 展开中的父项：左侧 2px 琥珀条（当前通道指示） */
.mi.has-sub.active { background: var(--bg-hover); }
.mi.has-sub.active::before {
  content: '';
  position: absolute;
  left: 0;
  top: 5px;
  bottom: 5px;
  width: 2px;
  border-radius: 1px;
  background: var(--amber);
}
.sep { height: 1px; margin: 4px 6px; background: var(--border); }

/* 通道色条（签名元素）：色值经 .mark-N 的 --hl-bg/--hl-fg 变量驱动，不硬编码 */
.ch { padding-left: 7px; }
.ch-bar {
  width: 3px;
  height: 16px;
  flex: none;
  border-radius: 1.5px;
  background: var(--hl-bg);
}
/* 悬停整行染该标记软色底——悬停即预览标记色；写在 .mi:hover 之后覆盖通用灰底 */
.mi.ch:hover:not(:disabled) {
  background: var(--hl-bg);
  color: var(--hl-fg);
}
</style>
