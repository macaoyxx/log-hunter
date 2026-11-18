<script setup lang="ts">
// 日志分析专家会话面板：右缘固定侧栏列（占布局非浮层——引用跳转时结果表仍可见）。
// 签名元素是取证时间线：agent 的每次工具调用在回复前呈现为时间线节点
//（取证=accent 圆点、产出=ok 色圆点）——「结论旁就是取证过程」是证据纪律的可视化。
// 会话顶栏随时切厂商/模型续聊（LLM 无状态全量重发，历史天然延续）。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import AppIcon from './AppIcon.vue'
import { renderMd } from './aiMd'
import { conf, logscan } from '../../wailsjs/go/models'
import { AiDiagnose, AiFollowup, CancelAi, AiResolveRef, AiSessionInfo } from '../../wailsjs/go/main/App'

const props = defineProps<{
  config: conf.Conf
  statsTotal: number // 行号引用校验上限（结果集总行数）
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'jump', idx: number): void
  (e: 'open-context', fileId: number, offset: number): void
  (e: 'run-query', kw: string): void
  (e: 'open-settings'): void
}>()

// ---------- 会话消息模型 ----------
interface Step {
  kind: 'call' | 'doc' | 'vision'
  text: string   // 工具名 / 文件名 / 「读图」
  detail: string // 参数 / 结果摘要 / 转述全文
  done: boolean  // call 收到对应 result
}
interface ChatItem {
  kind: 'user' | 'assistant' | 'error'
  text: string
  reasoning: string
  images?: string[] // 图片 dataURL（展示用）
  atts?: { name: string; size: number; kind: 'image' | 'text' }[] // 附件摘要（展示用）
  steps: Step[]
  streaming: boolean
}

const items = ref<ChatItem[]>([])
const busy = ref(false)
const inputText = ref('')

// 附件：截图/图片走视觉通道，文本文件（拖入的离线日志等）随消息拼入
interface Attachment { name: string; size: number; kind: 'image' | 'text'; data: string }
const inputAtt = ref<Attachment[]>([])
const ATT_TEXT_MAX = 512 << 10 // 文本附件上限 512KB（更大的文件建议拖入文件列表用查询分析）
const filePick = ref<HTMLInputElement | null>(null)
const flow = ref<HTMLElement | null>(null)
const stickBottom = ref(true)

// ---------- 厂商 / 模型 / 思考等级选择（会话中随切续聊） ----------
const activeProfile = ref(props.config.activeProfile || '')
const activeModel = ref(props.config.activeModel || '')
const effortSel = ref(props.config.effort || '')
watch(() => props.config, c => {
  if (!activeProfile.value && c.activeProfile) { activeProfile.value = c.activeProfile }
  if (!activeModel.value && c.activeModel) { activeModel.value = c.activeModel }
  if (!effortSel.value && c.effort) { effortSel.value = c.effort }
  // 当前厂商被删：回落默认
  if (activeProfile.value && !c.profiles.some(p => p.id === activeProfile.value)) {
    activeProfile.value = c.activeProfile || ''
    activeModel.value = c.activeModel || ''
  }
})
const curProfile = computed(() => props.config.profiles.find(p => p.id === activeProfile.value) || null)
const curModels = computed(() => curProfile.value?.models ?? [])
const visionModels = computed(() => // 视觉下拉只列 vision 条目
  props.config.profiles.find(p => p.id === props.config.visionProfile)?.models.filter(m => m.vision) ?? [])
const configured = computed(() => props.config.profiles.length > 0 && curProfile.value?.baseUrl)

// ---------- 后端事件（App 层 EventsOn('ai:event') 转发进来） ----------
function curStream(): ChatItem | null {
  const arr = items.value
  for (let i = arr.length - 1; i >= 0; i--) {
    if (arr[i].kind === 'assistant' && arr[i].streaming) return arr[i]
    if (arr[i].kind === 'user') return null // 越过最近 user 还没有 assistant：等占位创建
  }
  return null
}
// 事件按 Seq 连续应用：桥不保证顺序，乱序缓存等前序到齐（后端每轮重置 Seq）；
// pending 容量防御：极端堆积时丢弃最旧（事件已大量丢失的场合顺序无从谈起）
const applySeq = ref(0)
const pendingEvts = new Map<number, { kind: string; text: string; detail: string }>()
function onEvent(e: { kind: string; seq?: number; text: string; detail: string }) {
  if (typeof e.seq !== 'number' || e.seq <= 0) { applyEvt(e); return }
  if (e.seq <= applySeq.value) return // 重复投递
  pendingEvts.set(e.seq, e)
  if (pendingEvts.size > 512) {
    const first = Math.min(...pendingEvts.keys())
    pendingEvts.delete(first)
  }
  while (pendingEvts.has(applySeq.value + 1)) {
    const nx = pendingEvts.get(applySeq.value + 1)!
    pendingEvts.delete(applySeq.value + 1)
    applySeq.value++
    applyEvt(nx)
  }
}
// 流式增量展示节流：事件按 seq 严格有序应用，但 text 写入按 50ms 攒批——
// 高频模型每秒百片时逐片触发响应式重渲是真正压力点（事件桥本身实测可承受）
const flushBuf = { text: '', reasoning: '' }
let flushTimer: number | undefined
function applyEvt(e: { kind: string; text: string; detail: string }) {
  const cur = curStream()
  if (!cur) return
  switch (e.kind) {
    case 'delta':
      flushBuf.text += e.text
      scheduleFlush(cur)
      break
    case 'reasoning':
      flushBuf.reasoning += e.text
      scheduleFlush(cur)
      break
    case 'tool_call':
      cur.steps.push({ kind: 'call', text: e.text, detail: e.detail, done: false }); break
    case 'tool_result': {
      // 配对到最近一个同名未完成调用
      for (let i = cur.steps.length - 1; i >= 0; i--) {
        if (cur.steps[i].kind === 'call' && cur.steps[i].text === e.text && !cur.steps[i].done) {
          cur.steps[i].done = true
          cur.steps[i].detail += '\n→ ' + e.detail
          return
        }
      }
      cur.steps.push({ kind: 'call', text: e.text, detail: '→ ' + e.detail, done: true })
      break
    }
    case 'doc': cur.steps.push({ kind: 'doc', text: e.text, detail: e.detail, done: true }); break
    case 'vision': cur.steps.push({ kind: 'vision', text: '视觉转述', detail: e.text, done: true }); break
  }
}
function scheduleFlush(cur: ChatItem) {
  if (flushTimer !== undefined) return
  flushTimer = window.setTimeout(() => {
    flushTimer = undefined
    if (flushBuf.text) { cur.text += flushBuf.text; flushBuf.text = '' }
    if (flushBuf.reasoning) { cur.reasoning += flushBuf.reasoning; flushBuf.reasoning = '' }
  }, 50)
}
// 外部预填追问（右键「问 AI」）：填入输入框待用户编辑/发送，并聚焦
const inputEl = ref<HTMLTextAreaElement | null>(null)
function prefill(text: string) {
  if (busy.value) return
  inputText.value = text
  nextTick(() => inputEl.value?.focus())
}
defineExpose({ onEvent, prefill })

// ---------- 动作 ----------
function pushAssistant() {
  applySeq.value = 0
  pendingEvts.clear()
  items.value.push({ kind: 'assistant', text: '', reasoning: '', steps: [], streaming: true })
  stickBottom.value = true
}
function finishStream(err?: string) {
  flushNow()
  const arr = items.value
  for (let i = arr.length - 1; i >= 0; i--) {
    if (arr[i].kind === 'assistant' && arr[i].streaming) { arr[i].streaming = false; break }
  }
  if (err) items.value.push({ kind: 'error', text: err, reasoning: '', steps: [], streaming: false })
  busy.value = false
}

// 首轮诊断（结果集基线上下文 → agent 取证循环）
function flushNow() {
  if (flushTimer !== undefined) { window.clearTimeout(flushTimer); flushTimer = undefined }
  const cur = curStream()
  if (!cur) { flushBuf.text = ''; flushBuf.reasoning = ''; return }
  if (flushBuf.text) { cur.text += flushBuf.text; flushBuf.text = '' }
  if (flushBuf.reasoning) { cur.reasoning += flushBuf.reasoning; flushBuf.reasoning = '' }
}
function diagnose() {
  if (busy.value || !configured.value || !activeModel.value) return
  items.value = []
  items.value.push({ kind: 'user', text: '诊断当前结果集', reasoning: '', steps: [], images: [], streaming: false })
  pushAssistant()
  busy.value = true
  AiDiagnose(activeProfile.value, activeModel.value, effortSel.value)
    .then(() => finishStream())
    .catch(e => finishStream(String(e)))
}
// 追问（可带图；主模型/视觉路由由后端按配置决定）
function send() {
  const text0 = inputText.value.trim()
  if (busy.value || ( !text0 && !inputAtt.value.length ) || !activeModel.value) return
  // 附件分流：图片走视觉通道；文本附件（拖入的离线日志）以分隔块拼入消息体
  const atts = inputAtt.value
  const imgs = atts.filter(a => a.kind === 'image').map(a => a.data)
  const textBlocks = atts.filter(a => a.kind === 'text')
    .map(a => `\n\n--- 附件：${a.name}（${fmtSize(a.size)}）---\n${a.data}`)
  const text = text0 + textBlocks.join('')
  items.value.push({ kind: 'user', text: text0, reasoning: '', steps: [], images: imgs,
    atts: atts.map(a => ({ name: a.name, size: a.size, kind: a.kind })), streaming: false })
  inputText.value = ''
  inputAtt.value = []
  pushAssistant()
  busy.value = true
  AiFollowup(activeProfile.value, activeModel.value, effortSel.value, text, imgs)
    .then(() => finishStream())
    .catch(e => finishStream(String(e)))
}
function stop() { CancelAi().catch(() => {}) }

// ---------- 引用 / 建议 ----------
function validRow(n: number): boolean { return n >= 1 && n <= props.statsTotal }
function jumpRow(n: number) { emit('jump', n - 1) } // 展示 1-based → 结果表 0-based
function openFileRef(file: string, line: number) {
  // 文件:行 → fileId+offset（后端按名解析）→ 开上下文面板
  AiResolveRef(file, line).then((r: logscan.SideRow) => {
    emit('open-context', r.fileId, r.offset)
  }).catch((e: unknown) => {
    items.value.push({ kind: 'error', text: `引用无法定位（${file}:${line}）：${e}`, reasoning: '', steps: [], streaming: false })
  })
}
// ---------- 会话恢复：重开面板从后端 msgs 快照重建（关闭面板不再丢历史） ----------
function rebuildFromMsgs(msgs: Array<{ role: string; content: unknown; name?: string; toolCalls?: Array<{ id: string; function: { name: string; arguments: string } }> }>) {
  if (!msgs?.length) return
  const out: ChatItem[] = []
  for (const m of msgs) {
    if (m.role === 'system') continue
    if (m.role === 'user') {
      const text = typeof m.content === 'string' ? m.content : ''
      // 收敛提醒不展示：按后端结构化标记 name=sys-hint 过滤（前缀兜底兼容修复前注入的存量会话）
      if (m.name === 'sys-hint' || text.startsWith('（系统提示：')) continue
      if (typeof m.content === 'string') {
        out.push({ kind: 'user', text, reasoning: '', steps: [], streaming: false })
      } else if (Array.isArray(m.content)) {
        // 多模态：文本段 + 图片段（附件消息的文本块含「--- 附件：」标记）
        const parts = m.content as Array<{ type?: string; text?: string; image_url?: { url?: string } }>
        const texts = parts.filter(x => x.type === 'text').map(x => x.text ?? '')
        const imgs = parts.filter(x => x.type === 'image_url').map(x => x.image_url?.url ?? '')
        out.push({ kind: 'user', text: texts.join('\n'), reasoning: '', steps: [], images: imgs, streaming: false })
      }
      continue
    }
    if (m.role === 'assistant') {
      // agent 多步历史：带 tool_calls 的中间步并入 steps；正文步合并到最后一张卡
      const text = typeof m.content === 'string' ? m.content : ''
      if (m.toolCalls?.length) {
        const last = out[out.length - 1]
        const steps = last && last.kind === 'assistant' ? last.steps : []
        if (last && last.kind === 'assistant') {
          for (const tc of m.toolCalls) steps.push({ kind: 'call', text: tc.function.name, detail: tc.function.arguments, done: true })
        } else {
          out.push({ kind: 'assistant', text: '', reasoning: '', steps: m.toolCalls.map(tc => ({ kind: 'call' as const, text: tc.function.name, detail: tc.function.arguments, done: true })), streaming: false })
        }
        continue
      }
      if (text) {
        const last = out[out.length - 1]
        if (last && last.kind === 'assistant') last.text += (last.text ? '\n\n' : '') + text
        else out.push({ kind: 'assistant', text, reasoning: '', steps: [], streaming: false })
      }
      continue
    }
    if (m.role === 'tool') {
      const last = out[out.length - 1]
      // tool 结果配对到最近同名 call 步骤
      if (last && last.kind === 'assistant') {
        for (let i = last.steps.length - 1; i >= 0; i--) {
          const st = last.steps[i]
          if (st.kind === 'call' && st.text === (m as { name?: string }).name && !st.detail.includes('→')) {
            st.detail += '\n→ ' + String(m.content).slice(0, 300)
            break
          }
        }
      }
    }
  }
  if (out.length) items.value = out
}
onMounted(() => {
  AiSessionInfo().then((info: { msgs?: unknown }) => {
    rebuildFromMsgs((info.msgs as never) ?? [])
  }).catch(() => {})
})
onUnmounted(() => { if (flushTimer !== undefined) window.clearTimeout(flushTimer) })

const mdHandlers = {
  onRowRef: jumpRow,
  onFileRef: openFileRef,
  onQuery: (kw: string) => emit('run-query', kw),
  validRow,
}

// ---------- 贴图 / 拖入附件 ----------
function pickImages() { filePick.value?.click() }
// Ctrl+V 粘贴截图：剪贴板图片直接进附件（系统截图/微信截图后粘贴的常规动线）
function onPaste(e: ClipboardEvent) {
  for (const it of Array.from(e.clipboardData?.items ?? [])) {
    if (it.type.startsWith('image/')) {
      const f = it.getAsFile()
      if (!f) continue
      e.preventDefault()
      readFile(f)
    }
  }
}
// 拖入附件：图片走视觉；文本类（日志/配置）读内容随消息分析——离线日志一起排查的入口
function onDrop(e: DragEvent) {
  for (const f of Array.from(e.dataTransfer?.files ?? [])) readFile(f)
}
const TEXT_EXT = /\.(log|txt|json|xml|md|csv|ya?ml|sql|stack|trace|out)$/i
function readFile(f: File) {
  if (f.size > ATT_TEXT_MAX && !f.type.startsWith('image/')) {
    items.value.push({ kind: 'error', text: `附件「${f.name}」超过 ${fmtSize(ATT_TEXT_MAX)} 上限（当前 ${fmtSize(f.size)}），未添加——超大日志建议拖入文件列表查询`, reasoning: '', steps: [], streaming: false })
    return
  }
  if (f.size > (5 << 20)) {
    items.value.push({ kind: 'error', text: `图片「${f.name}」超过 5MB 上限，未添加`, reasoning: '', steps: [], streaming: false })
    return
  }
  const rd = new FileReader()
  if (f.type.startsWith('image/')) {
    rd.onload = () => {
      if (typeof rd.result === 'string') inputAtt.value.push({ name: f.name, size: f.size, kind: 'image', data: rd.result })
    }
    rd.readAsDataURL(f)
  } else if (f.type.startsWith('text/') || TEXT_EXT.test(f.name) || !f.type) {
    rd.onload = () => {
      if (typeof rd.result === 'string') inputAtt.value.push({ name: f.name, size: f.size, kind: 'text', data: rd.result })
    }
    rd.readAsText(f)
  } else {
    items.value.push({ kind: 'error', text: `不支持的附件类型：「${f.name}」（${f.type || '未知类型'}）——支持图片与文本类文件`, reasoning: '', steps: [], streaming: false })
  }
}
function removeAtt(i: number) { inputAtt.value.splice(i, 1) }
function fmtSize(n: number): string {
  return n >= 1024 ? (n / 1024).toFixed(n >= 10240 ? 0 : 1) + 'KB' : n + 'B'
}
function onPicked(e: Event) {
  const inp = e.target as HTMLInputElement
  if (!inp.files) return
  for (const f of Array.from(inp.files)) readFile(f)
  inp.value = ''
}


// ---------- 滚动跟随 ----------
function onFlowScroll() {
  const el = flow.value
  if (!el) return
  stickBottom.value = el.scrollTop + el.clientHeight >= el.scrollHeight - 48
}
async function scrollBottom() {
  if (!stickBottom.value) return
  await nextTick()
  const el = flow.value
  if (el) el.scrollTop = el.scrollHeight
}
watch(() => items.value[items.value.length - 1]?.text, scrollBottom)
watch(() => items.value.length, scrollBottom)

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
}
function fmtCtx(n: number): string {
  return n >= 1000 ? (n / 1000).toFixed(n >= 10000 ? 0 : 1) + 'K' : String(n)
}
const ctxNote = computed(() => { // 上下文规模提示（模型级 ctx × 70%）
  const m = curModels.value.find(x => x.name === activeModel.value)
  const ctx = m?.ctx || props.config.ctxTokens || 1048576
  return `上下文预算 ≈ ${fmtCtx(Math.round(ctx * 0.7))} tok`
})
</script>

<template>
  <div class="ai-col">
    <!-- 顶栏：厂商 + 模型随切（切模型=换参重发，历史延续） -->
    <div class="ai-head">
      <span class="ai-title"><AppIcon name="sparkles" :size="13" />日志分析专家</span>
      <span class="spacer"></span>
      <button v-if="configured" class="ai-redo" :disabled="busy || !statsTotal" title="基于当前结果集重新诊断"
        @click="diagnose"><AppIcon name="crosshair" :size="12" />重新诊断</button>
      <button class="ai-x" title="关闭" @click="emit('close')"><AppIcon name="x" :size="12" /></button>
      <template v-if="configured">
        <div class="ai-pickers">
          <select v-model="activeProfile" class="ai-sel" title="模型厂商">
            <option v-for="p in config.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
          <select v-model="effortSel" class="ai-sel ai-effort" title="思考等级（随时切换，下次请求生效；默认不发该字段）">
            <option value="">思考·默认</option>
            <option value="low">思考·低</option>
            <option value="medium">思考·中</option>
            <option value="high">思考·高</option>
          </select>
          <select v-model="activeModel" class="ai-sel ai-model" :title="ctxNote">
            <option v-if="activeModel && !curModels.some(m => m.name === activeModel)" :value="activeModel">{{ activeModel }}（手填）</option>
            <option v-if="!curModels.length && !activeModel" value="">（未拉取模型列表）</option>
            <option v-for="m in curModels" :key="m.name" :value="m.name">
              {{ m.name }}{{ m.vision ? ' ◉' : '' }}
            </option>
          </select>
        </div>
      </template>
    </div>

    <!-- 消息流 -->
    <div ref="flow" class="ai-flow" @scroll="onFlowScroll">
      <!-- 未配置引导 -->
      <div v-if="!items.length && !configured" class="ai-empty">
        <AppIcon name="sparkles" :size="22" />
        <div class="t">AI 诊断未配置</div>
        <div class="d">配置 OpenAI 兼容模型厂商（DeepSeek / GLM / Ollama / 自建网关均可），
          主模型与视觉模型可选不同厂商。</div>
        <button class="primary" @click="emit('open-settings')">去配置</button>
      </div>
      <div v-else-if="!items.length" class="ai-empty">
        <AppIcon name="crosshair" :size="22" />
        <div class="t">就绪</div>
        <div class="d">从错误聚类面板点「AI 诊断」，或在此直接「重新诊断」——
          专家会基于聚类摘要自主取证（搜索/读上下文/看时间分布），给出带行号引用的根因假设。</div>
        <button class="primary" :disabled="!statsTotal" @click="diagnose">
          <AppIcon name="sparkles" :size="12" />诊断当前结果
        </button>
      </div>

      <template v-for="(it, ii) in items" :key="ii">
        <!-- 用户消息 -->
        <div v-if="it.kind === 'user'" class="msg-user">
          <div class="u-bubble">
            <div class="u-text">{{ it.text }}</div>
            <div v-if="it.atts?.length" class="u-atts">
              <span v-for="(a, gi) in it.atts" :key="gi" class="u-att">
                <AppIcon :name="a.kind === 'image' ? 'image' : 'file-text'" :size="10" />{{ a.name }}（{{ fmtSize(a.size) }}）
              </span>
            </div>
            <div v-if="it.images?.length" class="u-imgs">
              <img v-for="(img, gi) in it.images" :key="gi" :src="img" alt="贴图" />
            </div>
          </div>
        </div>
        <!-- 错误 -->
        <div v-else-if="it.kind === 'error'" class="msg-err" :title="it.text">{{ it.text }}</div>
        <!-- 专家回复 -->
        <div v-else class="msg-ai">
          <!-- 思考过程（折叠；流式时展开态随收到的增量自动滚） -->
          <details v-if="it.reasoning" class="ai-reasoning" open>
            <summary>思考中…</summary>
            <pre>{{ it.reasoning }}</pre>
          </details>
          <!-- 取证时间线：结论前的过程，证据纪律的可视化 -->
          <div v-if="it.steps.length" class="ai-steps">
            <div v-for="(st, si) in it.steps" :key="si" class="step" :class="'step-' + st.kind">
              <i class="dot"></i>
              <div class="st-body">
                <div class="st-line">
                  <AppIcon v-if="st.kind === 'call'" name="terminal" :size="10" />
                  <AppIcon v-else-if="st.kind === 'doc'" name="file-text" :size="10" />
                  <AppIcon v-else name="image" :size="10" />
                  <span class="st-name">{{ st.text }}</span>
                  <span v-if="st.kind === 'call' && !st.done" class="st-run">取证中…</span>
                  <span v-else class="st-ok">✓</span>
                </div>
                <details v-if="st.detail" class="st-detail">
                  <summary>详情</summary>
                  <pre>{{ st.detail }}</pre>
                </details>
              </div>
            </div>
          </div>
          <!-- 回复正文（markdown + 引用 chip + 查询建议） -->
          <div v-if="it.text" class="ai-md">
            <component :is="() => renderMd(it.text, mdHandlers)" />
          </div>
          <div v-else-if="!it.steps.length && it.streaming" class="ai-wait">正在分析<span class="dots">…</span></div>
          <span v-if="it.streaming && it.text" class="cursor"></span>
        </div>
      </template>
    </div>

    <!-- 输入区 -->
    <div class="ai-input" v-if="configured" @dragover.prevent @drop.prevent="onDrop">
      <div v-if="inputAtt.length" class="in-imgs">
        <div v-for="(a, gi) in inputAtt" :key="gi" class="in-img">
          <img v-if="a.kind === 'image'" :src="a.data" alt="待发送" />
          <div v-else class="in-file" :title="a.name + '（' + fmtSize(a.size) + '）'">
            <AppIcon name="file-text" :size="14" />
            <span class="in-file-n">{{ a.name }}</span>
            <span class="in-file-s">{{ fmtSize(a.size) }}</span>
          </div>
          <button class="in-img-x" title="移除" @click="removeAtt(gi)"><AppIcon name="x" :size="9" /></button>
        </div>
      </div>
      <div class="in-row">
        <button class="in-tool" title="贴图（架构图/监控截图；纯文本模型将经视觉厂商转述）"
          :disabled="busy" @click="pickImages"><AppIcon name="image" :size="14" /></button>
        <input ref="filePick" type="file" accept="image/*" multiple hidden @change="onPicked" />
        <textarea ref="inputEl" v-model="inputText" class="in-text" rows="2"
          placeholder="追问（Enter 发送 · Shift+Enter 换行 · Ctrl+V 粘贴截图 · 可拖入日志文件）"
          :disabled="!activeModel" @keydown="onKey" @paste="onPaste"></textarea>
        <button v-if="!busy" class="in-send" :disabled="(!inputText.trim() && !inputAtt.length) || !activeModel" title="发送" @click="send">
          <AppIcon name="send" :size="13" />
        </button>
        <button v-else class="in-stop" title="停止" @click="stop"><AppIcon name="square" :size="12" /></button>
      </div>
      <div class="in-note">{{ ctxNote }} · 引用 [行 N] 可点回结果表核验 · 产出文档见取证时间线</div>
    </div>
  </div>
</template>

<style scoped>
.ai-col {
  flex: none;
  width: clamp(360px, 34vw, 560px);
  display: flex;
  flex-direction: column;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: var(--sheen);
  overflow: hidden;
  min-height: 0;
}
.ai-head {
  flex: none;
  display: flex;
  flex-wrap: wrap; /* 窄面板：标题/按钮/下拉放不下时换行，不再挤压裁切 */
  align-items: center;
  gap: 8px;
  padding: 7px 10px 7px 12px;
  border-bottom: 1px solid var(--border);
}
.ai-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  white-space: nowrap;
  user-select: none;
}
.ai-title :deep(svg) { color: var(--accent-strong); }
.spacer { flex: 1; }
.ai-redo {
  flex: none;
  font-size: 11px;
  padding: 3px 8px;
  color: var(--text-dim);
  gap: 4px;
}
.ai-redo:hover:not(:disabled) { color: var(--accent-strong); border-color: var(--accent-line); }
.ai-x {
  flex: none;
  background: none;
  border: none;
  color: var(--text-faint);
  padding: 3px;
}
.ai-x:hover { color: var(--text); border: none; background: var(--bg-hover); }
.ai-pickers {
  display: flex;
  gap: 6px;
  width: 100%;
  padding-top: 6px;
  flex-wrap: wrap;
}
.ai-sel {
  /* basis 0 + min-width 0：native select 的固有宽（最长 option）不再撑破容器——
     窄面板下曾被内容顶出边界、箭头与边框被裁 */
  flex: 1 1 0;
  min-width: 0;
  width: 0;
  font-size: 11px;
  padding: 3px 6px;
  font-family: var(--mono);
  background: var(--bg-elev);
}
.ai-model { flex: 1.6 1 0; }
.ai-effort { flex: 0 0 auto; width: 84px; }

.ai-flow {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
/* 引导态 */
.ai-empty {
  margin: auto;
  max-width: 260px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  text-align: center;
  color: var(--text-faint);
  padding: 20px 0;
}
.ai-empty :deep(svg) { color: var(--accent-line); }
.ai-empty .t { font-size: 13px; font-weight: 600; color: var(--text-dim); }
.ai-empty .d { font-size: 11.5px; line-height: 1.6; }

/* 附件预览（图片缩略 + 文本文件 chip） */
.in-file {
  width: 100%; height: 100%;
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 2px; padding: 4px; background: var(--bg-elev);
  border: 1px dashed var(--border-strong); border-radius: 6px; overflow: hidden;
}
.in-file-n { font-size: 9px; color: var(--text-dim); max-width: 100%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.in-file-s { font-size: 8.5px; color: var(--text-faint); font-family: var(--mono); }
.in-file :deep(svg) { color: var(--text-faint); }
/* 用户消息附件摘要 */
.u-atts { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 5px; }
.u-att {
  display: inline-flex; align-items: center; gap: 4px;
  font-size: 9.5px; font-family: var(--mono); color: var(--text-dim);
  background: var(--bg-elev); border: 1px solid var(--border);
  border-radius: 4px; padding: 1px 6px; max-width: 180px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.u-att :deep(svg) { color: var(--text-faint); flex: none; }

/* 用户消息 */
.msg-user { display: flex; justify-content: flex-end; }
.u-bubble {
  max-width: 86%;
  background: var(--accent-soft);
  border: 1px solid var(--accent-line);
  border-radius: var(--radius-sm);
  padding: 6px 10px;
}
.u-text { font-size: 12px; color: var(--text); white-space: pre-wrap; word-break: break-word; }
.u-imgs { display: flex; gap: 6px; margin-top: 6px; flex-wrap: wrap; }
.u-imgs img { width: 76px; height: 76px; object-fit: cover; border-radius: 6px; border: 1px solid var(--border-strong); }

.msg-err {
  font-size: 11.5px;
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--danger) 35%, transparent);
  border-radius: var(--radius-sm);
  padding: 6px 10px;
  white-space: pre-wrap;
  word-break: break-all;
}

/* 专家回复 */
.msg-ai { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.ai-reasoning {
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius-sm);
  padding: 4px 8px;
  font-size: 11px;
  color: var(--text-faint);
}
.ai-reasoning summary { cursor: pointer; user-select: none; font-size: 10.5px; }
.ai-reasoning pre {
  margin-top: 4px;
  font-family: var(--mono);
  font-size: 10.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 180px;
  overflow-y: auto;
}

/* 取证时间线：左竖线 + 节点圆点（取证 accent / 产出 ok / 视觉 indigo 走 accent-soft） */
.ai-steps {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding-left: 2px;
}
.step {
  position: relative;
  display: flex;
  gap: 8px;
  padding: 2px 0 2px 0;
}
.step::before { /* 时间线竖线 */
  content: '';
  position: absolute;
  left: 4px;
  top: 14px;
  bottom: -4px;
  width: 1px;
  background: var(--border);
}
.step:last-child::before { display: none; }
.step .dot {
  flex: none;
  width: 9px;
  height: 9px;
  margin-top: 4px;
  border-radius: 50%;
  z-index: 1;
}
.step-call .dot { background: var(--accent-strong); }
.step-doc .dot { background: var(--ok); }
.step-vision .dot { background: var(--lvl-2-fg); }
.st-body { flex: 1; min-width: 0; }
.st-line {
  display: flex;
  align-items: center;
  gap: 5px;
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--text-dim);
}
.st-line :deep(svg) { color: var(--text-faint); }
.step-doc .st-line { color: var(--ok); }
.step-doc .st-line :deep(svg) { color: var(--ok); }
.st-name { font-weight: 600; }
.st-run { color: var(--accent-strong); }
.st-ok { color: var(--ok); font-size: 10px; }
.st-detail { font-size: 10.5px; }
.st-detail summary { cursor: pointer; color: var(--text-faint); font-size: 10px; user-select: none; }
.st-detail pre {
  margin-top: 2px;
  font-family: var(--mono);
  font-size: 10px;
  line-height: 1.5;
  color: var(--text-faint);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 140px;
  overflow-y: auto;
}

/* markdown 正文与 chip（aiMd.ts 生成的类，经 :deep 落样式） */
.ai-md { font-size: 12px; line-height: 1.65; color: var(--text); min-width: 0; }
.ai-md :deep(.md-h) {
  font-size: 12.5px;
  font-weight: 600;
  margin: 8px 0 4px;
  color: var(--text);
}
.ai-md :deep(.md-h:first-child) { margin-top: 0; }
.ai-md :deep(.md-p) { margin: 4px 0; white-space: pre-wrap; word-break: break-word; }
.ai-md :deep(.md-ul) { margin: 4px 0; padding-left: 18px; }
.ai-md :deep(.md-ul li) { margin: 2px 0; }
.ai-md :deep(.md-code) {
  font-family: var(--mono);
  font-size: 11px;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0 4px;
}
.ai-md :deep(.md-pre-wrap) { position: relative; margin: 6px 0; }
.ai-md :deep(.md-pre-lang) {
  position: absolute; top: 4px; right: 8px; z-index: 1;
  font-family: var(--mono); font-size: 9px; color: var(--text-faint);
  letter-spacing: .05em; text-transform: uppercase; user-select: none;
}
.ai-md :deep(.md-table) {
  border-collapse: collapse; margin: 6px 0; font-size: 11px; width: 100%;
}
.ai-md :deep(.md-table th), .ai-md :deep(.md-table td) {
  border: 1px solid var(--border); padding: 3px 8px; text-align: left; vertical-align: top;
}
.ai-md :deep(.md-table th) { background: var(--bg-elev); color: var(--text-dim); font-weight: 600; }
.ai-md :deep(.md-ol) { margin: 4px 0; padding-left: 20px; }
.ai-md :deep(.md-ol li) { margin: 2px 0; }
.ai-md :deep(.md-quote) {
  margin: 6px 0; padding: 4px 10px; color: var(--text-dim);
  border-left: 2px solid var(--accent-line); background: var(--accent-soft);
  border-radius: 0 4px 4px 0;
}
.ai-md :deep(.md-link) { color: var(--accent-strong); text-decoration: underline; }
.ai-md :deep(.md-hr) { border: none; border-top: 1px solid var(--border); margin: 8px 0; }
.ai-md :deep(.md-del) { color: var(--text-faint); }
.ai-md :deep(.md-pre) {
  font-family: var(--mono);
  font-size: 11px;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 8px 10px;
  margin: 6px 0;
  overflow-x: auto;
  white-space: pre;
}
.ai-md :deep(.md-ref) {
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--accent-strong);
  background: var(--accent-soft);
  border: 1px solid var(--accent-line);
  border-radius: 999px;
  padding: 0 7px;
  margin: 0 1px;
  line-height: 17px;
  vertical-align: baseline;
}
.ai-md :deep(.md-ref:hover) { background: var(--accent-active); }
.ai-md :deep(.md-ref-bad) {
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 10%, transparent);
  border-color: color-mix(in srgb, var(--danger) 40%, transparent);
  cursor: not-allowed;
  text-decoration: line-through;
}
.ai-md :deep(.md-q) {
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--text);
  background: var(--bg-elev);
  border: 1px solid var(--border-strong);
  border-radius: 999px;
  padding: 0 8px;
  margin: 0 1px;
  line-height: 17px;
}
.ai-md :deep(.md-q:hover) { border-color: var(--accent-line); color: var(--accent-strong); }

.ai-wait { font-size: 11.5px; color: var(--text-faint); }
.dots { animation: dots 1.2s steps(3) infinite; }
@keyframes dots { 0% { content: ''; } 50% { content: '···'; } 100% { content: '…'; } }
.cursor {
  display: inline-block;
  width: 7px;
  height: 13px;
  background: var(--accent-strong);
  animation: blink 1s steps(2) infinite;
  vertical-align: text-bottom;
}
@keyframes blink { 50% { opacity: 0; } }

/* 输入区 */
.ai-input {
  flex: none;
  border-top: 1px solid var(--border);
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.in-imgs { display: flex; gap: 6px; flex-wrap: wrap; }
.in-img { position: relative; width: 56px; height: 56px; }
.in-img img { width: 100%; height: 100%; object-fit: cover; border-radius: 6px; border: 1px solid var(--border-strong); }
.in-img-x {
  position: absolute;
  top: -5px;
  right: -5px;
  width: 16px;
  height: 16px;
  padding: 0;
  border-radius: 50%;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  color: var(--text-dim);
  justify-content: center;
}
.in-img-x:hover { color: var(--danger); border-color: var(--danger); }
.in-row { display: flex; align-items: flex-end; gap: 6px; }
.in-tool {
  flex: none;
  width: 32px;
  height: 32px;
  padding: 0;
  justify-content: center;
  color: var(--text-dim);
}
.in-tool:hover:not(:disabled) { color: var(--accent-strong); border-color: var(--accent-line); }
.in-text {
  flex: 1;
  resize: none;
  font-family: var(--ui);
  font-size: 12px;
  line-height: 1.5;
  padding: 6px 10px;
  background: var(--bg-panel);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  outline: none;
  transition: border-color .12s, box-shadow .12s;
}
.in-text:focus { border-color: var(--accent-line); box-shadow: 0 0 0 3px var(--accent-soft); }
.in-send {
  flex: none;
  width: 32px;
  height: 32px;
  padding: 0;
  justify-content: center;
  background: var(--accent-strong);
  border-color: var(--accent-strong);
  color: var(--accent-ink);
}
.in-send:hover:not(:disabled) { background: var(--accent); border-color: var(--accent); }
.in-send:disabled { opacity: .35; }
.in-stop {
  flex: none;
  width: 32px;
  height: 32px;
  padding: 0;
  justify-content: center;
  color: var(--danger);
  border-color: color-mix(in srgb, var(--danger) 45%, transparent);
}
.in-note {
  font-family: var(--mono);
  font-size: 9.5px;
  color: var(--text-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
