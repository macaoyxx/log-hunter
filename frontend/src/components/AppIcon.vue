<script setup lang="ts">
// 内联 SVG 图标：内容来自 icons.ts 静态字符串（无用户输入），尺寸由 size 属性控制
import { ICONS } from '../icons'
withDefaults(defineProps<{ name: string; size?: number }>(), { size: 13 })
</script>

<template>
  <svg class="ic" :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor"
    stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"
    v-html="ICONS[name] ?? ''"></svg>
</template>

<style scoped>
.ic { flex: none; display: inline-block; vertical-align: -2px; }
</style>
