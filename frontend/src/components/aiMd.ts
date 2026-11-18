// 轻量 markdown 渲染（AI 诊断回复专用）——全部经 h() 构造 VNode，零 v-html：
// AI 输出按不可信内容处理（与日志高亮同一纪律），样式类由 AiPanel 的 :deep() 提供。
// 支持子集：##/### 标题、无序/有序列表、**粗体**、*斜体*、~~删除线~~、`行内码`、
// [链接](url)、围栏代码块（语言标注）、表格（| a | b | + |---|）、> 引用块、--- 分割线、
// 引用 chip（[行 N] / [文件名:行号]，越界标红——抓编造引用）、「查询:」建议 chip
import { h, type VNode } from 'vue'

export interface MdHandlers {
  onRowRef(n: number): void // [行 N]，n 为 1-based 展示行号
  onFileRef(file: string, line: number): void
  onQuery(kw: string): void
  validRow(n: number): boolean // 行号在当前结果集内
}

// inline 分词：粗体 / 斜体 / 删除线 / 行内码 / 链接 / [行 N] / [文件.ext:行号] / 查询: 关键字（到行尾，可含空格）
const INLINE_RE = /\*\*([^*]+)\*\*|\*([^*\n]+)\*|~~([^~]+)~~|`([^`\n]+)`|\[([^\]\n]+)\]\((https?:[^)\s]+)\)|\[行\s*(\d+)\]|\[([^\[\]:]+?\.[A-Za-z0-9]+):\s*(\d+)\]|查询[:：]\s*([^\n`]+)/g

function renderInline(text: string, hb: MdHandlers): (VNode | string)[] {
  const out: (VNode | string)[] = []
  let last = 0
  text = text.replace(/\r/g, '')
  INLINE_RE.lastIndex = 0
  let mt: RegExpExecArray | null
  let m: RegExpExecArray | null
  while ((m = INLINE_RE.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index))
    if (m[1] !== undefined) {
      out.push(h('strong', null, m[1]))
    } else if (m[2] !== undefined) {
      out.push(h('em', null, m[2]))
    } else if (m[3] !== undefined) {
      out.push(h('del', { class: 'md-del' }, m[3]))
    } else if (m[4] !== undefined) {
      out.push(h('code', { class: 'md-code' }, m[4]))
    } else if (m[5] !== undefined && m[6] !== undefined) {
      out.push(h('a', { class: 'md-link', href: m[6], target: '_blank', rel: 'noopener noreferrer' }, m[5]))
    } else if (m[7] !== undefined) {
      const n = parseInt(m[7], 10)
      out.push(h('button', {
        class: ['md-ref', !hb.validRow(n) && 'md-ref-bad'],
        title: hb.validRow(n) ? `跳到结果第 ${n} 行` : `行号 ${n} 不在当前结果集，疑似编造`,
        onClick: () => { if (hb.validRow(n)) hb.onRowRef(n) },
        type: 'button',
      }, `[行 ${n}]`))
    } else if (m[8] !== undefined && m[9] !== undefined) {
      const file = m[8], line = parseInt(m[9], 10)
      out.push(h('button', {
        class: 'md-ref',
        title: `${file}:${line}（打开上下文）`,
        onClick: () => hb.onFileRef(file, line),
        type: 'button',
      }, `[${file}:${line}]`))
    } else if (m[10] !== undefined) {
      const kw = m[10].trim().replace(/[。，；、）]$/, '') // 行尾标点不入查询词
      if (kw) {
        out.push(h('button', {
          class: 'md-q',
          title: `用「${kw}」立即查询`,
          onClick: () => hb.onQuery(kw),
          type: 'button',
        }, `查询 ${kw}`))
      }
    }
    last = m.index + m[0].length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

// 表格行解析：`| a | b |` → ['a','b']（容忍首尾竖线与单元格内的转义内容）
function tableRow(line: string): string[] {
  let s = line.trim()
  if (s.startsWith('|')) s = s.slice(1)
  if (s.endsWith('|')) s = s.slice(0, -1)
  return s.split('|').map(c => c.trim())
}
function isTableDivider(line: string): boolean {
  return /^\|?[\s:|-]+\|[\s:|-]*$/.test(line.trim()) && line.includes('-')
}

// renderMd 块级解析：围栏代码块 → 表格 → 标题 → 引用块 → 列表 → 分割线 → 段落
export function renderMd(text: string, hb: MdHandlers): VNode[] {
  const out: VNode[] = []
  const lines = text.replace(/\r/g, '').split('\n')
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    // 围栏代码块（```lang … ```）
    const fence = /^```(\w*)/.exec(line)
    if (fence) {
      const lang = fence[1] || ''
      const buf: string[] = []
      i++
      while (i < lines.length && !lines[i].startsWith('```')) { buf.push(lines[i]); i++ }
      i++ // 跳过收尾 ```
      out.push(h('figure', { class: 'md-pre-wrap' }, [
        lang ? h('figcaption', { class: 'md-pre-lang' }, lang) : null,
        h('pre', { class: 'md-pre' }, [h('code', null, buf.join('\n'))]),
      ]))
      continue
    }
    // 表格：当前行含 | 且下一行是分隔行
    if (line.includes('|') && i + 1 < lines.length && isTableDivider(lines[i + 1])) {
      const head = tableRow(line)
      i += 2
      const rows: string[][] = []
      while (i < lines.length && lines[i].includes('|') && lines[i].trim()) {
        rows.push(tableRow(lines[i])); i++
      }
      out.push(h('table', { class: 'md-table' }, [
        h('thead', null, [h('tr', null, head.map((c, ci) => h('th', { key: ci }, renderInline(c, hb))))]),
        h('tbody', null, rows.map((r, ri) => h('tr', { key: ri }, head.map((_, ci) =>
          h('td', { key: ci }, renderInline(r[ci] ?? '', hb)))))),
      ]))
      continue
    }
    // 标题（## 起，# 太大不用于侧栏）
    const hm = /^(#{1,4})\s+(.*)$/.exec(line)
    if (hm) {
      const level = Math.min(hm[1].length + 2, 6) // ## → h4，压制层级感
      out.push(h(`h${level}`, { class: 'md-h' }, renderInline(hm[2], hb)))
      i++
      continue
    }
    // 分割线（--- / *** 单独行，防与表格分隔行混淆已在表格分支先处理）
    if (/^(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      out.push(h('hr', { class: 'md-hr' }))
      i++
      continue
    }
    // 引用块（> 连续行）
    if (/^>\s?/.test(line)) {
      const buf: string[] = []
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        buf.push(lines[i].replace(/^>\s?/, '')); i++
      }
      out.push(h('blockquote', { class: 'md-quote' }, buf.flatMap(b => renderInline(b, hb))))
      continue
    }
    // 无序列表（连续 - / * 行）
    if (/^[-*]\s+/.test(line)) {
      const items: VNode[] = []
      while (i < lines.length && /^[-*]\s+/.test(lines[i])) {
        items.push(h('li', null, renderInline(lines[i].replace(/^[-*]\s+/, ''), hb)))
        i++
      }
      out.push(h('ul', { class: 'md-ul' }, items))
      continue
    }
    // 有序列表（连续 1. 2. 行——序号连续性从 1 起即可，模型输出普遍规范）
    if (/^\d+[.、]\s+/.test(line)) {
      const items: VNode[] = []
      while (i < lines.length && /^\d+[.、]\s+/.test(lines[i])) {
        items.push(h('li', null, renderInline(lines[i].replace(/^\d+[.、]\s+/, ''), hb)))
        i++
      }
      out.push(h('ol', { class: 'md-ol' }, items))
      continue
    }
    // 空行跳过；其余为段落（连续非空行合并，直到块级语法出现）
    if (!line.trim()) { i++; continue }
    const buf: string[] = [line]
    i++
    while (i < lines.length && lines[i].trim() && !lines[i].startsWith('```') &&
      !/^(#{1,4})\s/.test(lines[i]) && !/^[-*]\s+/.test(lines[i]) && !/^\d+[.、]\s+/.test(lines[i]) &&
      !/^>\s?/.test(lines[i]) && !/^(-{3,}|\*{3,})\s*$/.test(lines[i]) &&
      !(lines[i].includes('|') && i + 1 < lines.length && isTableDivider(lines[i + 1]))) {
      buf.push(lines[i]); i++
    }
    out.push(h('p', { class: 'md-p' }, renderInline(buf.join('\n'), hb)))
  }
  return out
}
