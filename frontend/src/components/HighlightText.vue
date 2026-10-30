<script setup lang="ts">
import { computed } from 'vue'

// 命中高亮组件：把命中的关键字/正则片段标出来。
// 分段渲染不走 v-html，日志内容含 HTML 也不会注入
const props = defineProps<{
  text: string
  keywords?: string[]   // 精确关键字（区分大小写，与后端 bytes.Contains 一致）
  regex?: string        // 可选正则（JS 引擎跑，RE2 语法基本兼容）
}>()

interface Seg { t: string; hit: boolean }

const segs = computed<Seg[]>(() => {
  const text = props.text ?? ''
  if (!text) return []
  const ranges: [number, number][] = []

  for (const kw of props.keywords ?? []) {
    if (!kw) continue
    let from = 0
    while (from <= text.length - kw.length) {
      const i = text.indexOf(kw, from)
      if (i < 0) break
      ranges.push([i, i + kw.length])
      from = i + kw.length
    }
  }
  if (props.regex) {
    try {
      const re = new RegExp(props.regex, 'g')
      let m: RegExpExecArray | null
      let guard = 0
      while ((m = re.exec(text)) !== null && guard++ < 5000) {
        if (m[0].length > 0) ranges.push([m.index, m.index + m[0].length])
        else re.lastIndex++
      }
    } catch { /* 非法正则不高亮 */ }
  }

  if (!ranges.length) return [{ t: text, hit: false }]
  ranges.sort((a, b) => a[0] - b[0] || a[1] - b[1])
  // 合并重叠区间
  const merged: [number, number][] = [ranges[0]]
  for (const r of ranges.slice(1)) {
    const last = merged[merged.length - 1]
    if (r[0] <= last[1]) {
      if (r[1] > last[1]) last[1] = r[1]
    } else {
      merged.push(r)
    }
  }
  const out: Seg[] = []
  let pos = 0
  for (const [a, b] of merged) {
    if (a > pos) out.push({ t: text.slice(pos, a), hit: false })
    out.push({ t: text.slice(a, b), hit: true })
    pos = b
  }
  if (pos < text.length) out.push({ t: text.slice(pos), hit: false })
  return out
})
</script>

<template>
  <span class="hl-root">
    <template v-for="(s, i) in segs" :key="i">
      <mark v-if="s.hit" class="hl-mark">{{ s.t }}</mark>
      <template v-else>{{ s.t }}</template>
    </template>
  </span>
</template>

<style scoped>
.hl-mark {
  background: rgba(245, 165, 36, .35);
  color: #ffd479;
  border-radius: 2px;
  padding: 0 1px;
  font-weight: 600;
}
</style>
