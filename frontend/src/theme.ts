// 主题系统类型与取值域 —— 前后端共用语义（后端校验回落同款默认）
// appearance: 外观三态，挂 <html data-theme>；palette: 主题色五套，挂 <html data-palette>
export type Appearance = 'auto' | 'light' | 'dark'
export type PaletteId = 'ice' | 'teal' | 'green' | 'plat' | 'amber'

export const APPEARANCES: { id: Appearance; name: string }[] = [
  { id: 'auto', name: '跟随系统' },
  { id: 'dark', name: '夜巡' },
  { id: 'light', name: '日间' },
]

// dot 为色卡圆点展示色（夜间值；仅展示用，实际色值在 style.css 的 token 里）
export const PALETTES: { id: PaletteId; name: string; dot: string }[] = [
  { id: 'ice', name: '冰蓝', dot: '#5FB2FF' },
  { id: 'teal', name: '极光青', dot: '#2EC4B0' },
  { id: 'green', name: '磷光绿', dot: '#62E88A' },
  { id: 'plat', name: '铂金', dot: '#C9D1E0' },
  { id: 'amber', name: '暖琥珀', dot: '#F0A84B' },
]

export const PALETTE_IDS: PaletteId[] = PALETTES.map(p => p.id)
export const DEFAULT_PALETTE: PaletteId = 'ice'
