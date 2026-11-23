/**
 * @vitest-environment happy-dom
 */
// AI 设置面板的草稿逻辑回归（v0.4.1）：保存所见即所得、填名预填、数字归一、
// 视觉通道级联与引用自愈、打开时的重拉。后端绑定全部打桩——只验前端草稿，
// 不碰 ai.json；落盘侧的契约由 ai_test.go 的 TestSetAiConfigKeepsUserEdits 把关。
//
// 不用 @vue/test-utils：它只为本文件提供「挂载 / 找元素 / 触发事件」，却依赖 js-beautify，
// 后者带进 glob / minimatch 的安全通告（含高危 GHSA-5j98-mcp5-4vw2）。下面几十行小工具
// 用 createApp 加原生 DOM 事件覆盖同样的需求，测试依赖只剩 vitest 与 happy-dom
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { conf } from '../../../wailsjs/go/models'
import { AiLookupCaps, GetAiConfig, SetAiConfig } from '../../../wailsjs/go/main/App'
import AiSettings from '../AiSettings.vue'

vi.mock('../../../wailsjs/go/main/App', () => ({
  AiPullModels: vi.fn(),
  AiLookupCaps: vi.fn(),
  GetAiConfig: vi.fn(),
  SetAiConfig: vi.fn(),
  TestAiConn: vi.fn(),
}))

const model = (name: string, extra: Partial<conf.Model> = {}) => conf.Model.createFrom({ name, ...extra })
const profile = (id: string, name: string, models: conf.Model[]) =>
  conf.Profile.createFrom({ id, name, baseUrl: 'http://x', apiKey: '', models })
const baseConf = (profiles: conf.Profile[], over: Partial<conf.Conf> = {}) =>
  conf.Conf.createFrom({ profiles, ctxTokens: 1048576, maxOutTokens: 8192, ...over })
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v))
// 能力表打桩：与后端无关，只按名字给确定的预填值
const capsOf = (name: string) => {
  if (name === 'gpt-4o') return model(name, { ctx: 131072, vision: true, maxOut: 16384 })
  if (name.includes('vl')) return model(name, { vision: true })
  return model(name)
}

type ErrHandler = (err: unknown, instance: unknown, info: string) => void
type Panel = { el: HTMLDivElement; app: App; saved: Mock<() => void> }
let panels: Panel[] = []
let errorHandler: Mock<ErrHandler> | undefined

// 后端桩的默认行为：保存成功、能力表按名字返回、打开时重拉返回 config
function stubBackend(config: conf.Conf) {
  vi.mocked(SetAiConfig).mockResolvedValue(undefined)
  vi.mocked(AiLookupCaps).mockImplementation(async name => capsOf(name))
  vi.mocked(GetAiConfig).mockResolvedValue(clone(config) as conf.Conf)
}
function mountPanel(config: conf.Conf): Panel {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const saved = vi.fn()
  const app = createApp(AiSettings, { config: clone(config) as conf.Conf, onSaved: saved })
  app.config.errorHandler = errorHandler
  app.mount(el)
  const panel = { el, app, saved }
  panels.push(panel)
  return panel
}
// 打开设置，并等「打开时重拉」落定后再断言
async function openSettings(config: conf.Conf) {
  stubBackend(config)
  const panel = mountPanel(config)
  await flush()
  return panel
}
// 「重拉挂起」变体：返回放行函数，用于验证「重拉返回前用户已编辑」的竞态
async function openSettingsWithPendingRefetch(snapshot: conf.Conf) {
  stubBackend(snapshot)
  let release!: (c: conf.Conf) => void
  vi.mocked(GetAiConfig).mockReturnValueOnce(new Promise(resolve => { release = resolve }))
  const panel = mountPanel(snapshot)
  return { panel, releaseRefetch: (c: conf.Conf) => { release(clone(c)) } }
}

// 等已排队的微任务与已 resolve 的 promise 链落定（作用等同 flushPromises）
const flush = () => new Promise<void>(resolve => { setTimeout(resolve, 0) })
const qa = <T extends Element>(root: Element, sel: string) => Array.from(root.querySelectorAll(sel)) as unknown as T[]

// 模拟用户输入：改值后派发对应事件——input 驱动 v-model 与 @input，change 驱动 @change、下拉与复选框
async function type(el: HTMLInputElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
}
async function commit(el: HTMLInputElement) {
  el.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
}
async function pick(el: HTMLSelectElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
}
async function setChecked(el: HTMLInputElement, checked: boolean) {
  el.checked = checked
  el.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
}
async function click(el: Element) {
  el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  await nextTick()
}

// 面板里的控件定位（类名取自 AiSettings.vue 的模板）
const w = (p: Panel) => ({
  rail: () => qa(p.el, '.st-rail-item'),
  name: (i: number) => qa<HTMLInputElement>(p.el, '.st-m-name')[i],
  ctx: (i: number) => qa<HTMLInputElement>(p.el, '.st-m-num')[2 * i],
  maxOut: (i: number) => qa<HTMLInputElement>(p.el, '.st-m-num')[2 * i + 1],
  vis: (i: number) => qa<HTMLInputElement>(p.el, '.st-m-vis input')[i],
  del: (i: number) => qa(p.el, '.st-m-del')[i],
  btn: (text: string) => qa<HTMLButtonElement>(p.el, 'button').find(b => (b.textContent ?? '').trim().startsWith(text))!,
  mainSelects: () => qa<HTMLSelectElement>(qa(p.el, '.st-g')[0], 'select'),
  visionSelects: () => qa<HTMLSelectElement>(qa(p.el, '.st-g')[1], 'select'),
  globalInputs: () => qa<HTMLInputElement>(qa(p.el, '.st-g')[3], 'input'),
  dirty: () => !!p.el.querySelector('.st-dirty'),
})
const optionTexts = (sel: HTMLSelectElement) => Array.from(sel.options).map(o => o.textContent?.trim())

const save = async (p: Panel) => {
  await click(w(p).btn('保存'))
  await flush()
}
const savedPayload = (call = 0) => clone(vi.mocked(SetAiConfig).mock.calls[call][0])

beforeEach(() => {
  errorHandler = undefined
  vi.clearAllMocks()
})
afterEach(() => {
  panels.forEach(p => { p.app.unmount(); p.el.remove() })
  panels = []
})

describe('保存所见即所得（v0.4.1 根因的前端一侧）', () => {
  it('勾视觉且数字留空的模型：保存载荷原样，vision 为 true 且不带数字键', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [model('GLM-5.3-Flash')])]))
    await setChecked(w(p).vis(0), true)
    expect(w(p).dirty()).toBe(true)
    await save(p)
    expect(savedPayload().profiles[0].models).toEqual([{ name: 'GLM-5.3-Flash', vision: true }])
    expect(p.saved).toHaveBeenCalledTimes(1)
  })

  it('数字框清空或非法输入等同留空，小数截断：载荷里不出现空串', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [model('m1', { ctx: 200000, maxOut: 8192 })])]))
    await type(w(p).ctx(0), '')
    await type(w(p).maxOut(0), 'abc')
    await type(w(p).globalInputs()[0], '1.9')
    await save(p)
    const payload = savedPayload()
    expect(payload.profiles[0].models).toEqual([{ name: 'm1' }])
    expect(payload.ctxTokens).toBe(1)
    expect(JSON.stringify(payload)).not.toMatch(/"(ctx|maxOut|ctxTokens|maxOutTokens)":""/)
  })
})

describe('填名预填', () => {
  it('新增行敲定模型名时预填一次；之后再改名不重复预填', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [])]))
    await click(w(p).btn('手填模型'))
    await type(w(p).name(0), 'gpt-4o')
    await commit(w(p).name(0))
    await flush()
    expect(AiLookupCaps).toHaveBeenCalledTimes(1)
    expect(AiLookupCaps).toHaveBeenCalledWith('gpt-4o')
    expect(w(p).ctx(0).value).toBe('131072')
    expect(w(p).maxOut(0).value).toBe('16384')
    expect(w(p).vis(0).checked).toBe(true)

    await type(w(p).name(0), 'gpt-4o-mini')
    await commit(w(p).name(0))
    await flush()
    expect(AiLookupCaps).toHaveBeenCalledTimes(1)
  })

  it('空名提交不消耗预填：先把默认名清空失焦，回头填真名仍会预填', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [])]))
    await click(w(p).btn('手填模型'))
    await type(w(p).name(0), '')
    await commit(w(p).name(0))
    await flush()
    expect(AiLookupCaps).not.toHaveBeenCalled()

    await type(w(p).name(0), 'gpt-4o')
    await commit(w(p).name(0))
    await flush()
    expect(AiLookupCaps).toHaveBeenCalledTimes(1)
    expect(w(p).ctx(0).value).toBe('131072')
  })

  it('用户先于预填填的数字与勾选不被覆盖', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [])]))
    await click(w(p).btn('手填模型'))
    await type(w(p).ctx(0), '99999')
    await setChecked(w(p).vis(0), true)
    await type(w(p).name(0), 'gpt-4o')
    await commit(w(p).name(0))
    await flush()
    expect(w(p).ctx(0).value).toBe('99999')
    expect(w(p).vis(0).checked).toBe(true)
    expect(w(p).maxOut(0).value).toBe('16384') // 空格才补
  })

  it('保存前等在途预填落定', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [])]))
    let release!: (m: conf.Model) => void
    vi.mocked(AiLookupCaps).mockReturnValueOnce(new Promise(resolve => { release = resolve }))
    await click(w(p).btn('手填模型'))
    await type(w(p).name(0), 'gpt-4o')
    await commit(w(p).name(0))
    await click(w(p).btn('保存'))
    await nextTick()
    expect(SetAiConfig).not.toHaveBeenCalled()

    release(model('gpt-4o', { ctx: 131072 }))
    await flush()
    expect(SetAiConfig).toHaveBeenCalledTimes(1)
    expect(savedPayload().profiles[0].models[0].ctx).toBe(131072)
  })

  it('预填失败进 Vue errorHandler，不被静默吞掉', async () => {
    errorHandler = vi.fn<ErrHandler>()
    const p = await openSettings(baseConf([profile('p1', '甲', [])]))
    vi.mocked(AiLookupCaps).mockRejectedValueOnce(new Error('bridge down'))
    await click(w(p).btn('手填模型'))
    await type(w(p).name(0), 'whatever')
    await commit(w(p).name(0))
    await flush()
    expect(errorHandler).toHaveBeenCalledTimes(1)
    expect(String(errorHandler.mock.calls[0][0])).toContain('能力预填失败')
  })
})

describe('视觉通道级联与引用自愈', () => {
  const twoVendors = () => baseConf([
    profile('p1', '甲', [model('plain'), model('va', { vision: true })]),
    profile('p2', '乙', [model('vb', { vision: true })]),
  ])
  // 存量悬空：视觉通道指向甲的 X，但 X 的视觉勾选被旧版保存写回了 false
  const danglingConf = () => baseConf([
    profile('p1', '甲', [model('X'), model('va', { vision: true })]),
    profile('p2', '乙', [model('vb', { vision: true })]),
  ], { visionProfile: 'p1', visionModel: 'X' })

  it('未选视觉厂商时只有提示项；选厂商后只列该厂商的视觉模型', async () => {
    const p = await openSettings(twoVendors())
    expect(optionTexts(w(p).visionSelects()[1])).toEqual(['（请先选视觉厂商）'])
    await pick(w(p).visionSelects()[0], 'p1')
    expect(optionTexts(w(p).visionSelects()[1])).toEqual(['（不指定）', 'va']) // 乙的 vb 不出现
    await pick(w(p).visionSelects()[0], 'p2')
    expect(optionTexts(w(p).visionSelects()[1])).toEqual(['（不指定）', 'vb'])
  })

  it('已选视觉模型有效时：切厂商、取消勾选、删行都会清空', async () => {
    const p = await openSettings(twoVendors())
    await pick(w(p).visionSelects()[0], 'p1')
    await pick(w(p).visionSelects()[1], 'va')

    await pick(w(p).visionSelects()[0], 'p2')
    expect(w(p).visionSelects()[1].value).toBe('')

    await pick(w(p).visionSelects()[0], 'p1')
    await pick(w(p).visionSelects()[1], 'va')
    await setChecked(w(p).vis(1), false) // 取消 va 的视觉
    expect(w(p).visionSelects()[1].value).toBe('')

    await setChecked(w(p).vis(1), true)
    await pick(w(p).visionSelects()[1], 'va')
    await click(w(p).del(1)) // 删掉 va 所在行
    expect(w(p).visionSelects()[1].value).toBe('')
  })

  it('切换视觉厂商后，新厂商里同名的视觉模型保留', async () => {
    const p = await openSettings(baseConf([
      profile('p1', '甲', [model('X', { vision: true })]),
      profile('p2', '乙', [model('X', { vision: true })]),
    ], { visionProfile: 'p1', visionModel: 'X' }))
    await pick(w(p).visionSelects()[0], 'p2')
    expect(w(p).visionSelects()[1].value).toBe('X')
  })

  it('存量悬空视觉模型（旧版保存把勾选写回了 false）：打开不产生「草稿未保存」，兜底显示真名，保存后值保留', async () => {
    const p = await openSettings(danglingConf())
    expect(w(p).dirty()).toBe(false)
    expect(w(p).visionSelects()[1].value).toBe('X')
    expect(optionTexts(w(p).visionSelects()[1])).toContain('X（未勾视觉）')

    await save(p)
    expect(savedPayload().visionModel).toBe('X')
  })

  it('存量悬空视觉模型：切换视觉厂商后清空，不留下跨厂商错配', async () => {
    const p = await openSettings(danglingConf())
    await pick(w(p).visionSelects()[0], 'p2')
    expect(w(p).visionSelects()[1].value).toBe('')
    await save(p)
    expect(savedPayload().visionModel).toBe('')
  })

  it('悬空模型补勾视觉后回到正常选项，已选值不变', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [model('X')])], { visionProfile: 'p1', visionModel: 'X' }))
    await setChecked(w(p).vis(0), true)
    expect(w(p).visionSelects()[1].value).toBe('X')
    expect(optionTexts(w(p).visionSelects()[1])).toEqual(['（不指定）', 'X'])
  })

  it('兜底说明按实际情况区分：该厂商有此模型但未勾视觉 / 该厂商没有此模型', async () => {
    const p = await openSettings(baseConf([profile('p1', '甲', [model('va', { vision: true })])], { visionProfile: 'p1', visionModel: 'ghost' }))
    expect(optionTexts(w(p).visionSelects()[1])).toEqual(['（不指定）', 'ghost（不在该厂商）', 'va'])
    expect(w(p).visionSelects()[1].value).toBe('ghost') // 同样不在打开时被改掉
  })
})

describe('主通道引用自愈', () => {
  it('切主厂商时旧主模型不在新厂商里即清空；删当前主模型所在行即清空', async () => {
    const p = await openSettings(baseConf([
      profile('p1', '甲', [model('a1')]),
      profile('p2', '乙', [model('b1')]),
    ]))
    await pick(w(p).mainSelects()[0], 'p1')
    await pick(w(p).mainSelects()[1], 'a1')
    await pick(w(p).mainSelects()[0], 'p2')
    expect(w(p).mainSelects()[1].value).toBe('')

    await pick(w(p).mainSelects()[0], 'p1')
    await pick(w(p).mainSelects()[1], 'a1')
    await click(w(p).del(0))
    expect(w(p).mainSelects()[1].value).toBe('')
  })
})

describe('打开设置时的重拉', () => {
  it('重拉返回前已有编辑：草稿不被替换，没动过的授权目录单独跟随最新', async () => {
    const { panel: p, releaseRefetch } = await openSettingsWithPendingRefetch(baseConf([]))
    await click(w(p).btn('添加厂商')) // 用户先加了一个厂商
    releaseRefetch(baseConf([], { grantedDirs: ['D:/new'] }))
    await flush()
    expect(w(p).rail()).toHaveLength(1)
    await save(p)
    expect(savedPayload().profiles).toHaveLength(1)
    expect(savedPayload().grantedDirs).toEqual(['D:/new'])
  })

  it('重拉返回前已改过授权目录：不被最新值覆盖', async () => {
    const { panel: p, releaseRefetch } = await openSettingsWithPendingRefetch(baseConf([], { grantedDirs: ['D:/old'] }))
    await click(w(p).btn('目录访问授权'))
    await click(p.el.querySelector('.st-gz-clear')!) // 一键清空
    releaseRefetch(baseConf([], { grantedDirs: ['D:/old', 'D:/new'] }))
    await flush()
    await save(p)
    expect(savedPayload().grantedDirs).toEqual([])
  })

  it('没有编辑：按后端配置整体刷新', async () => {
    const { panel: p, releaseRefetch } = await openSettingsWithPendingRefetch(baseConf([]))
    releaseRefetch(baseConf([profile('p1', '后端新加', [])]))
    await flush()
    expect(w(p).rail()).toHaveLength(1)
    expect(w(p).dirty()).toBe(false)
  })
})
