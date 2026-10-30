<script setup lang="ts">
import { computed } from 'vue'
import { logscan } from '../../wailsjs/go/models'
import { PickFiles } from '../../wailsjs/go/main/App'

const props = defineProps<{
  files: logscan.FileEntry[]
  selected: Set<number>
  loading: boolean
  progress: { name: string; done: number; total: number; fileNo: number } | null
}>()
const emit = defineEmits<{
  (e: 'add', paths: string[]): void
  (e: 'remove', id: number): void
  (e: 'clear'): void
  (e: 'toggle', id: number): void
}>()

async function pickFiles() {
  const paths = await PickFiles()
  if (paths?.length) emit('add', paths)
}

function fmtSize(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return Math.round(n / (1 << 10)) + ' KB'
  return n + ' B'
}
function fmtLines(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M 行'
  if (n >= 1e3) return (n / 1e3).toFixed(0) + 'K 行'
  return n + ' 行'
}
function fmtTs(ns: number): string {
  if (!ns) return ''
  const d = new Date(ns / 1e6)
  const p = (x: number) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

const allSelected = computed(() =>
  props.files.length > 0 && props.files.every(f => props.selected.has(f.id)))

function toggleAll() {
  const target = !allSelected.value
  for (const f of props.files) {
    if (props.selected.has(f.id) !== target) emit('toggle', f.id)
  }
}

const progPct = computed(() => {
  if (!props.progress || !props.progress.total) return 0
  return Math.min(100, (props.progress.done / props.progress.total) * 100)
})

const totalSize = computed(() => props.files.reduce((s, f) => s + f.size, 0))
</script>

<template>
  <div class="filebar" style="--wails-drop-target: drop">
    <!-- 空态：主舞台拖拽区 -->
    <div v-if="files.length === 0 && !loading" class="hero">
      <div class="hero-icon"></div>
      <div class="hero-title">把日志文件拖到这里</div>
      <div class="hero-sub">支持一次多个文件 · 几个 GB 都行 · 自动识别格式和编码（含 GBK）</div>
      <button class="hero-pick" @click="pickFiles">或从电脑里选择文件</button>
    </div>

    <!-- 加载中（首个文件） -->
    <div v-else-if="loading && files.length === 0" class="hero">
      <div class="hero-title">正在解析 {{ progress?.name }}…</div>
      <div class="hero-prog"><div class="hero-prog-fill" :style="{ width: progPct + '%' }"></div></div>
      <div class="hero-sub">{{ progPct.toFixed(0) }}% · 建好索引后查询是毫秒级的</div>
    </div>

    <!-- 有文件：筹码条 -->
    <template v-else>
      <div class="head">
        <label class="sel-all" @click.prevent="toggleAll">
          <input type="checkbox" :checked="allSelected" />
          <span>全选</span>
        </label>
        <div v-if="loading" class="prog">
          <span class="prog-name">{{ progress?.name }}</span>
          <div class="prog-bar"><div class="prog-fill" :style="{ width: progPct + '%' }"></div></div>
          <span class="prog-pct">{{ progPct.toFixed(0) }}%</span>
        </div>
        <div v-else class="sum">共 {{ fmtSize(totalSize) }} · 可继续拖入追加</div>
        <button class="danger" @click="emit('clear')" title="移除全部文件并释放内存">清空重来</button>
      </div>
      <div class="chips">
        <div v-for="f in files" :key="f.id" class="chip" :class="{ off: !selected.has(f.id) }" :title="f.path">
          <label class="chip-box"><input type="checkbox" :checked="selected.has(f.id)" @change="emit('toggle', f.id)" /></label>
          <div class="chip-main" @click="emit('toggle', f.id)">
            <div class="chip-name">{{ f.name }}</div>
            <div class="chip-meta">
              {{ fmtSize(f.size) }} · {{ fmtLines(f.lineCnt) }}
              <span class="chip-tag" v-if="f.hasTime">⏱ {{ fmtTs(f.tsMin) }} ~ {{ fmtTs(f.tsMax) }}</span>
              <span class="chip-tag warn" v-else>无时间列</span>
              <span class="chip-tag" v-if="f.enc !== 'UTF-8'">{{ f.enc }}</span>
            </div>
          </div>
          <button class="chip-x" @click.stop="emit('remove', f.id)" title="移除这个文件">✕</button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.filebar {
  flex: none;
  max-height: 216px;
  overflow-y: auto;
  background: var(--bg-panel);
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius);
  padding: 10px;
}
.filebar:has(.hero) { border-style: solid; background: transparent; }

.hero {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 44px 0 40px;
}
.hero-icon {
  width: 52px; height: 40px;
  border: 2px dashed var(--border-strong);
  border-radius: 8px;
  position: relative;
  opacity: .9;
}
.hero-icon::after {
  content: "≡";
  position: absolute; inset: 0;
  display: flex; align-items: center; justify-content: center;
  color: var(--amber);
  font-size: 20px;
  font-family: var(--mono);
}
.hero-title { font-size: 17px; font-weight: 600; }
.hero-sub { color: var(--text-dim); font-size: 12px; }
.hero-pick {
  margin-top: 8px;
  border-color: var(--amber-line);
  color: var(--amber);
  background: var(--amber-soft);
}
.hero-pick:hover { background: var(--amber); color: #1a1407; }
.hero-prog { width: 340px; height: 8px; background: var(--bg-panel); border-radius: 4px; overflow: hidden; }
.hero-prog-fill { height: 100%; background: var(--amber); transition: width .2s; }

.head { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }
.sel-all {
  display: flex; align-items: center; gap: 6px;
  cursor: pointer; user-select: none; color: var(--text-dim); font-size: 12px;
}
.sum { flex: 1; color: var(--text-faint); font-size: 12px; }
.prog { flex: 1; display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-dim); }
.prog-name { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.prog-bar { flex: 1; height: 6px; background: var(--bg); border-radius: 3px; overflow: hidden; }
.prog-fill { height: 100%; background: var(--amber); transition: width .2s; }
.prog-pct { width: 34px; text-align: right; font-family: var(--mono); }

.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.chip {
  display: flex;
  align-items: stretch;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  transition: border-color .12s;
}
.chip:hover { border-color: var(--border-strong); }
.chip.off { opacity: .45; }
.chip-box { display: flex; align-items: center; padding-left: 8px; cursor: pointer; }
.chip-main { padding: 6px 8px; cursor: pointer; min-width: 0; }
.chip-name {
  font-weight: 600; font-size: 12.5px;
  max-width: 240px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.chip-meta {
  color: var(--text-dim); font-size: 11px; margin-top: 2px;
  display: flex; gap: 6px; align-items: center; flex-wrap: wrap;
}
.chip-tag {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 5px;
  font-family: var(--mono);
  font-size: 10.5px;
}
.chip-tag.warn { color: var(--text-dim); border-style: dashed; }
.chip-x {
  border: none; background: transparent; color: var(--text-faint);
  padding: 0 8px; font-size: 12px; border-radius: 0;
}
.chip-x:hover { color: var(--danger); background: transparent; }
</style>
