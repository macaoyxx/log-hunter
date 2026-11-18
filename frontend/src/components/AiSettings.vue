<script setup lang="ts">
// AI 诊断设置（模态 · 主从双栏）：顶部全局装备条（主/视觉通道、思考档、默认窗口）+
// 左侧档案导航列 + 右侧详情编辑（端点/密钥/模型能力表/拉取/测试），目录授权独立页签。
// 厂商配置 CRUD、模型能力三属性（ctx/vision/maxOut）语义不变；
// 编辑走本地 draft，显式保存；「拉取模型/测试连接」操作已保存厂商（点击时先静默保存 draft）
import { computed, onMounted, ref } from 'vue'
import AppIcon from './AppIcon.vue'
import { conf } from '../../wailsjs/go/models'
import { AiPullModels, GetAiConfig, SetAiConfig, TestAiConn } from '../../wailsjs/go/main/App'

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
// 打开时重拉最新：授权目录在后端直接写盘（诊断中点「允许并记住」），props.config 是
// App 层旧快照，不重拉会出现「明明记住了却显示暂无授权」
onMounted(() => {
  GetAiConfig().then((c: conf.Conf) => {
    if (c.profiles) {
      draft.value = cloneConf(c)
      initAfterLoad()
    }
  }).catch(() => {})
})
const saving = ref(false)
const saveErr = ref('')
const pullState = ref<Record<string, string>>({}) // 厂商 id → 拉取状态
const testState = ref<Record<string, string>>({}) // 厂商 id → 测试结果

// 草稿脏标记（标题栏「未保存」点）：与打开时快照逐字对比
let baselineJSON = JSON.stringify(draft.value)
const dirty = computed(() => JSON.stringify(draft.value) !== baselineJSON)
function initAfterLoad() {
  baselineJSON = JSON.stringify(draft.value)
  // 重拉后档案可能已变：选中项失效则回落到第一个
  if (!draft.value.profiles.some(p => p.id === sel.value)) {
    sel.value = draft.value.profiles[0]?.id ?? null
  }
}

// 主从导航：左侧厂商列表（只有厂商——授权目录是全局配置，独立底部分区，不混列）
const sel = ref<string | null>(draft.value.profiles[0]?.id ?? null)
const cur = computed(() => draft.value.profiles.find(p => p.id === sel.value) ?? null)
const grantsOpen = ref(false) // 底部授权分区折叠态

function addProfile() {
  // 新增即分配不冲突 id（与后端 Normalize 的 p<N> 规则一致）：draft 内需要稳定身份
  let n = draft.value.profiles.length + 1
  while (draft.value.profiles.some(p => p.id === 'p' + n)) n++
  const p = conf.Profile.createFrom({
    id: 'p' + n, name: '新厂商 ' + (draft.value.profiles.length + 1),
    baseUrl: '', apiKey: '', models: [],
  })
  draft.value.profiles.push(p)
  sel.value = p.id
}
function removeProfile(id: string) {
  const i = draft.value.profiles.findIndex(p => p.id === id)
  if (i < 0) return
  const p = draft.value.profiles[i]
  draft.value.profiles.splice(i, 1)
  if (draft.value.activeProfile === p.id) { draft.value.activeProfile = ''; draft.value.activeModel = '' }
  if (draft.value.visionProfile === p.id) { draft.value.visionProfile = ''; draft.value.visionModel = '' }
  if (sel.value === p.id) sel.value = draft.value.profiles[Math.min(i, draft.value.profiles.length - 1)]?.id ?? null
}
function addModel(p: conf.Profile) {
  p.models.push(conf.Model.createFrom({ name: 'model-name', ctx: 0, vision: false, maxOut: 0 }))
}
function profileById(id: string | undefined) { return draft.value.profiles.find(p => p.id === id) }
// 档案状态灯：测试连通=绿 / 已配端点未测=蓝 / 未配置=灰
function ledOf(p: conf.Profile): string {
  if ((testState.value[p.id] ?? '').startsWith('✓')) return 'ok'
  if (p.baseUrl) return 'acc'
  return ''
}

// 保存 draft；返回是否成功
async function persist(): Promise<boolean> {
  saving.value = true
  saveErr.value = ''
  try {
    await SetAiConfig(draft.value)
    baselineJSON = JSON.stringify(draft.value)
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
// 拉取/测试直接作用于草稿档案（后端按传入档案执行，不落盘）——没点「保存」就不会进 ai.json
async function pullModels(p: conf.Profile) {
  pullState.value[p.id] = '拉取中…'
  try {
    const models = await AiPullModels(p)
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
  try {
    const out = await TestAiConn(p, model)
    testState.value[p.id] = '✓ 连通（回复：' + out.slice(0, 24) + '）'
  } catch (e) {
    testState.value[p.id] = '✕ ' + String(e)
  }
}

// 授权目录与其他配置同为草稿制：删除/清空只改草稿，点「保存」随 SetAiConfig 一并生效
function clearAllGrants() {
  draft.value.grantedDirs = []
}
function removeGrant(g: string) {
  draft.value.grantedDirs = (draft.value.grantedDirs ?? []).filter(x => x !== g)
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
        <span class="st-title"><AppIcon name="sparkles" :size="14" />AI 专家装备</span>
        <span v-if="dirty" class="st-dirty">草稿未保存</span>
        <button class="st-x" title="关闭" @click="emit('close')"><AppIcon name="x" :size="12" /></button>
      </div>

      <!-- 全局装备条：一次看全「谁在对话/谁看图/思考多深/纸有多大」 -->
      <div class="st-global">
        <label class="st-g">
          <span>主通道（对话）</span>
          <div class="st-g-pair">
            <select v-model="draft.activeProfile">
              <option value="">（未选厂商）</option>
              <option v-for="p in draft.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
            <select v-model="draft.activeModel">
              <option value="">（不指定模型）</option>
              <option v-for="m in profileById(draft.activeProfile)?.models ?? []" :key="m.name" :value="m.name">
                {{ m.name }}{{ m.vision ? ' ◉' : '' }}
              </option>
            </select>
          </div>
        </label>
        <label class="st-g">
          <span>视觉通道（贴图，可与主通道不同厂商）</span>
          <div class="st-g-pair">
            <select v-model="draft.visionProfile">
              <option value="">（不配置）</option>
              <option v-for="p in draft.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
            <select v-model="draft.visionModel">
              <option value="">（不指定）</option>
              <option v-for="o in visionOptions" :key="o.id" :value="o.name">{{ o.label }}</option>
            </select>
          </div>
        </label>
        <label class="st-g st-g-s">
          <span>思考等级</span>
          <select v-model="draft.effort">
            <option value="">默认</option>
            <option value="low">低</option>
            <option value="medium">中</option>
            <option value="high">高</option>
          </select>
        </label>
        <label class="st-g st-g-s">
          <span>全局默认（窗口 / 响应）</span>
          <div class="st-g-pair">
            <input v-model.number="draft.ctxTokens" type="number" min="1000" title="上下文窗口（token）" />
            <input v-model.number="draft.maxOutTokens" type="number" min="256" title="响应上限（token）" />
          </div>
        </label>
      </div>

      <!-- 主从双栏 -->
      <div class="st-split">
        <div class="st-rail">
          <div class="st-rail-cap">模型厂商</div>
          <button v-for="p in draft.profiles" :key="p.id" class="st-rail-item" :class="{ on: sel === p.id }" @click="sel = p.id">
            <i class="led" :class="ledOf(p)"></i>
            <span class="nm">{{ p.name }}</span>
            <span class="cnt">{{ p.models.length }}</span>
          </button>
          <button class="st-rail-add" @click="addProfile"><AppIcon name="plus" :size="11" />添加厂商</button>
        </div>

        <div class="st-detail">
          <template v-if="cur">
            <div class="st-d-row">
              <label class="st-f"><span>厂商名称</span>
                <input v-model="cur.name" placeholder="如 DeepSeek" /></label>
              <label class="st-f st-f-wide"><span>端点地址（OpenAI 兼容 /v1）</span>
                <input v-model="cur.baseUrl" class="mono-in" placeholder="https://api.deepseek.com/v1" /></label>
              <label class="st-f"><span>API Key（留空 = 不鉴权）</span>
                <input v-model="cur.apiKey" type="password" placeholder="sk-…" autocomplete="off" /></label>
            </div>

            <div class="st-d-scroll">
              <div class="st-sub">窗口/上限留空 = 用全局默认（{{ (((draft.ctxTokens ?? 1048576) / 1048576).toFixed(1).replace(/\.0$/, '')) }}M / {{ draft.maxOutTokens ?? 8192 }}）；视觉 = 支持图片输入，vision/vl 类自动勾选</div>
              <div v-for="(m, mi) in cur.models" :key="mi" class="st-model">
                <input v-model="m.name" class="st-m-name mono-in" placeholder="模型名" />
                <input v-model.number="m.ctx" class="st-m-num mono-in" type="number" min="0" title="上下文窗口（token，留空用全局默认）" :placeholder="String(draft.ctxTokens)" />
                <label class="st-m-vis" title="支持图片输入（纯文本模型贴图时走视觉厂商转述）">
                  <input v-model="m.vision" type="checkbox" />视觉
                </label>
                <input v-model.number="m.maxOut" class="st-m-num mono-in" type="number" min="0" title="单次响应上限（留空用全局默认）" :placeholder="String(draft.maxOutTokens)" />
                <button class="st-m-del" title="移除模型" @click="cur.models.splice(mi, 1)"><AppIcon name="x" :size="10" /></button>
              </div>
              <div class="st-rowbtns">
                <button @click="addModel(cur)"><AppIcon name="plus" :size="11" />手填模型</button>
                <button @click="pullModels(cur)"><AppIcon name="download" :size="11" />从端点拉取模型</button>
                <button @click="testConn(cur)"><AppIcon name="zap" :size="11" />测试连接</button>
                <button class="danger" @click="removeProfile(cur.id)"><AppIcon name="x" :size="11" />删除厂商</button>
              </div>
              <div v-if="pullState[cur.id]" class="st-state">{{ pullState[cur.id] }}</div>
              <div v-if="testState[cur.id]" class="st-state">{{ testState[cur.id] }}</div>
            </div>
          </template>

          <div v-else class="st-d-empty">
            <AppIcon name="info" :size="16" />
            <span>左侧选择或添加一个厂商开始配置</span>
          </div>
        </div>
      </div>

      <!-- 目录访问授权：全局配置独立分区（不与厂商混排——两类东西两处放） -->
      <div class="st-gz">
        <button class="st-gz-head" @click="grantsOpen = !grantsOpen">
          <AppIcon name="folder" :size="12" />
          <span class="st-gz-t">目录访问授权</span>
          <span class="st-gz-cnt">{{ draft.grantedDirs?.length || 0 }}</span>
          <span class="st-gz-hint">全局配置 · AI 读源码须授权（整棵子树有效），与厂商无关</span>
          <AppIcon class="st-gz-chev" :name="grantsOpen ? 'chevron-down' : 'chevron-right'" :size="12" />
        </button>
        <div v-if="grantsOpen" class="st-gz-body">
          <div v-if="draft.grantedDirs?.length" class="st-gz-list">
            <span v-for="g in [...draft.grantedDirs]" :key="g" class="st-gz-chip">
              <span class="st-gz-p" :title="g">{{ g }}</span>
              <button title="移除授权" @click="removeGrant(g)"><AppIcon name="x" :size="10" /></button>
            </span>
            <button class="danger st-gz-clear" @click="clearAllGrants">一键清空</button>
          </div>
          <div v-else class="st-gz-empty">暂无持久授权——诊断中点「允许并记住」会出现在这里</div>
        </div>
      </div>

      <div class="st-foot">
        <span class="st-note">存 exe 同级 data/ai.json（明文）· 模型级能力优先，留空回落全局默认</span>
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
  width: min(780px, 94vw);
  max-height: 88vh;
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
.st-dirty {
  font-size: 10px;
  color: var(--text-faint);
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 1px 8px;
}
.st-x {
  margin-left: auto;
  background: none;
  border: none;
  color: var(--text-faint);
  padding: 3px;
}
.st-x:hover { color: var(--text); border: none; background: var(--bg-hover); }

/* 全局装备条 */
.st-global {
  flex: none;
  display: flex;
  gap: 10px;
  padding: 10px 16px;
  border-bottom: 1px solid var(--border);
  background: var(--bg-panel);
  flex-wrap: wrap;
}
.st-g { display: flex; flex-direction: column; gap: 3px; font-size: 10px; color: var(--text-faint); flex: 1 1 180px; }
.st-g-s { flex: 0 1 130px; }
.st-g-pair { display: flex; gap: 5px; }
.st-g-pair select, .st-g-pair input { flex: 1; min-width: 0; font-size: 11.5px; padding: 4px 6px; }
.st-g select { font-size: 11.5px; padding: 4px 6px; }

/* 主从双栏 */
.st-split { flex: 1; min-height: 0; display: flex; }
.st-rail {
  flex: none;
  width: 176px;
  border-right: 1px solid var(--border);
  background: var(--bg-panel);
  padding: 10px 8px;
  display: flex;
  flex-direction: column;
  gap: 3px;
  overflow-y: auto;
}
.st-rail-cap {
  font-size: 10px;
  letter-spacing: .09em;
  text-transform: uppercase;
  color: var(--text-faint);
  font-weight: 600;
  padding: 2px 8px 6px;
}
.st-rail-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  justify-content: flex-start;
  padding: 7px 9px;
  font-size: 12px;
  color: var(--text-dim);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
}
.st-rail-item:hover { background: var(--bg-hover); border-color: transparent; }
.st-rail-item.on { background: var(--accent-soft); border-color: var(--accent-line); color: var(--text); }
.st-rail-item .led { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--border-strong); }
.st-rail-item .led.ok { background: var(--ok); }
.st-rail-item .led.acc { background: var(--accent-strong); }
.st-rail-item .nm { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; }
.st-rail-item .cnt { flex: none; font-size: 10px; color: var(--text-faint); }
.st-rail-add {
  margin-top: auto;
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius-sm);
  padding: 7px;
  font-size: 11.5px;
  color: var(--text-faint);
  gap: 5px;
}
.st-rail-add:hover { color: var(--accent-strong); border-color: var(--accent-line); background: none; }

.st-detail { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.st-d-row { flex: none; display: flex; gap: 8px; padding: 12px 14px 8px; flex-wrap: wrap; }
.st-f { display: flex; flex-direction: column; gap: 3px; font-size: 10px; color: var(--text-faint); flex: 1 1 140px; }
.st-f-wide { flex: 1.6 1 220px; }
.st-f input { font-size: 12px; padding: 4px 8px; }
.mono-in { font-family: var(--mono); }
.st-d-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 8px 14px 12px; }
.st-d-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: var(--text-faint);
  font-size: 12px;
  padding: 40px 0;
}
.st-sub { font-size: 10px; color: var(--text-faint); padding: 2px 2px 8px; }
.st-model { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
.st-m-name { flex: 1; min-width: 0; font-size: 11.5px; padding: 3px 8px; }
.st-m-num { flex: none; width: 88px; font-size: 11px; padding: 3px 6px; text-align: right; }
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
.st-m-del { flex: none; width: 22px; height: 22px; padding: 0; justify-content: center; color: var(--text-faint); }
.st-m-del:hover { color: var(--danger); border-color: var(--danger); }
.st-rowbtns { display: flex; gap: 6px; padding-top: 6px; flex-wrap: wrap; }
.st-rowbtns button { font-size: 11px; padding: 3px 9px; gap: 4px; }
.st-state { font-size: 10.5px; color: var(--text-faint); padding-top: 5px; word-break: break-all; }

/* 目录授权分区（全局独立）：与厂商区以边线相隔，默认折叠 */
.st-gz { flex: none; border-top: 1px solid var(--border); background: var(--bg-panel); }
.st-gz-head {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  justify-content: flex-start;
  padding: 8px 16px;
  background: none;
  border: none;
  border-radius: 0;
  font-size: 12px;
  color: var(--text);
}
.st-gz-head:hover { background: var(--bg-hover); border: none; }
.st-gz-head :deep(svg) { color: var(--text-faint); }
.st-gz-t { font-weight: 600; }
.st-gz-cnt { font-size: 10px; color: var(--text-faint); border: 1px solid var(--border); border-radius: 999px; padding: 0 6px; }
.st-gz-hint { flex: 1; min-width: 0; font-size: 10px; color: var(--text-faint);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.st-gz-chev { margin-left: auto; color: var(--text-faint); }
.st-gz-body { padding: 0 16px 10px; }
.st-gz-list { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.st-gz-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 3px 6px 3px 10px;
  background: var(--bg-elev);
}
.st-gz-p { font-family: var(--mono); font-size: 10.5px; color: var(--text-dim);
  max-width: 320px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.st-gz-chip button { width: 18px; height: 18px; padding: 0; justify-content: center;
  color: var(--text-faint); background: none; border: none; }
.st-gz-chip button:hover { color: var(--danger); }
.st-gz-clear { font-size: 10.5px; padding: 2px 10px; }
.st-gz-empty { font-size: 11px; color: var(--text-faint); padding: 2px 2px 4px; }

.st-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 16px;
  border-top: 1px solid var(--border);
}
.st-note { margin-right: auto; font-family: var(--mono); font-size: 9.5px; color: var(--text-faint); }
.st-err {
  font-size: 11px;
  color: var(--danger);
  max-width: 300px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
