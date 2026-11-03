<script setup lang="ts">
// 指挥轨 —— 左侧常驻区：品牌/收起、作战概览、源文件管理（添加/移除/清空/勾选）、级别频谱、状态卡
// 语义分层：勾选框 = 是否参与查询；× = 从会话移除该文件（清索引）；清空 = 全部卸载
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { logscan } from '../../wailsjs/go/models'
import { PickFiles, MemUsage } from '../../wailsjs/go/main/App'
import AppIcon from './AppIcon.vue'

const props = defineProps<{
  files: logscan.FileEntry[]
  selected: Set<number>
  loading: boolean
  progress: { name: string; done: number; total: number; fileNo: number } | null
  fileTotal?: number
  stats: logscan.QueryStats | null
  querying: boolean
  queryProgress?: { scanned: number; total: number; matched: number } | null
  collapsed: boolean
}>()
const emit = defineEmits<{
  (e: 'add', paths: string[]): void
  (e: 'remove', id: number): void
  (e: 'clear'): void
  (e: 'toggle', id: number): void
  (e: 'toggle-all', target: boolean): void
  (e: 'toggle-rail'): void
  (e: 'level-filter', lv: number): void
}>()

async function pickFiles() {
  const paths = await PickFiles()
  if (paths?.length) emit('add', paths)
}

function fmtSize(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(0) + ' MB'
  return Math.max(1, Math.round(n / (1 << 10))) + ' KB'
}
function fmtGB(n: number): string {
  return n >= (1 << 30) ? (n / (1 << 30)).toFixed(1) + ' GB' : (n / (1 << 20)).toFixed(0) + ' MB'
}
function fmtLines(n: number): string {
  if (n >= 1e8) return (n / 1e8).toFixed(1) + '亿行'
  if (n >= 1e4) return (n / 1e4).toFixed(0) + '万行'
  return n.toLocaleString() + ' 行'
}
function fmtTs(ns: number): string {
  if (!ns) return ''
  const d = new Date(ns / 1e6)
  const p = (x: number) => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

// 文件名中段省略：保留首尾（超长轮转名先看到头尾约定）
function midTrunc(s: string, max: number): string {
  if (s.length <= max) return s
  const h = Math.ceil((max - 1) / 2), t = max - 1 - h
  return s.slice(0, h) + '…' + s.slice(s.length - t)
}

// 收起态徽章码：首字母 + 尾部数字段（app-2026-11-02 → A02；无数字取前两字符）
function badgeCode(name: string): string {
  const base = name.replace(/\.[^.]+$/, '')
  const m = base.match(/^([A-Za-z]).*?(\d{1,4})$/)
  if (m) return m[1].toUpperCase() + m[2].slice(-3)
  return base.slice(0, 2).toUpperCase()
}

// 文件身份色：按拖入序循环三色（与结果表文件列一致）
function fileDot(id: number): string {
  return `var(--file-${id % 3})`
}

const selCount = computed(() => props.files.filter(f => props.selected.has(f.id)).length)
// 全选/全不选（一次 Set 替换 + 单次重查，避免逐个 toggle 触发 N 次查询）
const allSelected = computed(() =>
  props.files.length > 0 && props.files.every(f => props.selected.has(f.id)))
const someSelected = computed(() => selCount.value > 0 && !allSelected.value)
const selSize = computed(() =>
  props.files.filter(f => props.selected.has(f.id)).reduce((s, f) => s + f.size, 0))

const LEVEL_NAMES = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL']
const levelBars = computed(() => {
  const lc = props.stats?.levelCounts
  if (!lc) return []
  const max = Math.max(1, ...lc)
  return LEVEL_NAMES.map((name, i) => ({ lv: i, name, n: lc[i] ?? 0, pct: (lc[i] ?? 0) / max * 100 }))
})

const progPct = computed(() => {
  if (!props.progress || !props.progress.total) return 0
  return Math.min(100, (props.progress.done / props.progress.total) * 100)
})

// 实时内存：进程工作集 / 系统已用/总量，2s 轮询（后端 MemUsage 绑定）
const mem = ref({ ws: 0, sysUsed: 0, sysTotal: 0 })
let memTimer: number | undefined
function fmtMem(b: number): string {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + 'G'
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(0) + 'M'
  return '0'
}
onMounted(() => {
  const tick = () => MemUsage().then(m => { mem.value = m }).catch(() => {})
  tick()
  memTimer = window.setInterval(tick, 2000)
})
onUnmounted(() => window.clearInterval(memTimer))
</script>

<template>
  <aside class="rail" :class="{ mini: collapsed }" style="--wails-drop-target: drop">
    <div class="brand">
      <AppIcon name="crosshair" :size="15" />
      <span v-if="!collapsed" class="bt">LOGHUNTER</span>
      <button v-if="!collapsed" class="railbtn" title="收起指挥轨" @click="emit('toggle-rail')">
        <AppIcon name="panel-left-close" :size="13" />
      </button>
    </div>
    <button v-if="collapsed" class="railbtn mini-toggle" title="展开指挥轨" @click="emit('toggle-rail')">
      <AppIcon name="panel-left-open" :size="13" />
    </button>

    <!-- 作战概览 -->
    <div v-if="!collapsed" class="sec">作战概览</div>
    <div v-if="!collapsed" class="ovcard">
      <div><div class="k">已选文件</div><div class="v">{{ selCount }} <small>/ {{ files.length }}</small></div></div>
      <div><div class="k">数据量</div><div class="v">{{ fmtGB(selSize) }}</div></div>
      <div><div class="k">{{ querying ? '已命中' : '命中' }}</div>
        <div class="v">{{ querying ? (queryProgress?.matched ?? 0).toLocaleString() : (stats ? stats.total.toLocaleString() : '—') }}</div></div>
      <div><div class="k">耗时</div><div class="v">{{ stats ? stats.costMs + ' ms' : '—' }}</div></div>
    </div>

    <!-- 源文件：＋添加 / 清空；文件卡勾选=参与查询，×=移除 -->
    <div v-if="!collapsed" class="sec">
      源文件<span class="n">{{ files.length }}</span>
      <button class="selall" :class="{ all: allSelected, some: someSelected }"
        :disabled="!files.length"
        :title="allSelected ? '全部取消勾选' : '全选（参与查询）'"
        @click="emit('toggle-all', !allSelected)"><AppIcon name="check" :size="11" /></button>
      <button class="secaction" title="添加日志文件（也可直接拖入窗口）" @click="pickFiles"><AppIcon name="plus" :size="12" /></button>
      <button class="secaction danger" title="清空全部文件并释放内存" :disabled="!files.length" @click="emit('clear')">
        <AppIcon name="trash-2" :size="12" />
      </button>
    </div>
    <button v-else class="mini-add" title="添加日志文件" @click="pickFiles"><AppIcon name="plus" :size="13" /></button>

    <div class="filelist">
      <!-- 空态：拖入或添加 -->
      <div v-if="files.length === 0 && !loading" class="fempty" @click="pickFiles">
        <template v-if="!collapsed">
          <AppIcon name="file-text" :size="20" />
          <div>把日志文件拖进来</div>
          <div class="sub">或点击这里选择 · 几个 GB 都行</div>
        </template>
        <template v-else><AppIcon name="file-text" :size="15" /></template>
      </div>
      <!-- 首批加载中 -->
      <div v-else-if="loading && files.length === 0" class="floading">
        <template v-if="!collapsed">
          <div class="fl-name" :title="progress?.name">{{ progress?.name }}<template v-if="fileTotal"> · {{ progress?.fileNo }}/{{ fileTotal }}</template></div>
          <div class="fl-bar"><div class="fl-fill" :style="{ width: progPct + '%' }"></div></div>
          <div class="fl-pct">{{ progPct.toFixed(0) }}% · 建索引中</div>
        </template>
        <template v-else><AppIcon name="loader" :size="15" class="spin" /></template>
      </div>
      <!-- 文件卡列表 -->
      <template v-else>
        <div v-if="!collapsed && loading" class="inline-prog">
          <span class="ip-name" :title="progress?.name">{{ progress?.name }}</span>
          <span class="ip-pct">{{ progPct.toFixed(0) }}%</span>
          <div class="ip-bar"><div class="ip-fill" :style="{ width: progPct + '%' }"></div></div>
        </div>
        <div v-for="f in files" :key="f.id" class="fcard" :class="{ on: selected.has(f.id) }"
          :title="collapsed ? f.name + ' · ' + fmtSize(f.size) : f.path">
          <!-- 收起态：缩写徽章（选中高亮） -->
          <span v-if="collapsed" class="badge">{{ badgeCode(f.name) }}</span>
          <template v-else>
            <input class="cbox" type="checkbox" :checked="selected.has(f.id)"
              :title="selected.has(f.id) ? '取消勾选：不参与查询' : '勾选：参与查询'"
              @change="emit('toggle', f.id)" />
            <div class="meta" @click="emit('toggle', f.id)">
              <div class="nm"><span class="fdot" :style="{ background: fileDot(f.id) }"></span>{{ midTrunc(f.name, 22) }}</div>
              <div class="mt">{{ fmtSize(f.size) }} · {{ fmtLines(f.lineCnt) }}<template v-if="f.hasTime"> · {{ fmtTs(f.tsMin) }}~{{ fmtTs(f.tsMax) }}</template></div>
              <div class="mt2">
                <span class="tag" :class="{ warn: !f.hasTime }">{{ f.hasTime ? '有时间列' : '无时间列' }}</span>
                <span v-if="f.enc !== 'UTF-8'" class="tag">{{ f.enc }}</span>
              </div>
            </div>
            <button class="fx" title="从会话移除（清索引）" @click.stop="emit('remove', f.id)"><AppIcon name="x" :size="11" /></button>
          </template>
        </div>
      </template>
    </div>

    <!-- 级别频谱：统计即筛选（点击该级别 = 加条件立即重查） -->
    <div v-if="!collapsed && levelBars.length" class="specpanel">
      <div class="sec inpanel">级别频谱<span class="n">点击筛选</span></div>
      <div v-for="b in levelBars" :key="b.lv" class="sprow" :title="`只看 ${b.name}（${b.n.toLocaleString()} 条）`"
        @click="emit('level-filter', b.lv)">
        <span class="ll" :style="{ color: `var(--lvl-${b.lv}-fg)` }">{{ b.name }}</span>
        <span class="bar"><i :style="{ width: b.pct + '%', background: `var(--lvl-${b.lv}-fg)` }"></i></span>
        <span class="cnt">{{ b.n > 9999 ? (b.n / 1000).toFixed(0) + 'K' : b.n.toLocaleString() }}</span>
      </div>
    </div>

    <!-- 状态卡 -->
    <div v-if="!collapsed" class="statcard">
      <div class="st-row">
        <span class="ok" :class="{ busy: querying || loading }"></span>
        <span>{{ querying ? '扫描中 · 边扫边出' : loading ? '加载文件中' : '索引就绪' }}</span>
      </div>
      <div class="st-row dim2">
        <span>App {{ fmtMem(mem.ws) }}</span>
        <span>系统 {{ fmtMem(mem.sysUsed) }}/{{ fmtMem(mem.sysTotal) }}</span>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.rail {
  flex: none;
  width: 238px;
  transition: width .18s ease;
  background: var(--bg-panel);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: var(--sheen);
}
.rail.mini { width: 54px; }

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 12px 14px 4px;
  font-family: var(--mono);
  font-weight: 600;
  font-size: 13px;
  letter-spacing: .08em;
  color: var(--text);
  user-select: none;
}
.brand :deep(svg) { color: var(--accent-strong); flex: none; }
.railbtn {
  margin-left: auto;
  background: transparent;
  border: none;
  color: var(--text-faint);
  padding: 3px;
  border-radius: 5px;
}
.railbtn:hover { background: var(--bg-hover); border: none; color: var(--text); }
.mini-toggle {
  background: transparent; border: none; color: var(--text-faint);
  padding: 4px; margin: 2px auto; border-radius: 5px; display: flex;
}
.mini-toggle:hover { background: var(--bg-hover); border: none; color: var(--text); }

.sec {
  font-size: 10px;
  letter-spacing: .09em;
  text-transform: uppercase;
  color: var(--text-faint);
  font-weight: 600;
  padding: 12px 14px 5px;
  display: flex;
  align-items: center;
  gap: 3px;
  user-select: none;
}
.sec .n { margin-left: auto; font-family: var(--mono); font-size: 9.5px; letter-spacing: 0; }
.sec.inpanel { padding: 0 2px 5px; }
.secaction {
  background: transparent; border: none; color: var(--text-faint);
  padding: 2px; border-radius: 4px; display: inline-flex;
}
.secaction:hover { background: var(--bg-hover); border: none; color: var(--text); }
.secaction.danger:hover { color: var(--danger); }
.secaction:disabled { opacity: .35; cursor: default; }
/* 全选按钮：区头小方框，勾选态填色、半选态描边 */
.selall {
  background: transparent; border: 1px solid var(--border-strong);
  color: transparent; width: 14px; height: 14px; padding: 0;
  border-radius: 4px; display: inline-flex; align-items: center; justify-content: center;
  cursor: pointer;
}
.selall.all { background: var(--accent-strong); border-color: var(--accent-strong); color: var(--accent-ink); }
.selall.some { border-color: var(--accent-line); color: var(--accent-strong); }
.selall:disabled { opacity: .35; cursor: default; }

/* 作战概览卡 */
.ovcard {
  margin: 6px 12px 4px;
  padding: 9px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-elev);
  box-shadow: var(--sheen);
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 10px;
}
.ovcard .k {
  font-size: 9px;
  letter-spacing: .06em;
  color: var(--text-faint);
  text-transform: uppercase;
  font-weight: 600;
}
.ovcard .v { font-family: var(--mono); font-size: 13px; font-weight: 600; color: var(--text); }
.ovcard .v small { font-size: 9.5px; color: var(--text-faint); font-weight: 400; }

/* 文件列表 */
.filelist {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 2px 8px 6px;
}
.fcard {
  display: flex;
  gap: 8px;
  padding: 6px 6px;
  align-items: flex-start;
  cursor: pointer;
  border-radius: var(--radius-sm);
  position: relative;
}
.fcard:hover { background: var(--bg-hover); }
.fcard .cbox { margin-top: 2px; accent-color: var(--accent-strong); cursor: pointer; flex: none; }
.fcard .meta { min-width: 0; flex: 1; }
.fcard .nm {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text);
  display: flex;
  gap: 6px;
  align-items: center;
  white-space: nowrap;
  overflow: hidden;
}
.fcard.off .nm, .fcard.off .mt { opacity: .55; }
.fdot { width: 7px; height: 7px; border-radius: 50%; flex: none; }
.fcard .mt { font-size: 9.5px; color: var(--text-faint); margin-top: 1px; font-family: var(--mono); white-space: nowrap; overflow: hidden; }
.fcard .mt2 { display: flex; gap: 4px; margin-top: 3px; }
.fcard .tag {
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 4px;
  font-family: var(--mono);
  font-size: 9px;
  color: var(--text-faint);
}
.fcard .tag.warn { border-style: dashed; }
.fcard .fx {
  position: absolute;
  right: 4px;
  top: 4px;
  padding: 2px;
  border-radius: 4px;
  opacity: 0;
  color: var(--text-faint);
  background: transparent;
  border: none;
  display: inline-flex;
}
.fcard:hover .fx { opacity: 1; }
.fcard .fx:hover { color: var(--danger); background: transparent; border: none; }
/* 收起态徽章 */
.badge {
  width: 32px;
  height: 32px;
  border-radius: 9px;
  font-family: var(--mono);
  font-size: 9.5px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border-strong);
  color: var(--text-faint);
  background: var(--bg-elev);
  letter-spacing: .02em;
}
.fcard.on .badge {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
  border-color: var(--accent-line);
  color: var(--accent-strong);
}

/* 空态 / 加载态 */
.fempty {
  margin: 8px 4px;
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius-sm);
  padding: 18px 10px;
  text-align: center;
  font-size: 11px;
  color: var(--text-dim);
  line-height: 1.9;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
}
.fempty:hover { border-color: var(--accent-line); color: var(--text); }
.fempty .sub { color: var(--text-faint); font-size: 10px; }
.floading { padding: 14px 8px; display: flex; flex-direction: column; gap: 6px; }
.fl-name { font-family: var(--mono); font-size: 10.5px; color: var(--text-dim); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.fl-bar, .ip-bar { height: 4px; border-radius: 2px; background: var(--bg-elev); overflow: hidden; }
.fl-fill, .ip-fill { height: 100%; background: var(--accent-strong); transition: width .2s; border-radius: 2px; }
.fl-pct { font-family: var(--mono); font-size: 9.5px; color: var(--text-faint); }
.inline-prog {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 2px 8px;
  padding: 4px 6px 6px;
  font-family: var(--mono);
  font-size: 9.5px;
  color: var(--text-faint);
}
.ip-name { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ip-pct { color: var(--accent-strong); }
.inline-prog .ip-bar { grid-column: 1 / 3; }
.spin { animation: spin 1.2s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .spin { animation: none; } }

/* 级别频谱面板 */
.specpanel {
  margin: 2px 12px 8px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg-elev);
  padding: 8px 10px 6px;
  box-shadow: var(--sheen);
}
.sprow {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 2px;
  border-radius: 5px;
  cursor: pointer;
}
.sprow:hover { background: var(--bg-hover); }
.sprow .ll { font-family: var(--mono); font-size: 9px; width: 36px; letter-spacing: .03em; }
.sprow .bar { height: 7px; border-radius: 3px; flex: 1; background: var(--bg-panel); overflow: hidden; }
.sprow .bar i { display: block; height: 100%; border-radius: 3px; opacity: .6; }
.sprow .cnt { font-family: var(--mono); font-size: 9.5px; color: var(--text-dim); min-width: 30px; text-align: right; }

/* 状态卡 */
.statcard {
  margin: 0 12px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 7px 10px;
  background: var(--bg-elev);
  box-shadow: var(--sheen);
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-family: var(--mono);
  font-size: 9.5px;
  color: var(--text-faint);
}
/* 两行固定布局：行内不换行，第二行 App/系统/版本两端分布 */
.st-row { display: flex; align-items: center; gap: 6px; white-space: nowrap; }
.st-row.dim2 { justify-content: space-between; color: var(--text-dim); }
.statcard .ok { width: 6px; height: 6px; border-radius: 50%; background: var(--ok); flex: none; }
.statcard .ok.busy { background: var(--accent-strong); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: .35; } }
@media (prefers-reduced-motion: reduce) { .statcard .ok.busy { animation: none; } }

/* 收起态整理 */
.rail.mini .filelist { padding: 2px 0 6px; }
.rail.mini .fcard { justify-content: center; padding: 5px 0; }
.mini-add {
  background: transparent; border: none; color: var(--text-faint);
  padding: 4px; margin: 2px auto; border-radius: 5px; display: flex;
}
.mini-add:hover { background: var(--bg-hover); border: none; color: var(--text); }
</style>
