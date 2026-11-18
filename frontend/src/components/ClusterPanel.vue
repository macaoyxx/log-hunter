<script setup lang="ts">
// 错误聚类面板：结果区上方的可折叠横条带（与时间直方图同级）。
// 签名元素是组行的级别信号色占比条——组行背景按 count/最大组 铺 lvl 色带，
// 「错误的质量分布」一眼直读，这是聚类区别于普通列表的本质呈现。
// 口径诚实：采样时明示参与行数与总行数；「其他」组不可跳转（无首行锚点）。
import { computed, ref } from 'vue'
import AppIcon from './AppIcon.vue'
import { logscan } from '../../wailsjs/go/models'

// 级别显示名（与 ResultTable/SideRail 同款本地定义）
const LEVEL_NAMES = ['TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL']

const props = defineProps<{
  report: logscan.ClusterReport | null
  clustering: boolean
  progress: { done: number; total: number } | null
  aiReady: boolean // AI 已配置（false 时按钮转去设置）
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'jump', idx: number): void // 结果集全局下标（0-based，跳结果表）
  (e: 'open-context', fileId: number, offset: number): void // 非结果集行开上下文面板
  (e: 'ai-diagnose'): void
  (e: 'open-settings'): void
}>()

const expanded = ref<number>(-1) // 展开的组下标（-1 无）
// 组渲染截断：>100 组直出在低端机卡顿，先渲 100 + 展开全部
const RENDER_CAP = 100
const showAll = ref(false)
const shownGroups = computed(() =>
  showAll.value || (props.report?.groups.length ?? 0) <= RENDER_CAP
    ? props.report?.groups ?? []
    : props.report!.groups.slice(0, RENDER_CAP))

// 占比条宽度基准：最大组（首组，排序保证）
const maxCount = computed(() => props.report?.groups?.[0]?.count ?? 0)

function pct(n: number): string {
  if (!maxCount.value) return '0'
  return (n / maxCount.value * 100).toFixed(1)
}
// 组内样本可点击：Idx>=0 跳结果表；否则开上下文面板（SideCluster 的组）
function sampleClick(s: logscan.ClusterSample) {
  if (s.idx >= 0) emit('jump', s.idx)
  else if (s.offset >= 0) emit('open-context', s.fileId, s.offset)
}
function fmtT(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts / 1e6)
  return d.toTimeString().slice(0, 8)
}
function fmtN(n: number): string { return n.toLocaleString() }
</script>

<template>
  <div class="cluster-strip">
    <div class="cs-head">
      <span class="cs-title"><AppIcon name="layers" :size="13" />错误聚类</span>
      <span v-if="clustering" class="cs-meta">聚类中… {{ fmtN(progress?.done ?? 0) }} / {{ fmtN(progress?.total ?? 0) }} 行</span>
      <template v-else-if="report">
        <span class="cs-meta">
          {{ report.groups.length }} 组 · 参与 {{ fmtN(report.participated) }}/{{ fmtN(report.totalRows) }} 行
          <template v-if="report.sampled">（超限均匀采样，计数为采样口径）</template>
          · {{ report.costMs }} ms
        </span>
      </template>
      <span class="spacer"></span>
      <button class="cs-ai" :class="{ on: aiReady }" title="把聚类摘要交给内置日志分析专家取证诊断"
        @click="aiReady ? emit('ai-diagnose') : emit('open-settings')">
        <AppIcon name="sparkles" :size="12" />AI 诊断
      </button>
      <button class="cs-x" title="收起聚类" @click="emit('close')"><AppIcon name="x" :size="12" /></button>
    </div>

    <div v-if="clustering" class="cs-bar"><div class="cs-fill"
      :style="{ width: (progress && progress.total > 0 ? progress.done / progress.total * 100 : 4) + '%' }"></div></div>

    <div v-else-if="report" class="cs-list">
      <div v-for="(g, gi) in shownGroups" :key="gi" class="cs-group" :class="{ open: expanded === gi }">
        <!-- 组行：级别色占比条铺底（count/最大组），点击展开样本 -->
        <button class="cs-row" :style="{ '--fill': pct(g.count) + '%' }" @click="expanded = expanded === gi ? -1 : gi">
          <span class="g-bar" :class="'g-lvl-' + (g.level >= 0 ? g.level : 'x')"></span>
          <span class="g-count">{{ fmtN(g.count) }}</span>
          <span class="g-lvl">
            <i v-if="g.level >= 0" class="lvl" :class="'lvl-' + g.level">{{ LEVEL_NAMES[g.level] ?? '?' }}</i>
          </span>
          <span class="g-tpl" :title="g.template">{{ g.template }}</span>
          <span class="g-time" v-if="g.firstTs">{{ fmtT(g.firstTs) }}<template v-if="g.lastTs !== g.firstTs"> ~ {{ fmtT(g.lastTs) }}</template></span>
          <AppIcon v-if="g.samples && g.samples.length" name="chevron-down" :size="12" class="g-exp"
            :class="{ flip: expanded === gi }" />
        </button>
        <!-- 样本行（最多 3 条）：点击跳结果表 / 开上下文面板 -->
        <div v-if="expanded === gi && g.samples?.length" class="g-samples">
          <button v-for="(s, si) in g.samples" :key="si" class="g-sample"
            :title="s.idx >= 0 ? `跳到结果第 ${s.idx + 1} 行` : `${s.file}:${s.lineNo}（开上下文）`"
            @click="sampleClick(s)">
            <span class="s-anchor">{{ s.idx >= 0 ? `[行 ${s.idx + 1}]` : `[${s.file}:${s.lineNo}]` }}</span>
            <span class="s-text">{{ s.text }}</span>
          </button>
        </div>
      </div>
      <button v-if="report.groups.length > RENDER_CAP && !showAll" class="cs-more"
        @click="showAll = true">
        展开全部 {{ report.groups.length }} 组（当前显示前 {{ RENDER_CAP }}） ▾
      </button>
    </div>
  </div>
</template>

<style scoped>
.cluster-strip {
  flex: none;
  display: flex;
  flex-direction: column;
  max-height: 258px;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: var(--sheen);
  overflow: hidden;
}
.cs-head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px 6px 12px;
}
.cs-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text);
  user-select: none;
}
.cs-title :deep(svg) { color: var(--accent-strong); }
.cs-meta {
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--text-faint);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.spacer { flex: 1; }
.cs-ai {
  flex: none;
  font-size: 11.5px;
  padding: 3.5px 10px;
  color: var(--text-dim);
}
.cs-ai.on {
  color: var(--accent-strong);
  border-color: var(--accent-line);
  background: var(--accent-soft);
}
.cs-x {
  flex: none;
  background: none;
  border: none;
  color: var(--text-faint);
  padding: 3px;
}
.cs-x:hover { color: var(--text); border: none; background: var(--bg-hover); }

/* 聚类进度条 */
.cs-bar { flex: none; height: 4px; margin: 0 12px 8px; border-radius: 2px; background: var(--bg-elev); overflow: hidden; }
.cs-fill { height: 100%; background: var(--accent-strong); transition: width .15s; }

.cs-more {
  width: 100%; justify-content: center;
  font-size: 11px; padding: 5px 8px; color: var(--text-dim);
}
.cs-more:hover { color: var(--accent-strong); border-color: var(--accent-line); }
.cs-list {
  flex: 1;
  min-height: 120px;
  overflow-y: auto;
  padding: 0 8px 8px;
}
.cs-group { margin-bottom: 2px; }
/* 组行：占比条为背景层（--fill 宽度、组级别色），内容浮于其上 */
.cs-row {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  justify-content: flex-start;
  min-height: 30px;
  padding: 3px 8px 3px 10px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  text-align: left;
  cursor: pointer;
}
.cs-row:hover { background: var(--bg-hover); border: none; }
.g-bar {
  position: absolute;
  left: 0;
  top: 15%;
  bottom: 15%;
  width: var(--fill);
  border-radius: 3px;
  pointer-events: none;
}
.g-lvl-0 { background: var(--lvl-0-bg); }
.g-lvl-1 { background: var(--lvl-1-bg); }
.g-lvl-2 { background: var(--lvl-2-bg); }
.g-lvl-3 { background: var(--lvl-3-bg); }
.g-lvl-4 { background: var(--lvl-4-bg); }
.g-lvl-5 { background: var(--lvl-5-bg); }
.g-lvl-x { background: var(--bg-hover); }
.g-count {
  position: relative;
  flex: none;
  min-width: 56px;
  font-family: var(--mono);
  font-size: 11px;
  font-weight: 600;
  color: var(--text);
  text-align: right;
}
.g-lvl { position: relative; flex: none; }
.g-tpl {
  position: relative;
  flex: 1;
  min-width: 0;
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.g-time {
  position: relative;
  flex: none;
  font-family: var(--mono);
  font-size: 10px;
  color: var(--text-faint);
}
.g-exp { position: relative; flex: none; color: var(--text-faint); transition: transform .15s; }
.g-exp.flip { transform: rotate(180deg); }

.g-samples {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 0 8px 4px 26px;
}
.g-sample {
  display: flex;
  align-items: baseline;
  gap: 8px;
  justify-content: flex-start;
  width: 100%;
  min-height: 24px;
  padding: 2px 8px;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  text-align: left;
  cursor: pointer;
}
.g-sample:hover { border-color: var(--accent-line); background: var(--accent-soft); }
.s-anchor {
  flex: none;
  font-family: var(--mono);
  font-size: 10px;
  color: var(--accent-strong);
}
.s-text {
  flex: 1;
  min-width: 0;
  font-family: var(--mono);
  font-size: 10.5px;
  color: var(--text-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
