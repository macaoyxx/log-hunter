<script setup lang="ts">
// 设置弹层（二级结构）：一级菜单「主题 / 检查更新 / 关于」，点「主题」进二级
// 才展开外观三态与主题色五套；选择即时生效（状态由 App 层持有并持久化）
import { onUnmounted, ref } from 'vue'
import AppIcon from './AppIcon.vue'
import { APPEARANCES, PALETTES, type Appearance, type PaletteId } from '../theme'

defineProps<{ appearance: Appearance; palette: PaletteId }>()
const emit = defineEmits<{
  (e: 'set-appearance', a: Appearance): void
  (e: 'set-palette', p: PaletteId): void
  (e: 'closed'): void
}>()

const open = ref(false)
const view = ref<'menu' | 'theme'>('menu')
const root = ref<HTMLElement | null>(null)
const onDocClick = (e: MouseEvent) => {
  if (open.value && root.value && !root.value.contains(e.target as Node)) close()
}
if (typeof document !== 'undefined') {
  document.addEventListener('click', onDocClick)
  onUnmounted(() => document.removeEventListener('click', onDocClick))
}
// 收起并回到一级（菜单项点击/点外部共用）。
// closed 事件：App 层借此清空检查更新等瞬态结果——结果生命周期绑定菜单开关
//（GitHub Desktop 模式：关菜单丢弃，重开徽标初始态，点击才查）
function close() { open.value = false; view.value = 'menu'; emit('closed') }
// 显式导航 + 阻断冒泡：防上层点击处理把弹层收掉导致「点了没反应」
function go(v: 'menu' | 'theme') { view.value = v }
defineExpose({ close })
</script>

<template>
  <div ref="root" class="theme-pop">
    <button class="tp-btn" :class="{ open }" title="设置（主题 / 检查更新 / 关于）" @click="open = !open">
      <AppIcon name="theme" :size="15" />
    </button>
    <div v-if="open" class="tp-pop" :class="{ 'tp-pop-menu': view === 'menu' }">
      <!-- 一级菜单（图标槽行版）：图标底槽 + 主/副标题两行 + 右侧元素 -->
      <template v-if="view === 'menu'">
        <button class="mi" type="button" @click.stop="go('theme')">
          <span class="ico"><AppIcon name="theme" :size="13" /></span>
          <span class="lab"><span>主题</span><span class="sub">外观 · 主题色</span></span>
          <AppIcon name="chevron-right" :size="12" class="go" />
        </button>
        <div class="tp-sep"></div>
        <slot name="menu"></slot>
      </template>
      <!-- 二级：主题设置 -->
      <template v-else>
        <div class="l2-head">
          <button class="back" type="button" title="返回" @click.stop="go('menu')"><AppIcon name="chevron-left" :size="14" /></button>
          <span>主题</span>
        </div>
        <div class="tp-sec">外观</div>
        <div class="apseg">
          <button v-for="a in APPEARANCES" :key="a.id" :class="{ on: a.id === appearance }"
            @click="emit('set-appearance', a.id)">{{ a.name }}</button>
        </div>
        <div class="tp-sec">主题色</div>
        <div class="swrow">
          <button v-for="p in PALETTES" :key="p.id" class="sw" :class="{ sel: p.id === palette }"
            :aria-label="'主题色 ' + p.name" @click="emit('set-palette', p.id)">
            <span class="dot" :style="{ background: p.dot }"></span>
            <span>{{ p.name }}</span>
          </button>
        </div>
        <div class="tp-note">偏好保存到 data/config.json（settings.theme）</div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.theme-pop { position: relative; flex: none; }
.tp-btn {
  background: transparent; border: none; color: var(--text-dim);
  padding: 6px 8px; border-radius: 5px;
}
.tp-btn:hover { background: var(--bg-hover); border: none; color: var(--text); }
.tp-btn.open { color: var(--accent-strong); }
.tp-pop {
  position: absolute; top: calc(100% + 8px); right: 0; z-index: 160;
  width: 292px; padding: 10px;
  background: var(--bg-float); border: 1px solid var(--border-strong);
  border-radius: var(--radius); box-shadow: var(--shadow-pop), var(--sheen-light);
}
.tp-pop-menu { width: 272px; padding: 8px; }
/* 一级菜单项（图标槽行）：显式 flex-start——全局 button 的 justify-content:center
   会把内容顶成居中（flex 下 text-align 无效），本组件与插槽菜单项都基于 .mi */
.mi {
  display: flex; align-items: center; justify-content: flex-start; gap: 11px;
  width: 100%; height: 44px; padding: 0 10px;
  background: none; border: none; border-radius: var(--radius-sm);
  color: var(--text); font-size: 12.5px; text-align: left; cursor: pointer;
}
.mi:hover { background: var(--bg-hover); border: none; }
.mi .ico {
  width: 28px; height: 28px; border-radius: 7px; background: var(--bg-elev);
  display: flex; align-items: center; justify-content: center;
  color: var(--text-dim); flex: none;
}
.mi:hover .ico { color: var(--accent-strong); }
.mi .lab { display: flex; flex-direction: column; gap: 1px; align-items: flex-start; min-width: 0; }
.mi .sub { font-size: 10px; color: var(--text-faint); line-height: 1.3; }
.mi .go { margin-left: auto; color: var(--text-faint); }
.mi .badge {
  margin-left: auto; font-family: var(--mono); font-size: 9.5px;
  color: var(--accent-strong); background: var(--accent-soft);
  border: 1px solid var(--accent-line); border-radius: 999px; padding: 1px 7px;
}
.mi .ck { margin-left: auto; font-size: 11px; color: var(--text-faint); }
.tp-sep { height: 1px; background: var(--border); margin: 6px 4px; }
/* 二级头 */
.l2-head {
  display: flex; align-items: center; gap: 4px; margin-bottom: 8px;
  font-size: 12.5px; font-weight: 600; color: var(--text); user-select: none;
}
.back {
  background: none; border: none; color: var(--text-dim);
  padding: 3px 4px; border-radius: 5px; display: inline-flex;
}
.back:hover { background: var(--bg-hover); border: none; color: var(--text); }
.tp-sec {
  font-size: 10px; letter-spacing: .09em; text-transform: uppercase;
  color: var(--text-faint); font-weight: 600; padding: 2px 6px 7px;
}
.apseg { display: flex; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; margin-bottom: 12px; }
.apseg button {
  flex: 1; justify-content: center; border-radius: 0; border: none;
  background: transparent; color: var(--text-dim); font-size: 11.5px; padding: 6px 4px;
}
.apseg button:hover { background: var(--bg-hover); border: none; }
.apseg button.on { background: var(--accent-soft); color: var(--accent-strong); font-weight: 600; }
.swrow { display: flex; gap: 4px; }
.sw {
  flex: 1; flex-direction: column; gap: 6px; align-items: center;
  padding: 8px 2px 6px; border-radius: var(--radius-sm); border: 1px solid transparent;
  background: transparent; color: var(--text-faint); font-size: 10px;
}
.sw:hover { background: var(--bg-hover); border: none; }
.sw.sel { border-color: var(--accent-line); background: var(--accent-soft); color: var(--text); }
.sw .dot {
  width: 20px; height: 20px; border-radius: 50%;
  box-shadow: inset 0 0 0 1px rgba(127, 127, 127, .35); /* 中性灰描边，双主题通用 */
}
.tp-note {
  margin-top: 11px; padding-top: 9px; border-top: 1px solid var(--border);
  font-family: var(--mono); font-size: 9.5px; color: var(--text-faint);
}
</style>
