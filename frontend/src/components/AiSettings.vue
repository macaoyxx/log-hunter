<script setup lang="ts">
// AI 诊断设置（模态）：厂商配置 CRUD（名称/BaseURL/APIKey/模型列表）、
// 模型能力三属性（ctx 窗口 / vision 视觉 / maxOut 响应上限）行内编辑、
// 主/视觉厂商默认选择（跨厂商组合）、思考等级与全局默认。
// 编辑走本地 draft，显式保存；「拉取模型/测试连接」操作已保存厂商（点击时先静默保存 draft）
import { computed, ref } from 'vue'
import AppIcon from './AppIcon.vue'
import { conf } from '../../wailsjs/go/models'
import { AiPullModels, SetAiConfig, TestAiConn } from '../../wailsjs/go/main/App'

const props = defineProps<{ config: conf.Conf }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

// 深拷贝 draft（保存前不动全局）
function cloneConf(c: conf.Conf): conf.Conf {
  const out = conf.Conf.createFrom(JSON.parse(JSON.stringify(c)))
  // 历史数据兜底：Models/Profiles 曾以 null 落盘（后端已修，旧文件仍可能带 null）。
  // 数字能力不做前端代填——空值经输入框占位符展示全局默认，保持「空 = 用默认」语义
  if (!out.profiles) out.profiles = []
  for (const p of out.profiles) if (!p.models) p.models = []
  return out
}
const draft = ref(cloneConf(props.config))
const saving = ref(false)
const saveErr = ref('')
const pullState = ref<Record<string, string>>({}) // 厂商 id → 拉取状态
const testState = ref<Record<string, string>>({}) // 厂商 id → 测试结果

const expanded = ref<string | null>(draft.value.profiles[0]?.id ?? null)

function addProfile() {
  const p = conf.Profile.createFrom({
    id: '', name: '新厂商 ' + (draft.value.profiles.length + 1),
    baseUrl: '', apiKey: '', models: [],
  })
  draft.value.profiles.push(p)
  expanded.value = p.id || 'new-' + draft.value.profiles.length
}
function removeProfile(i: number) {
  const p = draft.value.profiles[i]
  draft.value.profiles.splice(i, 1)
  if (draft.value.activeProfile === p.id) { draft.value.activeProfile = ''; draft.value.activeModel = '' }
  if (draft.value.visionProfile === p.id) { draft.value.visionProfile = ''; draft.value.visionModel = '' }
}
function addModel(p: conf.Profile) {
  p.models.push(conf.Model.createFrom({ name: 'model-name', ctx: 0, vision: false, maxOut: 0 }))
}
function profileById(id: string | undefined) { return draft.value.profiles.find(p => p.id === id) }

// 保存 draft；返回是否成功
async function persist(): Promise<boolean> {
  saving.value = true
  saveErr.value = ''
  try {
    await SetAiConfig(draft.value)
    return true
  } catch (e) {
    saveErr.value = String(e)
    return false
  } finally {
    saving.value = false
  }
}
async function save() {
  if (await persist()) { emit('saved'); emit('close') }
}
// 拉取模型/测试连接前先保存（这两操作作用于已持久化厂商）
async function pullModels(p: conf.Profile) {
  pullState.value[p.id] = '拉取中…'
  if (!(await persist())) { pullState.value[p.id] = '保存失败'; return }
  try {
    const models = await AiPullModels(p.id)
    p.models = models
    pullState.value[p.id] = `已拉取 ${models.length} 个模型`
  } catch (e) {
    pullState.value[p.id] = '失败：' + String(e)
  }
}
async function testConn(p: conf.Profile) {
  const model = p.models[0]?.name || ''
  if (!model) { testState.value[p.id] = '请先添加/拉取模型'; return }
  testState.value[p.id] = '测试中…'
  if (!(await persist())) { testState.value[p.id] = '保存失败'; return }
  try {
    const out = await TestAiConn(p.id, model)
    testState.value[p.id] = '✓ 连通（回复：' + out.slice(0, 24) + '）'
  } catch (e) {
    testState.value[p.id] = '✕ ' + String(e)
  }
}

const visionOptions = computed(() => { // 展平：所有厂商的 vision 模型（select 不能嵌套）
  const out: { id: string; label: string; name: string }[] = []
  for (const p of draft.value.profiles) {
    for (const m of p.models) {
      if (m.vision) out.push({ id: p.id + '/' + m.name, label: `${p.name} / ${m.name}`, name: m.name })
    }
  }
  return out
})
</script>

<template>
  <div class="st-mask" @click.self="emit('close')">
    <div class="st-dlg">
      <div class="st-head">
        <span class="st-title"><AppIcon name="sparkles" :size="14" />AI 诊断设置</span>
        <button class="st-x" title="关闭" @click="emit('close')"><AppIcon name="x" :size="12" /></button>
      </div>

      <div class="st-body">
        <!-- 厂商列表 -->
        <div class="st-sec">模型厂商（OpenAI 兼容：DeepSeek / GLM / Ollama /v1 / 自建网关）</div>
        <div v-for="(p, pi) in draft.profiles" :key="pi" class="st-card" :class="{ open: expanded === (p.id || 'new-' + (pi + 1)) }">
          <button class="st-card-head" @click="expanded = expanded === (p.id || 'new-' + (pi + 1)) ? null : (p.id || 'new-' + (pi + 1))">
            <AppIcon name="chevron-right" :size="12" class="st-exp" :class="{ flip: expanded === (p.id || 'new-' + (pi + 1)) }" />
            <input v-model="p.name" class="st-name" placeholder="厂商名（如 DeepSeek）" @click.stop />
            <span class="st-meta">{{ p.baseUrl || '未配置地址' }} · {{ p.models.length }} 模型</span>
          </button>
          <div v-if="expanded === (p.id || 'new-' + (pi + 1))" class="st-card-body" @click.stop>
            <div class="st-grid">
              <label class="st-field span2">
                <span>Base URL</span>
                <input v-model="p.baseUrl" placeholder="https://api.deepseek.com/v1" />
              </label>
              <label class="st-field span2">
                <span>API Key（留空 = 不鉴权，Ollama/自建网关）</span>
                <input v-model="p.apiKey" type="password" placeholder="sk-…" autocomplete="off" />
              </label>
            </div>
            <div class="st-sub">模型（窗口/上限留空 = 用全局默认 {{ (((draft.ctxTokens ?? 1048576)/1048576).toFixed(1).replace(/\.0$/, '')) }}M / {{ draft.maxOutTokens ?? 8192 }} · 视觉=支持图片，vision/vl 类自动勾选）</div>
            <div v-for="(m, mi) in p.models" :key="mi" class="st-model">
              <input v-model="m.name" class="st-m-name" placeholder="模型名" />
              <input v-model.number="m.ctx" class="st-m-num" type="number" min="0" title="上下文窗口（token，留空用全局默认）" :placeholder="String(draft.ctxTokens)" />
              <label class="st-m-vis" title="支持图片输入（纯文本模型贴图时走视觉厂商转述）">
                <input v-model="m.vision" type="checkbox" />视觉
              </label>
              <input v-model.number="m.maxOut" class="st-m-num" type="number" min="0" title="单次响应上限（留空用全局默认）" :placeholder="String(draft.maxOutTokens)" />
              <button class="st-m-del" title="移除模型" @click="p.models.splice(mi, 1)"><AppIcon name="x" :size="10" /></button>
            </div>
            <div class="st-rowbtns">
              <button @click="addModel(p)"><AppIcon name="plus" :size="11" />手填模型</button>
              <button @click="pullModels(p)"><AppIcon name="refresh-cw" :size="11" />拉取模型</button>
              <button @click="testConn(p)"><AppIcon name="zap" :size="11" />测试连接</button>
              <button class="danger" @click="removeProfile(pi)"><AppIcon name="trash-2" :size="11" />删除厂商</button>
            </div>
            <div v-if="pullState[p.id]" class="st-state">{{ pullState[p.id] }}</div>
            <div v-if="testState[p.id]" class="st-state">{{ testState[p.id] }}</div>
          </div>
        </div>
        <button class="st-add" @click="addProfile"><AppIcon name="plus" :size="12" />添加厂商</button>

        <!-- 默认选择与全局 -->
        <div class="st-sec">默认选择</div>
        <div class="st-grid">
          <label class="st-field">
            <span>主模型厂商</span>
            <select v-model="draft.activeProfile">
              <option value="">（未选）</option>
              <option v-for="p in draft.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
          </label>
          <label class="st-field">
            <span>主模型（可留空，会话中选）</span>
            <select v-model="draft.activeModel">
              <option value="">（不指定）</option>
              <option v-for="m in profileById(draft.activeProfile)?.models ?? []" :key="m.name" :value="m.name">
                {{ m.name }}{{ m.vision ? ' ◉' : '' }}
              </option>
            </select>
          </label>
          <label class="st-field">
            <span>视觉厂商（可与主厂商不同）</span>
            <select v-model="draft.visionProfile">
              <option value="">（不配置）</option>
              <option v-for="p in draft.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
          </label>
          <label class="st-field">
            <span>视觉模型（贴图经它转述给纯文本主模型）</span>
            <select v-model="draft.visionModel">
              <option value="">（不指定）</option>
              <option v-for="o in visionOptions" :key="o.id" :value="o.name">{{ o.label }}</option>
            </select>
          </label>
          <label class="st-field">
            <span>思考等级（默认不发该字段）</span>
            <select v-model="draft.effort">
              <option value="">跟随端点默认</option>
              <option value="low">低</option>
              <option value="medium">中</option>
              <option value="high">高</option>
            </select>
          </label>
          <label class="st-field">
            <span>全局默认窗口 / 响应（token）</span>
            <div class="st-pair">
              <input v-model.number="draft.ctxTokens" type="number" min="1000" title="ctxTokens" />
              <input v-model.number="draft.maxOutTokens" type="number" min="256" title="maxOutTokens" />
            </div>
          </label>
        </div>
        <div class="st-note">配置存 exe 同级 data/ai.json（明文，独立于 config.json）；模型级能力优先生效，留空回落全局默认。</div>
      </div>

      <div class="st-foot">
        <span v-if="saveErr" class="st-err">{{ saveErr }}</span>
        <button class="ghost" @click="emit('close')">取消</button>
        <button class="primary" :disabled="saving" @click="save">{{ saving ? '保存中…' : '保存' }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.st-mask {
  position: fixed;
  inset: 0;
  z-index: 300;
  background: var(--mask);
  display: flex;
  align-items: center;
  justify-content: center;
}
.st-dlg {
  width: min(620px, 92vw);
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  background: var(--bg-float);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  box-shadow: var(--shadow-pop), var(--sheen-light);
}
.st-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
}
.st-title { display: inline-flex; align-items: center; gap: 7px; font-size: 13.5px; font-weight: 600; }
.st-title :deep(svg) { color: var(--accent-strong); }
.st-x {
  margin-left: auto;
  background: none;
  border: none;
  color: var(--text-faint);
  padding: 3px;
}
.st-x:hover { color: var(--text); border: none; background: var(--bg-hover); }

.st-body { flex: 1; min-height: 0; overflow-y: auto; padding: 12px 16px; }
.st-sec {
  font-size: 10px;
  letter-spacing: .09em;
  text-transform: uppercase;
  color: var(--text-faint);
  font-weight: 600;
  padding: 8px 2px 6px;
}
.st-card {
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  margin-bottom: 6px;
  overflow: hidden;
}
.st-card.open { border-color: var(--border-strong); }
.st-card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  justify-content: flex-start;
  padding: 7px 10px;
  background: transparent;
  border: none;
  border-radius: 0;
  text-align: left;
}
.st-card-head:hover { background: var(--bg-hover); border: none; }
.st-exp { flex: none; color: var(--text-faint); transition: transform .15s; }
.st-exp.flip { transform: rotate(90deg); }
.st-name {
  width: 130px;
  font-size: 12px;
  padding: 2px 6px;
  background: transparent;
  border-color: transparent;
}
.st-name:hover, .st-name:focus { border-color: var(--border-strong); background: var(--bg-panel); }
.st-meta {
  font-family: var(--mono);
  font-size: 10px;
  color: var(--text-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.st-card-body { padding: 4px 12px 10px; border-top: 1px solid var(--border); }
.st-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 10px; padding: 8px 0 2px; }
.st-field { display: flex; flex-direction: column; gap: 3px; font-size: 10.5px; color: var(--text-faint); }
.st-field.span2 { grid-column: span 2; }
.st-field input, .st-field select { font-size: 12px; padding: 4px 8px; }
.st-pair { display: flex; gap: 6px; }
.st-pair input { width: 50%; font-family: var(--mono); font-size: 11.5px; }
.st-sub { font-size: 10px; color: var(--text-faint); padding: 8px 2px 4px; }
.st-model {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 4px;
}
.st-m-name { flex: 1; min-width: 0; font-family: var(--mono); font-size: 11.5px; padding: 3px 8px; }
.st-m-num {
  flex: none;
  width: 88px;
  font-family: var(--mono);
  font-size: 11px;
  padding: 3px 6px;
  text-align: right;
}
.st-m-vis {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 10.5px;
  color: var(--text-dim);
  white-space: nowrap;
  cursor: pointer;
}
.st-m-vis input { accent-color: var(--accent-strong); }
.st-m-del {
  flex: none;
  width: 22px;
  height: 22px;
  padding: 0;
  justify-content: center;
  color: var(--text-faint);
}
.st-m-del:hover { color: var(--danger); border-color: var(--danger); }
.st-rowbtns { display: flex; gap: 6px; padding-top: 6px; flex-wrap: wrap; }
.st-rowbtns button { font-size: 11px; padding: 3px 9px; gap: 4px; }
.st-state { font-size: 10.5px; color: var(--text-faint); padding-top: 5px; word-break: break-all; }
.st-add { font-size: 11.5px; padding: 4px 10px; color: var(--text-dim); }
.st-add:hover { color: var(--accent-strong); border-color: var(--accent-line); }

.st-note {
  margin-top: 10px;
  padding-top: 9px;
  border-top: 1px solid var(--border);
  font-family: var(--mono);
  font-size: 9.5px;
  color: var(--text-faint);
}

.st-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  justify-content: flex-end;
  padding: 10px 16px;
  border-top: 1px solid var(--border);
}
.st-err {
  margin-right: auto;
  font-size: 11px;
  color: var(--danger);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
