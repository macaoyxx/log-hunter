<script setup lang="ts">
import { computed } from 'vue'

// 命中高亮组件：把命中的关键字/正则片段标出来；marks 为用户右键格式标记
// （详情面板），同一标记词在全文所有出现处同色。分段渲染不走 v-html，
// 日志内容含 HTML 也不会注入
const props = defineProps<{
  text: string
  keywords?: string[]   // 精确关键字（区分大小写，与后端 bytes.Contains 一致）
  regex?: string        // 可选正则（JS 引擎跑，RE2 语法基本兼容）
  marks?: { word: string; color: number }[]  // 格式标记：词 → 槽位色 0-4
}>()

// hit：false=普通文本，'kw'=查询命中高亮，0-4=标记槽位色
interface Seg { t: string; hit: false | 'kw' | number }
interface Rg { a: number; b: number; tag: 'kw' | number }

const segs = computed<Seg[]>(() => {
  const text = props.text ?? ''
  if (!text) return []

  // 标记区间收集（逐词 indexOf，与关键字扫描同款）
  const mrgs: Rg[] = []
  for (const m of props.marks ?? []) {
    if (!m.word) continue
    let from = 0
    while (from <= text.length - m.word.length) {
      const i = text.indexOf(m.word, from)
      if (i < 0) break
      mrgs.push({ a: i, b: i + m.word.length, tag: m.color })
      from = i + m.word.length
    }
  }
  // 查询高亮区间收集
  const krgs: [number, number][] = []
  for (const kw of props.keywords ?? []) {
    if (!kw) continue
    let from = 0
    while (from <= text.length - kw.length) {
      const i = text.indexOf(kw, from)
      if (i < 0) break
      krgs.push([i, i + kw.length])
      from = i + kw.length
    }
  }
  if (props.regex) {
    try {
      const re = new RegExp(props.regex, 'g')
      let m: RegExpExecArray | null
      let guard = 0
      while ((m = re.exec(text)) !== null && guard++ < 5000) {
        if (m[0].length > 0) krgs.push([m.index, m.index + m[0].length])
        else re.lastIndex++
      }
    } catch { /* 非法正则不高亮 */ }
  }

  if (!mrgs.length && !krgs.length) return [{ t: text, hit: false }]

  // 标记区间排序为互不相交（重叠处前段用先到者颜色、后段用后到者颜色）
  mrgs.sort((x, y) => x.a - y.a || x.b - y.b)
  const mm: Rg[] = []
  for (const r of mrgs) {
    const last = mm[mm.length - 1]
    if (last && r.a < last.b) {
      if (r.b > last.b) mm.push({ a: last.b, b: r.b, tag: r.tag })
    } else {
      mm.push({ ...r })
    }
  }
  // 查询高亮减去标记覆盖（标记优先：用户主动标的更醒目）
  const kwLeft: Rg[] = []
  for (const [a0, b0] of krgs) {
    let s = a0
    for (const m of mm) {
      if (m.b <= s) continue
      if (m.a >= b0) break
      if (m.a > s) kwLeft.push({ a: s, b: Math.min(m.a, b0), tag: 'kw' })
      s = Math.max(s, m.b)
      if (s >= b0) break
    }
    if (s < b0) kwLeft.push({ a: s, b: b0, tag: 'kw' })
  }

  const all = [...mm, ...kwLeft].sort((x, y) => x.a - y.a || x.b - y.b)
  const out: Seg[] = []
  let pos = 0
  for (const r of all) {
    if (r.a > pos) out.push({ t: text.slice(pos, r.a), hit: false })
    out.push({ t: text.slice(r.a, r.b), hit: r.tag })
    pos = Math.max(pos, r.b)
  }
  if (pos < text.length) out.push({ t: text.slice(pos), hit: false })
  return out
})
</script>

<template>
  <span class="hl-root">
    <template v-for="(s, i) in segs" :key="i">
      <mark
        v-if="s.hit !== false" class="hl-mark"
        :class="typeof s.hit === 'number' ? 'mark-' + s.hit : ''"
      >{{ s.t }}</mark>
      <template v-else>{{ s.t }}</template>
    </template>
  </span>
</template>

<style scoped>
.hl-mark {
  background: var(--hl-bg, rgba(245, 165, 36, .35));
  color: var(--hl-fg, #ffd479);
  border-radius: 2px;
  padding: 0 1px;
  font-weight: 600;
}
</style>
