/**
 * MLNR 麻辣牛肉控制器主题管理
 * 支持三种模式：light（亮色）、dark（暗色）、auto（跟随 fnOS 系统主题）
 * auto 模式完全走浏览器侧通道（与 fnOS 官方测试代码 zdaitest001 一致）：
 *   localStorage fnos-theme-mode（'20'=深色 '10'=亮色）→ postMessage → matchMedia 兜底
 * 通过 document.documentElement 的 data-theme 属性切换 CSS 变量。
 *
 * 配色风格（palette）为独立维度：不改变主题模式逻辑，仅叠加 data-palette 属性
 * 覆盖 --c-primary/--c-accent 等强调色变量（明暗各自定义）。
 */

export type ThemeMode = 'light' | 'dark' | 'auto' | 'sepia' | 'midnight'

/** 解析后的实际应用主题（data-theme 取值） */
export type ResolvedTheme = 'light' | 'dark' | 'sepia' | 'midnight'

export interface ThemeOption {
  id: ThemeMode
  name: string
}

const STORAGE_KEY = 'mlnr-theme'
const PALETTE_KEY = 'mlnr-palette'
const CUSTOM_KEY = 'mlnr-palette-custom' // 自定义配色（#rrggbb）

/** 主题选项列表（sepia=护眼暖、midnight=午夜蓝，均为亮/暗之外的整体色调主题） */
export const themeOptions: ThemeOption[] = [
  { id: 'light', name: '亮色' },
  { id: 'dark', name: '暗色' },
  { id: 'sepia', name: '护眼暖' },
  { id: 'midnight', name: '午夜蓝' },
  { id: 'auto', name: '自动' },
]

/** 配色风格 */
export type PaletteId = 'default' | 'emerald' | 'ocean' | 'sunset' | 'grape' | 'rose' | 'custom'

export interface PaletteOption {
  id: PaletteId
  name: string
}

/** 配色风格列表（default=默认蓝，不叠加任何覆盖） */
export const palettes: PaletteOption[] = [
  { id: 'default', name: '默认蓝' },
  { id: 'emerald', name: '翡翠绿' },
  { id: 'ocean', name: '海盐青' },
  { id: 'sunset', name: '暖阳橙' },
  { id: 'grape', name: '葡萄紫' },
  { id: 'rose', name: '玫瑰粉' },
]

/** 获取当前存储的配色风格（custom=自定义 RGB，见 setCustomColor） */
export function getCurrentPalette(): PaletteId {
  const stored = localStorage.getItem(PALETTE_KEY)
  if (stored === 'custom') return 'custom'
  return palettes.some(p => p.id === stored) ? (stored as PaletteId) : 'default'
}

/** 获取自定义配色 hex（如 #3b82f6）；未设置/非法返回 null */
export function getCustomColor(): string | null {
  const v = localStorage.getItem(CUSTOM_KEY)
  return v && /^#[0-9a-fA-F]{6}$/.test(v) ? v.toLowerCase() : null
}

/** 设置自定义配色并持久化：data-palette=custom，强调色变量由内联样式覆盖（全主题生效） */
export function setCustomColor(hex: string): void {
  if (!/^#[0-9a-fA-F]{6}$/.test(hex)) return
  const h = hex.toLowerCase()
  localStorage.setItem(CUSTOM_KEY, h)
  localStorage.setItem(PALETTE_KEY, 'custom')
  document.documentElement.setAttribute('data-palette', 'custom')
  applyCustomVars(h)
}

/** 设置配色风格并持久化（仅叠加强调色，不影响明暗模式） */
export function setPalette(id: PaletteId): void {
  localStorage.setItem(PALETTE_KEY, id)
  applyPalette(id)
}

/** 应用配色风格到 DOM */
export function applyPalette(id: PaletteId): void {
  if (id === 'custom') {
    document.documentElement.setAttribute('data-palette', 'custom')
    applyCustomVars(getCustomColor() ?? '#3b82f6')
    return
  }
  clearCustomVars()
  document.documentElement.setAttribute('data-palette', id)
}

// ---------- 自定义 RGB 强调色 ----------
function hexToRgb(hex: string): [number, number, number] {
  const n = parseInt(hex.replace('#', ''), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

/** 当前解析主题是否为深色系（决定 soft 背景/文字推导方向） */
function isDarkResolvedTheme(): boolean {
  const t = document.documentElement.getAttribute('data-theme')
  return t === 'dark' || t === 'midnight'
}

/** 由主色推导 hover/soft 等衍生变量，写入 documentElement 内联样式（覆盖所有主题/预设） */
function applyCustomVars(hex: string): void {
  const c = hexToRgb(hex)
  const black: [number, number, number] = [0, 0, 0]
  const white: [number, number, number] = [255, 255, 255]
  const dark = isDarkResolvedTheme()
  const mix = (a: [number, number, number], b: [number, number, number], t: number): string =>
    `${Math.round(a[0] + (b[0] - a[0]) * t)} ${Math.round(a[1] + (b[1] - a[1]) * t)} ${Math.round(a[2] + (b[2] - a[2]) * t)}`
  const hover = mix(c, black, 0.18)               // 主色加深
  const soft = mix(c, dark ? black : white, dark ? 0.82 : 0.86)  // 底色化背景
  const softText = mix(c, dark ? white : black, dark ? 0.6 : 0.4)
  const accent = mix(c, dark ? white : black, dark ? 0.25 : 0.12) // 强调色与主色略区分
  const accentSoft = mix(c, dark ? black : white, 0.84)
  const st = document.documentElement.style
  st.setProperty('--c-primary', c.join(' '))
  st.setProperty('--c-primary-hover', hover)
  st.setProperty('--c-primary-soft', soft)
  st.setProperty('--c-primary-soft-text', softText)
  st.setProperty('--c-accent', accent)
  st.setProperty('--c-accent-soft', accentSoft)
  st.setProperty('--c-accent-soft-text', softText)
  st.setProperty('--c-download', accent)
  st.setProperty('--c-download-soft', accentSoft)
  st.setProperty('--c-download-soft-text', softText)
}

/** 清除自定义强调色内联样式（切回预设时恢复 CSS 变量控制） */
function clearCustomVars(): void {
  const st = document.documentElement.style
  for (const k of [
    '--c-primary', '--c-primary-hover', '--c-primary-soft', '--c-primary-soft-text',
    '--c-accent', '--c-accent-soft', '--c-accent-soft-text',
    '--c-download', '--c-download-soft', '--c-download-soft-text',
  ]) {
    st.removeProperty(k)
  }
}

/** 监听系统主题变化的 MediaQueryList（仅作浏览器侧通道全不可用时的兜底） */
let mql: MediaQueryList | null = null
let mqlListener: ((e: MediaQueryListEvent) => void) | null = null

/** fnOS 系统主题（null=未获取到浏览器侧信号） */
let fnosTheme: 'light' | 'dark' | null = null

// Fix #2：幂等标志，防止重复安装 message/storage 监听器
let fnosListenersInstalled = false

// fnOS 门户在浏览器 localStorage 写入主题模式（测试代码 zdaitest001 main.js 方式）：
//   key=fnos-theme-mode，'20'=深色，'10'=亮色，其它值/缺失 = 跟随系统
function fnosThemeFromStorage(): 'light' | 'dark' | null {
  try {
    const v = localStorage.getItem('fnos-theme-mode')
    if (v === '20') return 'dark'
    if (v === '10') return 'light'
  } catch {
    // localStorage 不可用（隐私模式等）
  }
  return null
}

// Item 6：fnOS webview postMessage 通信（测试代码 main.js 方式）
// fnOS 桌面环境通过 webview 嵌入时，父窗口会通过 postMessage 推送主题变化
function setupFnosPostMessage(): void {
  // Fix #2：幂等保护，避免每次 applyTheme 都新增监听器
  if (fnosListenersInstalled) {
    // 已安装过监听器，仅重新请求一次父窗口主题
    try {
      if (window.parent && window.parent !== window) {
        window.parent.postMessage({ type: 'fnos-theme-request' }, '*')
      }
    } catch {
      // ignore
    }
    return
  }
  fnosListenersInstalled = true
  try {
    // 主动请求父窗口当前主题
    if (window.parent && window.parent !== window) {
      window.parent.postMessage({ type: 'fnos-theme-request' }, '*')
    }
  } catch {
    // ignore
  }
  // 监听 fnOS 主题变化消息
  window.addEventListener('message', (event) => {
    if (!event?.data) return
    // fnOS 主动推送当前主题
    if (event.data.type === 'fnos-theme' || event.data.type === 'fnos-theme-change') {
      const t = event.data.theme
      if (t === 'light' || t === 'dark') {
        fnosTheme = t
        if (getCurrentTheme() === 'auto') {
          document.documentElement.setAttribute('data-theme', getResolvedTheme('auto'))
        }
      }
    }
  })
  // localStorage 事件监听 — fnOS 有时通过 localStorage.fnos-theme-mode 变化通知
  // code: '10' = light, '20' = dark（测试代码 main.js 方式）
  window.addEventListener('storage', (e) => {
    if (e.key !== 'fnos-theme-mode' || getCurrentTheme() !== 'auto') return
    const v = e.newValue
    if (v === '10') { fnosTheme = 'light'; document.documentElement.setAttribute('data-theme', 'light') }
    else if (v === '20') { fnosTheme = 'dark'; document.documentElement.setAttribute('data-theme', 'dark') }
  })
}

/** 获取当前生效的主题（解析 auto 为实际的 light/dark；auto 优先跟随 fnOS） */
export function getResolvedTheme(mode: ThemeMode): ResolvedTheme {
  if (mode === 'auto') {
    // 浏览器侧 fnOS 信号优先：postMessage 结果 → localStorage fnos-theme-mode（测试代码方式）
    if (fnosTheme === 'light' || fnosTheme === 'dark') return fnosTheme
    const fromStorage = fnosThemeFromStorage()
    if (fromStorage) return fromStorage
    // 全部不可用时回退浏览器系统偏好
    if (typeof window !== 'undefined' && window.matchMedia) {
      return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
    }
    return 'dark'
  }
  return mode
}

/** 应用主题到 DOM */
export function applyTheme(mode: ThemeMode): void {
  // 清理旧的 auto 监听
  if (mql && mqlListener) {
    mql.removeEventListener('change', mqlListener)
    mql = null
    mqlListener = null
  }

  // Item 6：启动 fnOS postMessage + storage 事件监听（测试代码方式）
  setupFnosPostMessage()

  if (mode === 'auto') {
    // 启动时立即从 localStorage 读取 fnOS 主题（fnOS 门户写入，'20'/'10'）
    if (fnosTheme === null) fnosTheme = fnosThemeFromStorage()
    const resolved = getResolvedTheme(mode)
    document.documentElement.setAttribute('data-theme', resolved)
    // 兜底：浏览器侧信号全不可用时监听浏览器主题变化
    if (typeof window !== 'undefined' && window.matchMedia) {
      mql = window.matchMedia('(prefers-color-scheme: light)')
      mqlListener = () => {
        if (fnosTheme === null && fnosThemeFromStorage() === null) {
          document.documentElement.setAttribute('data-theme', getResolvedTheme('auto'))
        }
      }
      mql.addEventListener('change', mqlListener)
    }
  } else {
    // 非 auto：直接用指定主题，fnos 信号不再生效
    fnosTheme = null
    document.documentElement.setAttribute('data-theme', getResolvedTheme(mode))
  }
  // 自定义配色：主题切换后按新主题明暗重新推导 hover/soft 等衍生色
  if (getCurrentPalette() === 'custom') {
    applyCustomVars(getCustomColor() ?? '#3b82f6')
  }
}

/** 获取当前存储的主题模式 */
export function getCurrentTheme(): ThemeMode {
  const stored = localStorage.getItem(STORAGE_KEY)
  if (stored === 'light' || stored === 'dark' || stored === 'auto') return stored
  return 'auto'
}

/** 设置主题模式并持久化 */
export function setTheme(mode: ThemeMode): void {
  localStorage.setItem(STORAGE_KEY, mode)
  applyTheme(mode)
}

/** 应用启动时调用：从 localStorage 读取主题并应用 */
export function initTheme(): void {
  const mode = getCurrentTheme()
  applyTheme(mode)
  applyPalette(getCurrentPalette())
}
