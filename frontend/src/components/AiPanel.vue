<script setup lang="ts">
// 日志分析专家会话面板：右缘固定侧栏列（占布局非浮层——引用跳转时结果表仍可见）。
// 签名元素是取证时间线：agent 的每次工具调用在回复前呈现为时间线节点
//（取证=accent 圆点、产出=ok 色圆点）——「结论旁就是取证过程」是证据纪律的可视化。
// 会话顶栏随时切厂商/模型续聊（LLM 无状态全量重发，历史天然延续）。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import AppIcon from './AppIcon.vue'
import { renderMd } from './aiMd'
import { conf, logscan } from '../../wailsjs/go/models'
import { AiDiagnose, AiFollowup, AiResetSession, CancelAi, AiAuthRespond, AiResolveRef, AiSessionInfo } from '../../wailsjs/go/main/App'

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
  atts?: { name: string; size: number; kind: 'image' | 'text' | 'path' }[] // 附件摘要（展示用）
  auth?: { id: string; path: string; reason: string; sensitive: boolean; done: string } // 授权卡（done: '' | 允许并记住 | 仅本次 | 已拒绝/超时）
  steps: Step[]
  stepsOpen?: boolean // 取证时间线展开态；缺省收起（一行摘要，点击展开）
  rOpen?: boolean // 思考块展开态：流式期间展开跟随，轮结束自动收起（用户可再展开）
  streaming: boolean
}

const items = ref<ChatItem[]>([])
const busy = ref(false)
// busy 持续时间（秒）——等待状态可视化：不只光标，动态图标 + 已持续秒数
const busySec = ref(0)
// 阶段感知（真实进度）：工具按 FIFO 执行，「最后一个未完成的调用」= 正在执行的工具；
// 正文 delta 在流 = 输出报告中；两者皆无 = 模型思考中。曾显示「第 N 步」循环号——
// 与「取证 N 步」（工具调用次数）单位不同还撞名，并行批量后必然对不上，已删
const outputting = ref(false)
const activeToolName = computed(() => {
  const c = curStream()
  if (!c) return ''
  for (let i = c.steps.length - 1; i >= 0; i--) {
    const s = c.steps[i]
    if (s.kind === 'call' && !s.done) return s.text
  }
  return ''
})
let busyTimer: number | undefined
function startBusyTimer() {
  busySec.value = 0
  window.clearInterval(busyTimer)
  busyTimer = window.setInterval(() => { busySec.value++ }, 1000)
}
// x分x秒；达到小时显示 x时x分x秒
function fmtElapsed(sec: number): string {
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return h + '时' + m + '分' + s + '秒'
  return m + '分' + s + '秒' // 最小单位分钟秒（0分5秒）
}
function stopBusyTimer() {
  window.clearInterval(busyTimer)
  busyTimer = undefined
}
const inputText = ref('')

// 附件：截图/图片走视觉通道，文本文件（拖入的离线日志等）随消息拼入
interface Attachment { name: string; size: number; kind: 'image' | 'text' | 'path'; data: string }
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
    // 授权卡（kind:'user' 但带 auth）不是对话轮——须跳过继续向前找流式消息；
    // 曾在此返回 null：授权卡弹出后所有事件（已授权/清单/续跑输出）被静默丢弃，
    // 表现为「点击授权后一直转圈零输出」
    if (arr[i].kind === 'user' && !arr[i].auth) return null // 越过最近 user 还没有 assistant：等占位创建
  }
  return null
}
// 事件按 Seq 连续应用：桥不保证顺序，乱序缓存等前序到齐（后端每轮重置 Seq）；
// 跳洞恢复：短暂等待后仍缺号则跳过空洞按升序应用——桥丢事件时严禁永久冻结
//（实测：点授权后后端已续跑，一个事件丢失让后续全部堆在缓冲区、UI 零输出）
const applySeq = ref(0)
const pendingEvts = new Map<number, { kind: string; text: string; detail: string }>()
let gapTimer: number | undefined
function onEvent(e: { kind: string; seq?: number; text: string; detail: string }) {
  if (typeof e.seq !== 'number' || e.seq <= 0) { applyEvt(e); return }
  if (e.seq <= applySeq.value) return // 重复投递
  pendingEvts.set(e.seq, e)
  if (pendingEvts.size > 512) {
    const first = Math.min(...pendingEvts.keys())
    pendingEvts.delete(first)
  }
  drainPending()
}
function drainPending() {
  while (pendingEvts.has(applySeq.value + 1)) {
    const nx = pendingEvts.get(applySeq.value + 1)!
    pendingEvts.delete(applySeq.value + 1)
    applySeq.value++
    applyEvt(nx)
  }
  if (!pendingEvts.size) { clearGapTimer(); return }
  // 有洞：给乱序补投 400ms 余地，仍缺即跳洞（丢一片的代价是几个字符，冻结不可接受）
  if (gapTimer === undefined) {
    gapTimer = window.setTimeout(() => {
      gapTimer = undefined
      flushPendingSkipHoles()
    }, 400)
  }
}
function clearGapTimer() {
  if (gapTimer !== undefined) { window.clearTimeout(gapTimer); gapTimer = undefined }
}
// 跳洞应用缓冲区全部事件（轮收尾兜底也走这里：尾部事件不能卡在缓冲区里丢展示）
function flushPendingSkipHoles() {
  const seqs = Array.from(pendingEvts.keys()).sort((a, b) => a - b)
  for (const s of seqs) {
    if (s <= applySeq.value) { pendingEvts.delete(s); continue }
    const ev = pendingEvts.get(s)!
    pendingEvts.delete(s)
    applySeq.value = s
    applyEvt(ev)
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
      outputting.value = true // 正文在流（流式文字自证，不显示阶段标签）
      flushBuf.text += e.text
      scheduleFlush(cur)
      break
    case 'reasoning':
      outputting.value = false // 思考流/工具执行到达即证明不在输出正文——跨步残留会让「模型思考中」失显
      flushBuf.reasoning += e.text
      scheduleFlush(cur)
      break
    case 'tool_call':
      outputting.value = false
      cur.steps.push({ kind: 'call', text: e.text, detail: e.detail, done: false }); break
    case 'tool_result': {
      // FIFO 配对：工具按调用序执行、结果按序到达（并行批量同名调用是常态——
      // 曾按「最近同名」配对，结果全堆到最后一张卡，前面的卡永远转圈）。
      // 先配最早已发起未完成的同名步骤；全部已完成则为进度详情，累进最近同名卡
      let idx = -1
      for (let i = 0; i < cur.steps.length; i++) {
        const st = cur.steps[i]
        if (st.kind === 'call' && st.text === e.text && !st.done) { idx = i; break }
      }
      if (idx < 0) {
        for (let i = cur.steps.length - 1; i >= 0; i--) {
          if (cur.steps[i].kind === 'call' && cur.steps[i].text === e.text) { idx = i; break }
        }
      }
      if (idx >= 0) {
        cur.steps[idx].done = true
        cur.steps[idx].detail += '\n→ ' + e.detail
      } else {
        cur.steps.push({ kind: 'call', text: e.text, detail: '→ ' + e.detail, done: true })
      }
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
// 外部预填追问（右键「问 AI」）：无条件填入输入框待用户编辑/发送，并聚焦。
// 曾在会话进行中直接丢弃（busy 守卫）——内容带不过来只能停止后再点；
// 输入框在会话中本就可编辑，发送环节自会挡住 busy（此时按钮是「停止」）
const inputEl = ref<HTMLTextAreaElement | null>(null)
function prefill(text: string) {
  inputText.value = text
  nextTick(() => inputEl.value?.focus())
}
// defineExpose 已在 addPathAtt 处统一声明（onEvent/prefill/addPathAtt/showAuthCard）

// ---------- 动作 ----------
function pushAssistant() {
  applySeq.value = 0
  pendingEvts.clear()
  clearGapTimer()
  outputting.value = false
  items.value.push({ kind: 'assistant', text: '', reasoning: '', steps: [], rOpen: true, streaming: true })
  stickBottom.value = true
}
function finishStream(err?: string) {
  flushNow()
  // 收尾兜底：缓冲区若还有未应用事件（洞后 400ms 未到即轮结束），跳洞冲进当前消息
  flushPendingSkipHoles()
  clearGapTimer()
  stopBusyTimer()
  outputting.value = false
  const arr = items.value
  for (let i = arr.length - 1; i >= 0; i--) {
    if (arr[i].kind === 'assistant' && arr[i].streaming) {
      arr[i].streaming = false
      arr[i].rOpen = false // 思考块收起（标题同转「思考过程」）——报告是主角，过程让位
      break
    }
  }
  // 阻塞授权下正常结束时不应有未决卡；仍有=本轮被停止/超时中断，标失效防可点残卡
  for (const it of arr) {
    if (it.auth && !it.auth.done) it.auth.done = '已失效（本轮已结束）'
    // 步骤兜底：结果事件丢失/中断时未完成步骤不再永远转圈（含并行批量调用的错配容错）
    if (it.steps) {
      for (const st of it.steps) {
        if (st.kind === 'call' && !st.done) st.done = true
      }
    }
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
  startBusyTimer()
  AiDiagnose(activeProfile.value, activeModel.value, effortSel.value)
    .then(() => finishStream())
    .catch(e => finishStream(String(e).includes('context canceled') ? undefined : String(e)))
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
  const pathBlocks = atts.filter(a => a.kind === 'path')
    .map(a => `\n\n--- 路径附件：${a.data} ---\n（可用 read_source 工具读取该路径，需用户授权；目录会返回一层清单）`)
  const text = text0 + textBlocks.join('') + pathBlocks.join('')
  items.value.push({ kind: 'user', text: text0, reasoning: '', steps: [], images: imgs,
    atts: atts.map(a => ({ name: a.name, size: a.size, kind: a.kind })), streaming: false })
  inputText.value = ''
  inputAtt.value = []
  pushAssistant()
  busy.value = true
  startBusyTimer()
  AiFollowup(activeProfile.value, activeModel.value, effortSel.value, text, imgs)
    .then(() => finishStream())
    .catch(e => finishStream(String(e).includes('context canceled') ? undefined : String(e)))
}
function stop() { CancelAi().catch(() => {}) }
// 新会话：两段确认防误触（当前会话将不存在）——第一次点亮起「确认」，再点才执行，
// 2.5s 不点自动复原；会话进行中不可用（先停止，避免边流边清的状态错乱）
const newSessionArmed = ref(false)
let newSessionTimer: number | undefined
function newSession() {
  if (!newSessionArmed.value) {
    newSessionArmed.value = true
    window.clearTimeout(newSessionTimer)
    newSessionTimer = window.setTimeout(() => { newSessionArmed.value = false }, 2500)
    return
  }
  window.clearTimeout(newSessionTimer)
  newSessionArmed.value = false
  AiResetSession().then(() => {
    items.value = []
    applySeq.value = 0
    pendingEvts.clear()
    clearGapTimer()
    busy.value = false
  }).catch(() => {})
}

// ---------- 取证时间线摘要（默认收起态的一行） ----------
// probeCount 可收起的步骤数（工具调用 + 视觉转述；产出文档是交付物不算）
function probeCount(it: ChatItem): number {
  let n = 0
  for (const s of it.steps) if (s.kind !== 'doc') n++
  return n
}
// liveProbe 最新一步的动作与状态（收起时仍能看到「现在在干什么」）
function liveProbe(it: ChatItem): string {
  for (let i = it.steps.length - 1; i >= 0; i--) {
    if (it.steps[i].kind !== 'doc') return it.steps[i].text + (it.steps[i].done ? ' ✓' : ' 取证中…')
  }
  return ''
}
// capRunning 收起摘要行上最新一步是否进行中：live 文案转 accent 色与完成态区分
function capRunning(it: ChatItem): boolean {
  for (let i = it.steps.length - 1; i >= 0; i--) {
    if (it.steps[i].kind !== 'doc') return it.steps[i].kind === 'call' && !it.steps[i].done
  }
  return false
}

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
// ---------- 源码读取授权卡 ----------
// AI 请求读源码目录时弹出：允许并记住（持久 ai.json）/ 仅本次（会话内）/ 拒绝（本会话不再问）
function showAuthCard(e: { id: string; path: string; reason: string; sensitive?: boolean }) {
  items.value.push({ kind: 'user', text: '', reasoning: '', steps: [], streaming: false,
    auth: { id: e.id, path: e.path, reason: e.reason, sensitive: !!e.sensitive, done: '' } })
  stickBottom.value = true
}
function answerAuth(idx: number, allow: boolean, remember: boolean) {
  const it = items.value[idx]
  if (!it?.auth || it.auth.done) return
  const au = it.auth
  au.done = allow ? (remember ? '已允许并记住' : '已允许（仅本次）') : '已拒绝'
  stickBottom.value = true // 回执后强制回底跟随：阻塞授权已续跑，后续取证/输出在视野内
  AiAuthRespond(au.id, allow, remember).then((ok: boolean) => {
    if (!ok) au.done = '回执未送达（本轮已结束）——下次读取会重新询问'
  }).catch(() => {
    au.done = '回执发送失败——下次读取会重新询问'
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
onUnmounted(() => {
  if (flushTimer !== undefined) window.clearTimeout(flushTimer)
  stopBusyTimer()
})

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
// 拖入文件/文件夹 → 路径附件：不读内容，AI 经 read_source（用户授权）自行读取；
// 图片文件例外走视觉通道（二进制 read_source 读不了）
const IMG_EXT = /\.(png|jpe?g|gif|webp|bmp)$/i
function addPathAtt(paths: string[]) {
  for (const p of paths) {
    if (IMG_EXT.test(p)) {
      // 图片文件拖入也走路径（WebView2 安全策略禁 fetch file://，无法转 dataURL；
      // 想给 AI 看图请用 Ctrl+V 粘贴截图）
      items.value.push({ kind: 'error', text: `「${p}」是图片文件：拖入走路径附件（AI 无法读取二进制）；想让它看图请直接粘贴截图（Ctrl+V）`, reasoning: '', steps: [], streaming: false })
      continue
    }
    inputAtt.value.push({ name: p, size: 0, kind: 'path', data: p })
  }
}
defineExpose({ onEvent, prefill, addPathAtt, showAuthCard })
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
      <button class="ai-redo" :class="{ armed: newSessionArmed }" :disabled="busy"
        :title="busy ? '会话进行中，请先停止再开新会话' : (newSessionArmed ? '再点一次确认：当前会话将不存在' : '清空上下文重新开始（当前会话将不存在）')"
        @click="newSession">
        <AppIcon name="rotate-ccw" :size="12" />{{ newSessionArmed ? '确认新会话？' : '新会话' }}
      </button>
      <button class="ai-x" title="关闭" @click="emit('close')"><AppIcon name="x" :size="12" /></button>
      <template v-if="configured">
        <div class="ai-pickers">
          <select v-model="activeProfile" class="ai-sel" :disabled="busy"
            :title="busy ? '会话进行中——停止或结束后可切换厂商' : '模型厂商'">
            <option v-for="p in config.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
          <select v-model="effortSel" class="ai-sel ai-effort" :disabled="busy"
            :title="busy ? '会话进行中——停止或结束后可调整思考等级' : '思考等级（切换后下次请求生效；默认不发该字段）'">
            <option value="">思考·默认</option>
            <option value="low">思考·低</option>
            <option value="medium">思考·中</option>
            <option value="high">思考·高</option>
          </select>
          <select v-model="activeModel" class="ai-sel ai-model" :disabled="busy"
            :title="busy ? '会话进行中——停止或结束后可切换模型' : ctxNote">
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
        <!-- 授权卡：待答为完整卡；答完收起为单行记录（保留授权痕迹不占版面） -->
        <div v-if="it.auth && !it.auth.done" class="auth-card" :class="{ sens: it.auth.sensitive }">
          <div class="auth-head">
            <AppIcon :name="it.auth.sensitive ? 'info' : 'folder'" :size="13" />
            <span>{{ it.auth.sensitive ? '敏感文件读取授权（每次读取均需确认）' : '目录访问授权请求（整棵子树有效）' }}</span>
          </div>
          <div class="auth-path" :title="it.auth.path">{{ it.auth.path }}</div>
          <div class="auth-reason">{{ it.auth.sensitive ? '该文件可能包含密钥/凭证' : '动机' }}：{{ it.auth.reason || '（未说明）' }}</div>
          <div class="auth-btns">
            <template v-if="!it.auth.sensitive">
              <button class="primary" @click="answerAuth(ii, true, true)">允许并记住</button>
            </template>
            <button :class="it.auth.sensitive ? 'primary' : ''" @click="answerAuth(ii, true, false)">仅本次</button>
            <button class="danger" @click="answerAuth(ii, false, false)">拒绝</button>
          </div>
        </div>
        <div v-else-if="it.auth" class="auth-rec" :class="{ deny: !it.auth.done.startsWith('已允许') }">
          <AppIcon :name="it.auth.done.startsWith('已允许') ? 'check' : 'x'" :size="11" />
          <span class="auth-rec-st">{{ it.auth.done }}</span>
          <span class="auth-rec-path" :title="it.auth.path">{{ it.auth.path }}</span>
        </div>
        <!-- 用户消息（授权卡项 text 为空——排除，否则渲染出空气泡） -->
        <div v-if="it.kind === 'user' && !it.auth" class="msg-user">
          <div class="u-bubble">
            <div v-if="it.text" class="u-text">{{ it.text }}</div>
            <div v-if="it.atts?.length" class="u-atts">
              <span v-for="(a, gi) in it.atts" :key="gi" class="u-att">
                <AppIcon :name="a.kind === 'image' ? 'image' : (a.kind === 'path' ? 'folder' : 'file-text')" :size="10" />{{ a.name }}（{{ a.kind === 'path' ? '路径' : fmtSize(a.size) }}）
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
          <!-- 思考过程：流式期间展开跟随、标题「思考中…」；轮结束自动收起、
               标题转「思考过程」——曾写死「思考中…」，结束后永远显示在思考的假象 -->
          <details v-if="it.reasoning" class="ai-reasoning" :open="it.rOpen"
            @toggle="it.rOpen = ($event.target as HTMLDetailsElement).open">
            <summary>{{ it.streaming ? '思考中…' : '思考过程' }}</summary>
            <pre>{{ it.reasoning }}</pre>
          </details>
          <!-- 取证时间线：默认收起为一行摘要（步数 + 最新动作实时跳动），点击展开；
               产出文档卡是交付物不随收起隐藏。收起行与步骤行同构同距（图标列严格对齐）；
               进行中的步骤：图标转 accent 色 + 旋转 spinner（与完成态 ✓ 一眼区分） -->
          <div v-if="it.steps.length" class="ai-steps">
            <div v-if="probeCount(it)" class="steps-cap" :class="{ open: it.stepsOpen }" @click="it.stepsOpen = !it.stepsOpen">
              <AppIcon name="terminal" :size="11" />
              <span class="cap-n">取证 {{ probeCount(it) }} 步</span>
              <span class="cap-live" :class="{ run: capRunning(it) }">{{ liveProbe(it) }}</span>
              <span class="cap-chev">{{ it.stepsOpen ? '收起' : '展开' }}</span>
            </div>
            <div v-show="it.stepsOpen || st.kind === 'doc'" v-for="(st, si) in it.steps" :key="si" class="step" :class="['step-' + st.kind, { run: st.kind === 'call' && !st.done }]">
              <div class="st-body">
                <div class="st-line">
                  <span class="st-ic">
                    <AppIcon v-if="st.kind === 'call'" name="terminal" :size="11" />
                    <AppIcon v-else-if="st.kind === 'doc'" name="file-text" :size="11" />
                    <AppIcon v-else name="image" :size="11" />
                  </span>
                  <span class="st-name">{{ st.text }}</span>
                  <span v-if="st.kind === 'call' && !st.done" class="st-spin" title="取证中"></span>
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
          <div v-if="busy && it.streaming" class="ai-wait-v2">
            <span class="ai-spinner"></span>
            <span>正在分析</span>
            <!-- 阶段标注只在「看不见动静」时出现：取证中·工具名（等授权/慢扫描时）/
                 模型思考中（推理）；正文在流时自证，不加多余标签 -->
            <span v-if="activeToolName" class="ai-phase acc">取证中 · {{ activeToolName }}</span>
            <span v-else-if="!outputting" class="ai-phase">模型思考中</span>
            <span class="ai-elapsed" v-if="busySec > 0">{{ fmtElapsed(busySec) }}</span>
          </div>

        </div>
      </template>
    </div>

    <!-- 输入区 -->
    <div class="ai-input" v-if="configured" @dragover.prevent>
      <div v-if="inputAtt.length" class="in-imgs">
        <div v-for="(a, gi) in inputAtt" :key="gi" class="in-img">
          <img v-if="a.kind === 'image'" :src="a.data" alt="待发送" />
          <div v-else-if="a.kind === 'path'" class="in-file in-path" :title="'路径附件（AI 经授权读取）：' + a.data">
            <AppIcon name="folder" :size="14" />
            <span class="in-file-n">{{ a.data }}</span>
            <span class="in-file-s">路径</span>
          </div>
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
          placeholder="追问（Enter 发送 · Shift+Enter 换行）"
          :disabled="!activeModel" @keydown="onKey" @paste="onPaste"></textarea>
        <button v-if="!busy" class="in-send" :disabled="(!inputText.trim() && !inputAtt.length) || !activeModel" title="发送" @click="send">
          <AppIcon name="send" :size="13" />
        </button>
        <button v-else class="in-stop" title="停止分析" @click="stop">停止</button>
      </div>
      <div class="in-note">{{ ctxNote }} · Ctrl+V 贴图 · 拖入文件/文件夹 = 路径附件（AI 经授权读取）</div>
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
.ai-redo.armed { color: var(--danger); border-color: var(--danger); background: color-mix(in srgb, var(--danger) 10%, transparent); }
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
.ai-sel:disabled { opacity: .5; cursor: not-allowed; } /* 会话进行中锁定切档/切模型/思考档 */
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

/* 授权卡 */
.auth-card.sens { border-color: color-mix(in srgb, var(--lvl-3-fg) 45%, transparent); background: var(--lvl-3-bg); }
.auth-card.sens .auth-head { color: var(--lvl-3-fg); }
.auth-card {
  border: 1px solid var(--accent-line);
  background: var(--accent-soft);
  border-radius: var(--radius-sm);
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.auth-head { display: flex; align-items: center; gap: 6px; font-size: 11.5px; font-weight: 600; color: var(--accent-strong); }
.auth-path { font-family: var(--mono); font-size: 10.5px; color: var(--text); word-break: break-all; }
.auth-reason { font-size: 11px; color: var(--text-dim); }
.auth-btns { display: flex; gap: 6px; margin-top: 4px; }
.auth-btns button { font-size: 11px; padding: 3px 10px; }
.auth-btns .danger:hover { border-color: var(--danger); color: var(--danger); }
/* 已答授权单行记录（收起态） */
.auth-rec { display: flex; align-items: center; gap: 6px; max-width: 560px;
  padding: 3px 10px; border: 1px solid var(--border); border-radius: var(--radius-sm);
  background: var(--bg-elev); color: var(--text-dim); }
.auth-rec-st { font-size: 10.5px; flex: none; }
.auth-rec-path { font-family: var(--mono); font-size: 10px; color: var(--text-faint);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.auth-rec.deny .auth-rec-st { color: var(--text-faint); }
.in-path .in-file-n { color: var(--accent-strong); }

/* 等待状态：旋转 spinner + 持续秒数 */
.ai-wait-v2 {
  display: flex; align-items: center; gap: 8px;
  font-size: 11.5px; color: var(--text-dim);
  padding: 4px 0;
}
.ai-spinner {
  width: 14px; height: 14px; flex: none;
  border: 2px solid var(--bg-elev);
  border-top-color: var(--accent-strong);
  border-radius: 50%;
  animation: ai-spin .8s linear infinite;
}
.ai-elapsed {
  font-family: var(--mono); font-size: 10px;
  color: var(--accent-strong);
  background: var(--accent-soft);
  border-radius: 999px; padding: 1px 6px;
}
.ai-phase { font-family: var(--mono); font-size: 10px; color: var(--text-faint); }
.ai-phase.acc { color: var(--accent-strong); }
@keyframes ai-spin { to { transform: rotate(360deg); } }

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

/* 取证时间线：收起摘要行与步骤行同构（同 padding 同图标列——严格对齐，不再错位） */
.steps-cap { /* 收起态摘要行：步数 + 最新动作，点击展开（无底板，与步骤行共一条左边线） */
  display: flex; align-items: center; gap: 5px;
  margin: 2px 0 4px; padding: 2px 0;
  font-family: var(--mono); font-size: 10.5px; color: var(--text-dim);
  cursor: pointer; user-select: none;
}
.steps-cap:hover { color: var(--text); }
.steps-cap.open { margin-bottom: 6px; }
.steps-cap :deep(svg) { color: var(--text-faint); }
.steps-cap .cap-n { flex: none; color: var(--text); font-weight: 600; }
.steps-cap .cap-live { flex: 1; min-width: 0; font-size: 10px; color: var(--text-faint);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.steps-cap .cap-live.run { color: var(--accent-strong); }
.steps-cap .cap-chev { flex: none; color: var(--text-faint); }
.ai-steps {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding-left: 2px;
}
/* 步骤行卡（层次：摘要行=元信息 / 行卡=条目 / 详情=条目内嵌套，三级递进） */
.step { margin: 3px 0; }
.st-body {
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 5px 9px;
}
.step.run .st-body { border-color: var(--accent-line); }
.step-doc .st-body { border-color: color-mix(in srgb, var(--ok) 30%, transparent); }
.st-line {
  display: flex;
  align-items: center;
  gap: 7px;
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--text-dim);
}
/* 图标入位（圆角小方 chip）：视觉锚点替代此前被否掉的裸圆点 */
.st-ic {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 6px;
  background: var(--bg-panel);
  color: var(--text-faint);
}
.step-doc .st-ic { color: var(--ok); }
.step.run .st-ic { color: var(--accent-strong); }
.step.run .st-name { color: var(--accent-strong); }
.st-name {
  font-weight: 600;
  color: var(--text);
  min-width: 0; /* flex 子项默认 min-width:auto 不收缩——缺它会溢出行卡 */
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 状态右置：完成 ✓ 绿 / 进行中 spinner 冰蓝（占位同一位置，动态一眼可辨） */
.st-ok { margin-left: auto; flex: none; color: var(--ok); font-size: 10px; }
.st-spin {
  flex: none;
  margin-left: auto;
  width: 10px;
  height: 10px;
  border: 1.5px solid var(--accent-soft);
  border-top-color: var(--accent-strong);
  border-radius: 50%;
  animation: st-spin .8s linear infinite;
}
@keyframes st-spin { to { transform: rotate(360deg); } }
/* 详情：缩进到名称列正下方（层次从属），轻链接式摘要 */
.st-detail { margin: 3px 0 1px 27px; font-size: 10.5px; }
.st-detail summary { cursor: pointer; color: var(--text-faint); font-size: 10px; user-select: none; width: fit-content; }
.st-detail summary:hover { color: var(--accent-strong); }
.st-detail pre {
  margin-top: 4px;
  font-family: var(--mono);
  font-size: 10px;
  line-height: 1.5;
  color: var(--text-faint);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 140px;
  overflow-y: auto;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 8px;
}

/* markdown 正文与 chip（aiMd.ts 生成的类，经 :deep 落样式） */
/* 报告面板化（方向A）：正文结论是「产出物」，给面板底+边框+顶缘微光，与取证时间线分层 */
.ai-md {
  font-size: 12px;
  line-height: 1.65;
  color: var(--text);
  min-width: 0;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 10px 12px;
  box-shadow: var(--sheen);
}
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
  padding: 5px 10px;
  color: var(--danger);
  border-color: color-mix(in srgb, var(--danger) 45%, transparent);
  font-size: 11.5px;
}
.in-stop:hover { border-color: var(--danger); color: var(--danger); background: color-mix(in srgb, var(--danger) 8%, transparent); }
.in-note {
  font-family: var(--mono);
  font-size: 9.5px;
  color: var(--text-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
