<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ValidateRegex, GetConfig, SaveHistory } from '../../wailsjs/go/main/App'
import { main, logscan } from '../../wailsjs/go/models'
import TimePicker from './TimePicker.vue'

const props = defineProps<{
  disabled: boolean
  querying: boolean
  progress: { scanned: number; total: number; matched: number } | null
  error: string
  files: logscan.FileEntry[]
  selected: Set<number>
}>()
const emit = defineEmits<{
  (e: 'search', spec: main.QuerySpecJSON): void
  (e: 'cancel'): void
}>()

const keywords = ref('')
const excludes = ref('')
const regex = ref('')
const regexErr = ref('')
const keywordAnd = ref(true)
const tsStart = ref('')
const tsEnd = ref('')
const levels = ref<Set<number>>(new Set())
const showAdvanced = ref(false)

const LEVELS = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL']

watch(regex, async v => {
  regexErr.value = v ? await ValidateRegex(v) : ''
})

// 任一高级条件被使用时自动展开面板
watch([excludes, regex, tsStart, tsEnd, levels], () => {
  if (excludes.value || regex.value || tsStart.value || tsEnd.value || levels.value.size) {
    showAdvanced.value = true
  }
}, { deep: true })

function toggleLevel(i: number) {
  const s = new Set(levels.value)
  if (s.has(i)) s.delete(i); else s.add(i)
  levels.value = s
}

// 时间快捷：贴合当前选中文件的时间范围
const hasFileTime = computed(() =>
  props.files.some(f => props.selected.has(f.id) && f.hasTime))

function fitFileRange() {
  let min = Infinity, max = -Infinity
  for (const f of props.files) {
    if (!props.selected.has(f.id) || !f.hasTime) continue
    min = Math.min(min, f.tsMin)
    max = Math.max(max, f.tsMax)
  }
  if (min === Infinity) return
  const toLocal = (ns: number) => {
    const d = new Date(ns / 1e6 - new Date(ns / 1e6).getTimezoneOffset() * 60000)
    return d.toISOString().slice(0, 16)
  }
  tsStart.value = toLocal(min)
  tsEnd.value = toLocal(max)
  showAdvanced.value = true
}

function toNs(local: string): number {
  if (!local) return 0
  return new Date(local).getTime() * 1e6
}

// 筛选链：当前生效条件的胶囊视图，可逐个移除后立即重查（删光 = 回到浏览全部）
const chain = computed(() => {
  const items: { key: string; label: string; kind: string }[] = []
  for (const k of keywordList.value) {
    items.push({ key: 'kw:' + k, label: k, kind: keywordAnd.value ? '都要含' : '任一含' })
  }
  for (const k of excludeList.value) {
    items.push({ key: 'ex:' + k, label: '排除 ' + k, kind: '' })
  }
  if (regex.value.trim()) {
    items.push({ key: 're', label: '正则 ' + regex.value.trim(), kind: '' })
  }
  if (tsStart.value || tsEnd.value) {
    items.push({ key: 'ts', label: `时间 ${tsStart.value.replace('T', ' ') || '…'} ~ ${tsEnd.value.replace('T', ' ') || '…'}`, kind: '' })
  }
  for (const l of levels.value) {
    items.push({ key: 'lv:' + l, label: LEVELS[l], kind: '级别' })
  }
  return items
})

const keywordList = computed(() => keywords.value.trim() ? keywords.value.trim().split(/\s+/) : [])
const excludeList = computed(() => excludes.value.trim() ? excludes.value.trim().split(/\s+/) : [])

// ---- 正则模板库（不会写正则的直接选） ----
const REGEX_TEMPLATES = [
  { name: 'IP 地址', hint: '找出所有 IPv4 地址', re: '\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}' },
  { name: 'URL 链接', hint: '找出 http/https 链接', re: 'https?://[^\\s]+' },
  { name: '异常堆栈', hint: 'Exception 及 at 开头的调用栈行', re: '(Exception|\\bat\\s+[\\w.$]+\\()' },
  { name: '耗时毫秒', hint: '如 123ms、1.5s 等', re: '\\d+(\\.\\d+)?\\s*(ms|s|毫秒|秒)' },
  { name: '手机号', hint: '1 开头的 11 位号码', re: '1[3-9]\\d{9}' },
  { name: '中文报错', hint: '含"错误/异常/失败/超时"的行', re: '[错误异常失败超时]' },
  { name: 'SQL 语句', hint: 'SELECT/INSERT/UPDATE/DELETE 开头', re: '^\\s*(SELECT|INSERT|UPDATE|DELETE)\\b' },
  { name: '空指针', hint: 'NullPointer / NPE', re: 'NullPointer|NPE' },
  { name: '十六进制 ID', hint: 'GUID / 32 位十六进制串', re: '[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}|[0-9a-fA-F]{32}' },
  { name: '状态码非 2xx', hint: '如 404、500、302 等响应码', re: '\\b[345]\\d{2}\\b' },
]
const CHEAT_SHEET = [
  ['\\d', '一个数字'], ['\\w', '字母/数字/下划线'], ['\\s', '空白字符'], ['.', '任意字符'],
  ['+', '前一个至少 1 次'], ['*', '前一个 0 次或多次'], ['?', '前一个 0 或 1 次'],
  ['[abc]', 'a 或 b 或 c'], ['[^a]', '除 a 以外'], ['(xx|yy)', 'xx 或 yy'],
  ['^', '行首'], ['$', '行尾'], ['{2,5}', '前一个重复 2~5 次'],
]
const showTpl = ref(false)
const showCheat = ref(false)
const tplRoot = ref<HTMLElement | null>(null)
function pickTpl(re: string) {
  regex.value = re
  showTpl.value = false
  showAdvanced.value = true
}

// ---- 查询历史（config.json 持久化，重启保留；一键重跑） ----
const HISTORY_MAX = 20
interface HistItem { label: string; spec: main.QuerySpecJSON }
const history = ref<HistItem[]>([])
const showHist = ref(false)
const histRoot = ref<HTMLElement | null>(null)

// 挂载时从持久化配置载入历史（spec 为 JSON 字符串，反序列化重跑用；坏条目跳过）
onMounted(async () => {
  try {
    const cfg = await GetConfig()
    history.value = (cfg.history ?? []).flatMap(h => {
      try {
        return [{ label: h.label, spec: JSON.parse(h.spec) as main.QuerySpecJSON }]
      } catch {
        return []
      }
    })
  } catch { /* 配置读取失败退化为会话内历史，不阻断 */ }
})

function histLabel(spec: main.QuerySpecJSON): string {
  const parts: string[] = []
  if (spec.keywords?.length) parts.push(spec.keywords.join(spec.keywordAnd ? ' + ' : ' | '))
  if (spec.regex) parts.push(`正则:${spec.regex}`)
  if (spec.excludes?.length) parts.push(`排除:${spec.excludes.join(' ')}`)
  if (spec.levels?.length) parts.push(spec.levels.map((l: number) => LEVELS[l]).join('/'))
  if (spec.tsStart || spec.tsEnd) parts.push(`${spec.tsStart.replace('T', ' ') || '…'}~${spec.tsEnd.replace('T', ' ') || '…'}`)
  return parts.join('，') || '(无条件)'
}

function runHist(h: HistItem) {
  showHist.value = false
  // 恢复条件到输入态（含「(无条件)」浏览全部的历史）
  keywords.value = (h.spec.keywords ?? []).join(' ')
  excludes.value = (h.spec.excludes ?? []).join(' ')
  regex.value = h.spec.regex ?? ''
  keywordAnd.value = h.spec.keywordAnd ?? true
  tsStart.value = h.spec.tsStart ?? ''
  tsEnd.value = h.spec.tsEnd ?? ''
  levels.value = new Set(h.spec.levels ?? [])
  showAdvanced.value = true
  emit('search', h.spec)
}

if (typeof document !== 'undefined') {
  document.addEventListener('click', (e: MouseEvent) => {
    if (showTpl.value && tplRoot.value && !tplRoot.value.contains(e.target as Node)) showTpl.value = false
    if (showHist.value && histRoot.value && !histRoot.value.contains(e.target as Node)) showHist.value = false
  })
}

// ---- 时间快捷段（基于选中文件时间范围尾部） ----
function quickRange(minutes: number | 'today') {
  let max = -Infinity
  for (const f of props.files) {
    if (!props.selected.has(f.id) || !f.hasTime) continue
    max = Math.max(max, f.tsMax)
  }
  if (max === -Infinity) return
  const toLocal = (ns: number) => {
    const d = new Date(ns / 1e6 - new Date(ns / 1e6).getTimezoneOffset() * 60000)
    return d.toISOString().slice(0, 16)
  }
  if (minutes === 'today') {
    const d = new Date(max / 1e6)
    const day = new Date(d.getFullYear(), d.getMonth(), d.getDate())
    tsStart.value = toLocal(day.getTime() * 1e6)
    tsEnd.value = ''
  } else {
    tsStart.value = toLocal(max - minutes * 60 * 1e9)
    tsEnd.value = toLocal(max)
  }
  showAdvanced.value = true
}

// 移除一个条件胶囊并立即重查（全部删光 = 回到浏览全部）
function removeChip(key: string) {
  const [kind, ...rest] = key.split(':')
  const val = rest.join(':')
  if (kind === 'kw') {
    const keep = keywordList.value.filter(k => k !== val)
    keywords.value = keep.join(' ')
    if (!keep.length) keywords.value = ''
  } else if (kind === 'ex') {
    excludes.value = excludeList.value.filter(k => k !== val).join(' ')
  } else if (kind === 're') {
    regex.value = ''
  } else if (kind === 'ts') {
    tsStart.value = ''
    tsEnd.value = ''
  } else if (kind === 'lv') {
    toggleLevel(Number(val))
  }
  search()
}

function buildSpec(): main.QuerySpecJSON {
  const spec = new main.QuerySpecJSON()
  spec.keywords = keywordList.value
  spec.excludes = excludeList.value
  spec.regex = regex.value.trim()
  spec.keywordAnd = keywordAnd.value
  spec.tsStart = tsStart.value
  spec.tsEnd = tsEnd.value
  spec.levels = [...levels.value]
  return spec
}

function search() {
  if (props.querying || regexErr.value) return
  const spec = buildSpec()
  // 记录历史（去重、置顶）并持久化到 config.json（失败不阻断查询）
  const label = histLabel(spec)
  history.value = [{ label, spec }, ...history.value.filter(h => h.label !== label)].slice(0, HISTORY_MAX)
  SaveHistory(label, JSON.stringify(spec)).catch(() => {})
  emit('search', spec)
}

// 暴露给父组件：清空重来时一并重置条件；结果表级别点击联动
function reset() {
  keywords.value = ''
  excludes.value = ''
  regex.value = ''
  regexErr.value = ''
  keywordAnd.value = true
  tsStart.value = ''
  tsEnd.value = ''
  levels.value = new Set()
  showAdvanced.value = false
}
function addLevelAndSearch(lv: number) {
  const s = new Set(levels.value)
  s.add(lv)
  levels.value = s
  showAdvanced.value = true
  search()
}
// 按最近一次查询条件重跑，供删除文件后自动刷新剩余文件的结果。
// 无历史时（如首次加载的自动全览不经 search() 未入历史）退回
// 按当前输入框条件查询（空条件 = 全览），fileIds 由 App 层按勾选重填
function rerunLast() {
  if (history.value[0]) emit('search', history.value[0].spec)
  else search()
}
defineExpose({ reset, addLevelAndSearch, rerunLast })

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter') search()
}

const progPct = computed(() => {
  if (!props.progress || !props.progress.total) return 0
  return Math.min(100, (props.progress.scanned / props.progress.total) * 100)
})
</script>

<template>
  <div class="querybar">
    <div class="main">
      <div class="hist-wrap" ref="histRoot">
        <button class="q-icon" :class="{ open: showHist }" @click="showHist = !showHist"
          title="查询历史（点击选择重跑）">🔍<span class="caret">▾</span></button>
        <div v-if="showHist" class="hist-pop">
          <div class="hist-title">查询历史（点击重跑）</div>
          <div v-if="!history.length" class="hist-empty">还没有查询记录</div>
          <button v-for="(h, i) in history" :key="i" class="hist-item" @click="runHist(h)">{{ h.label }}</button>
        </div>
      </div>
      <input
        class="q-input" type="text" v-model="keywords"
        placeholder="想找什么？留空看全部，多个关键字空格隔开（如：E502 超时）"
        :disabled="disabled"
        @keydown="onKey"
      />
      <button v-if="!querying" class="primary" :disabled="disabled || !!regexErr" @click="search">
        查一下
      </button>
      <button v-else class="stop" @click="emit('cancel')">停止</button>
      <button class="ghost adv-toggle" :class="{ open: showAdvanced }" @click="showAdvanced = !showAdvanced">
        更多条件 ▾
      </button>
    </div>

    <div v-if="showAdvanced" class="advanced">
      <div class="a-row">
        <span class="a-label">匹配</span>
        <div class="seg">
          <button :class="{ on: keywordAnd }" @click="keywordAnd = true">全部包含</button>
          <button :class="{ on: !keywordAnd }" @click="keywordAnd = false">任一包含</button>
        </div>
        <span class="a-label">排除词</span>
        <input class="a-ex" type="text" v-model="excludes" placeholder="包含这些词的行不要（空格分隔）" @keydown="onKey" />
      </div>
      <div class="a-row">
        <span class="a-label">正则</span>
        <input class="a-re" type="text" v-model="regex" :class="{ bad: regexErr }" placeholder="不会写？点右边「常用」选一个，可以再改" @keydown="onKey" />
        <div class="tpl-wrap" ref="tplRoot">
          <button class="ghost" :class="{ open: showTpl }" @click="showTpl = !showTpl">常用 ▾</button>
          <div v-if="showTpl" class="tpl-pop">
            <button v-for="t in REGEX_TEMPLATES" :key="t.name" class="tpl-item" @click="pickTpl(t.re)">
              <span class="tpl-name">{{ t.name }}</span>
              <span class="tpl-hint">{{ t.hint }}</span>
              <code class="tpl-re">{{ t.re }}</code>
            </button>
            <button class="tpl-cheat-toggle" @click="showCheat = !showCheat">
              {{ showCheat ? '收起' : '展开' }}写法速查
            </button>
            <div v-if="showCheat" class="cheat">
              <span v-for="[sym, desc] in CHEAT_SHEET" :key="sym" class="cheat-item"><code>{{ sym }}</code>{{ desc }}</span>
            </div>
          </div>
        </div>
      </div>
      <div class="a-row">
        <span class="a-label">时间</span>
        <TimePicker v-model="tsStart" placeholder="开始时间" @keydown="onKey" />
        <span class="a-sep">到</span>
        <TimePicker v-model="tsEnd" placeholder="结束时间" @keydown="onKey" />
        <span class="quick">
          <template v-if="hasFileTime">
            <button class="ghost qk" @click="quickRange(5)" title="选中文件时间范围的最后 5 分钟">最后5分</button>
            <button class="ghost qk" @click="quickRange(30)">最后30分</button>
            <button class="ghost qk" @click="quickRange(60)">最后1小时</button>
            <button class="ghost qk" @click="quickRange('today')">当天</button>
          </template>
          <button class="ghost" @click="fitFileRange" title="按选中文件的时间范围自动填充">贴合文件范围</button>
        </span>
      </div>
      <div class="a-row">
        <span class="a-label">级别</span>
        <button v-for="(name, i) in LEVELS" :key="i" class="lvl-pick" :class="{ [`sel-lvl${i}`]: levels.has(i) }" @click="toggleLevel(i)">
          {{ name }}
        </button>
      </div>
      <div v-if="regexErr" class="re-err">正则写法有误：{{ regexErr }}</div>
    </div>

    <!-- 签名交互：筛选链 —— 生效条件一目了然，点 × 移除并立即重查（删光回到全部） -->
    <div v-if="chain.length" class="chain">
      <span class="chain-label">缩小到</span>
      <span v-for="c in chain" :key="c.key" class="chain-chip">
        <span v-if="c.kind" class="chain-kind">{{ c.kind }}</span>
        <span class="chain-text" :title="c.label">{{ c.label }}</span>
        <button class="chain-x" @click="removeChip(c.key)" title="移除这个条件并重新查询">×</button>
      </span>
    </div>

    <div v-if="querying" class="qprog">
      <div class="qprog-bar"><div class="qprog-fill" :style="{ width: progPct + '%' }"></div></div>
      <span class="qprog-text">正在扫描 {{ progPct.toFixed(0) }}% · 已找到 {{ (progress?.matched ?? 0).toLocaleString() }} 条</span>
    </div>
    <div v-if="error" class="err">{{ error }}</div>
  </div>
</template>

<style scoped>
.querybar {
  flex: none;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.main {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--bg-panel);
  border: 1px solid var(--border-strong);
  border-radius: 10px;
  padding: 8px 10px;
  transition: border-color .15s;
}
.main:focus-within { border-color: var(--amber-line); }
.q-icon {
  background: transparent;
  border: none;
  color: var(--text-dim);
  font-size: 14px;
  padding: 4px 6px 4px 2px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}
.q-icon:hover { color: var(--amber); background: transparent; border: none; }
.q-icon.open { color: var(--amber); }
.caret { font-size: 9px; opacity: .8; }

/* 查询历史 */
.hist-wrap { position: relative; flex: none; }
.hist-pop {
  position: absolute; top: calc(100% + 6px); left: 0; z-index: 60;
  width: 380px; max-height: 300px; overflow-y: auto;
  background: var(--bg-float); border: 1px solid var(--border-strong);
  border-radius: 10px; box-shadow: var(--shadow-pop);
  padding: 6px;
}
.hist-title {
  color: var(--text-faint); font-size: 11px; padding: 2px 10px 6px;
  border-bottom: 1px solid var(--border); margin-bottom: 4px;
}
.q-input {
  flex: 1;
  background: transparent;
  border: none;
  padding: 6px 4px;
  font-size: 14px;
}
.q-input:focus { border: none; outline: none; }
.adv-toggle.open { color: var(--amber); }
.stop { border-color: var(--danger); color: var(--danger); }

.advanced {
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.a-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.a-label {
  color: var(--text-dim); font-size: 12px; width: 38px; flex: none;
  text-align: justify; text-align-last: justify;
}
.a-ex { flex: 1; min-width: 180px; }
.a-re { flex: 1; min-width: 220px; font-family: var(--mono); }
.a-re.bad { border-color: var(--danger); }
.a-sep { color: var(--text-dim); }
.seg { display: flex; border: 1px solid var(--border); border-radius: 6px; overflow: hidden; }
.seg button { border: none; border-radius: 0; padding: 5px 10px; font-size: 12px; color: var(--text-dim); background: transparent; }
.seg button.on { background: var(--amber-soft); color: var(--amber); font-weight: 600; }

.lvl-pick {
  padding: 4px 10px;
  font-family: var(--mono);
  font-size: 11.5px;
  color: var(--text-dim);
}
.lvl-pick.sel-lvl0 { color: var(--lvl-0-fg); border-color: var(--lvl-0-fg); background: var(--lvl-0-bg); }
.lvl-pick.sel-lvl1 { color: var(--lvl-1-fg); border-color: var(--lvl-1-fg); background: var(--lvl-1-bg); }
.lvl-pick.sel-lvl2 { color: var(--lvl-2-fg); border-color: var(--lvl-2-fg); background: var(--lvl-2-bg); }
.lvl-pick.sel-lvl3 { color: var(--lvl-3-fg); border-color: var(--lvl-3-fg); background: var(--lvl-3-bg); }
.lvl-pick.sel-lvl4 { color: var(--lvl-4-fg); border-color: var(--lvl-4-fg); background: var(--lvl-4-bg); }
.lvl-pick.sel-lvl5 { color: var(--lvl-5-fg); border-color: var(--lvl-5-fg); background: var(--lvl-5-bg); }

.re-err { color: var(--danger); font-size: 12px; }

/* 筛选链 */
.chain { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.chain-label {
  color: var(--text-faint); font-size: 12px; flex: none;
}
.chain-chip {
  display: inline-flex; align-items: center; gap: 5px;
  background: var(--amber-soft);
  border: 1px solid var(--amber-line);
  color: var(--text);
  border-radius: 20px;
  padding: 2px 4px 2px 10px;
  font-size: 12px;
  max-width: 300px;
}
.chain-kind { color: var(--amber); font-size: 11px; flex: none; }
.chain-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--mono); font-size: 11.5px; }
.chain-x {
  border: none; background: transparent; color: var(--text-dim);
  font-size: 14px; padding: 0 6px 0 2px; border-radius: 10px;
}
.chain-x:hover { color: var(--danger); background: transparent; }

.qprog { display: flex; align-items: center; gap: 10px; }
.qprog-bar { flex: 1; height: 6px; background: var(--bg-panel); border-radius: 3px; overflow: hidden; }
.qprog-fill { height: 100%; background: var(--amber); transition: width .15s; }
.qprog-text { color: var(--text-dim); font-size: 12px; flex: none; }
.err {
  color: var(--danger); font-size: 12px;
  background: var(--lvl-4-bg); border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent);
  border-radius: 6px; padding: 6px 10px;
  white-space: pre-wrap;
}

.hist-empty { color: var(--text-faint); text-align: center; padding: 14px; font-size: 12px; }
.hist-item {
  display: block; width: 100%; text-align: left;
  background: transparent; border: none; border-radius: 6px;
  padding: 6px 10px; font-size: 12px; color: var(--text);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.hist-item:hover { background: var(--bg-float-hover); border: none; }

/* 正则模板库 */
.tpl-wrap { position: relative; }
.tpl-pop {
  position: absolute; top: calc(100% + 6px); left: 0; z-index: 60;
  width: 430px; max-height: 340px; overflow-y: auto;
  background: var(--bg-float); border: 1px solid var(--border-strong);
  border-radius: 10px; box-shadow: var(--shadow-pop);
  padding: 6px;
}
.tpl-item {
  display: grid; grid-template-columns: 72px 1fr; grid-template-rows: auto auto;
  column-gap: 8px;
  width: 100%; text-align: left;
  background: transparent; border: none; border-radius: 6px;
  padding: 6px 10px; color: var(--text); align-items: baseline;
}
.tpl-item:hover { background: var(--bg-float-hover); border: none; }
.tpl-name { font-weight: 600; font-size: 12.5px; color: var(--amber); }
.tpl-hint { color: var(--text-dim); font-size: 11.5px; }
.tpl-re {
  grid-column: 2;
  font-family: var(--mono); font-size: 11px; color: var(--text-dim);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.tpl-cheat-toggle {
  width: 100%; background: transparent; border: none; border-top: 1px solid var(--border);
  border-radius: 0; color: var(--text-dim); font-size: 12px; padding: 7px;
  margin-top: 4px;
}
.tpl-cheat-toggle:hover { color: var(--amber); }
.cheat { padding: 8px 10px 4px; display: flex; flex-wrap: wrap; gap: 6px 14px; }
.cheat-item { font-size: 11.5px; color: var(--text-dim); }
.cheat-item code {
  font-family: var(--mono); color: var(--amber);
  background: var(--bg); border-radius: 3px; padding: 0 4px; margin-right: 3px;
}
.tpl-wrap > button.open { color: var(--amber); }

/* 时间快捷段 */
.quick { display: flex; gap: 4px; align-items: center; }
.qk { font-size: 11.5px; padding: 4px 8px; }
</style>
