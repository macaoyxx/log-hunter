<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'

// 自绘右键菜单（详情面板格式标记用）：复制选中 / 5 色槽位标记 / 按槽位清除。
// fixed 定位锚定鼠标坐标，视口边缘自动回退；outside mousedown、Escape、滚动关闭
const props = defineProps<{
  x: number          // 弹出横坐标（clientX）
  y: number          // 弹出纵坐标（clientY）
  selText: string    // 当前选中文本（空串 = 未选中，相关项置灰）
  usedSlots: number[] // 使用中的标记槽位（0-4，决定清除区列出哪些）
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

const SLOT_NAMES = ['红', '黄', '绿', '蓝', '紫']
</script>

<template>
  <div
    ref="el" class="ctxmenu" :style="{ left: px + 'px', top: py + 'px' }"
    @contextmenu.prevent @mousedown.stop
  >
    <button class="mi" :disabled="!selText" title="复制右键选中的文本" @click="emit('copy')">
      ⧉ 复制选中文本<span v-if="!selText" class="dim">（先选中）</span>
    </button>
    <div class="sep"></div>
    <div class="mtitle">用格式标记<span v-if="selText" class="dim-word">「{{ selText.length > 12 ? selText.slice(0, 12) + '…' : selText }}」</span></div>
    <div class="colors" :class="{ disabled: !selText }">
      <button
        v-for="i in 5" :key="i" class="dot" :class="'mark-' + (i - 1)"
        :title="'标记' + i + '（' + SLOT_NAMES[i - 1] + '）'"
        @click="selText && emit('mark', i - 1)"
      ></button>
    </div>
    <div class="sep"></div>
    <div class="mtitle">清除格式标记</div>
    <template v-if="usedSlots.length">
      <button
        v-for="i in usedSlots" :key="i" class="mi"
        :title="'清除所有用标记' + (i + 1) + '标的词'"
        @click="emit('clearSlot', i)"
      >
        <i class="dot-sm" :class="'mark-' + i"></i>清除标记{{ i + 1 }}
      </button>
      <button class="mi danger" title="清除全部标记" @click="emit('clearAll')">✕ 清除所有标记</button>
    </template>
    <div v-else class="mempty">暂无标记</div>
  </div>
</template>

<style scoped>
.ctxmenu {
  position: fixed;
  z-index: 200;
  min-width: 178px;
  padding: 5px;
  background: var(--bg-elev);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: 0 12px 40px rgba(0, 0, 0, .55);
  font-size: 12px;
  color: var(--text);
  user-select: none;
}
.mi {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  padding: 6px 9px;
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
.mi.danger:hover { background: rgba(229, 83, 75, .15); color: var(--danger); }
.dim { color: var(--text-faint); font-size: 11px; }
.dim-word {
  color: var(--text-dim);
  font-size: 11px;
  margin-left: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sep { height: 1px; margin: 4px 7px; background: var(--border); }
.mtitle { padding: 4px 9px 5px; color: var(--text-faint); font-size: 11px; }
.mempty { padding: 4px 9px 7px; color: var(--text-faint); font-size: 11.5px; }
.colors { display: flex; gap: 8px; padding: 2px 9px 8px; }
.colors.disabled { opacity: .45; }
.dot {
  width: 20px;
  height: 20px;
  border-radius: 5px;
  border: 1px solid var(--border-strong);
  background: var(--hl-bg);
  cursor: pointer;
}
.colors:not(.disabled) .dot:hover { transform: scale(1.12); }
.colors.disabled .dot { cursor: default; }
.dot-sm {
  width: 11px;
  height: 11px;
  flex: none;
  border-radius: 3px;
  border: 1px solid var(--border);
  background: var(--hl-bg);
}
</style>
