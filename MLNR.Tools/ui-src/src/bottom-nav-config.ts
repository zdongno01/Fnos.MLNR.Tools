// 底部导航栏配置（纯逻辑模块，无 DOM 依赖，可独立单测）
// 导航项目录与左侧导航栏（main.ts NAV_GROUPS）的 key 一一对应：
// home / fans / disks / sensors / connection / settings / schedules / logs
import type { IconName } from './ui'

export interface BottomNavItemDef {
  key: string    // 对应侧边导航 key（唯一标识）
  label: string  // 底部导航显示名
  icon: IconName // svgIcon 名称
  hash: string   // 目标路由
}

/** 全部可选的底部导航项（= 左侧导航栏全部条目） */
export const BOTTOM_NAV_ALL_ITEMS: BottomNavItemDef[] = [
  { key: 'home', label: '总览', icon: 'chart', hash: '#/' },
  { key: 'fans', label: '风扇', icon: 'fan', hash: '#/fans' },
  { key: 'disks', label: '硬盘', icon: 'hdd', hash: '#/disks' },
  { key: 'sensors', label: '传感器', icon: 'thermometer', hash: '#/sensors' },
  { key: 'connection', label: '连接', icon: 'link', hash: '#/connection' },
  { key: 'settings', label: '设置', icon: 'settings', hash: '#/settings' },
  { key: 'schedules', label: '计划', icon: 'clock', hash: '#/schedules' },
  { key: 'logs', label: '日志', icon: 'log', hash: '#/logs' },
]

/** 默认底部导航项：总览 / 风扇 / 硬盘 / 设置 */
export const BOTTOM_NAV_DEFAULT_KEYS: string[] = ['home', 'fans', 'disks', 'settings']

/** 底部导航项数量上限（= 左侧导航栏条目数） */
export const BOTTOM_NAV_MAX_ITEMS = 8

/** 规范化 key 列表：只保留合法项、去重、限 8 项（顺序保持）；非数组输入返回 [] */
export function normalizeBottomNavKeys(keys: string[] | null | undefined): string[] {
  if (!Array.isArray(keys)) return []
  const valid = new Set(BOTTOM_NAV_ALL_ITEMS.map(i => i.key))
  const seen = new Set<string>()
  const out: string[] = []
  for (const k of keys) {
    if (!k || !valid.has(k) || seen.has(k) || out.length >= BOTTOM_NAV_MAX_ITEMS) continue
    seen.add(k)
    out.push(k)
  }
  return out
}

/** 勾选/取消某项：勾选时追加到末尾（按当前选择顺序），取消时移除；达到上限后忽略新增 */
export function toggleBottomNavKey(keys: string[], key: string): string[] {
  if (keys.includes(key)) return keys.filter(k => k !== key)
  if (keys.length >= BOTTOM_NAV_MAX_ITEMS) return keys
  return [...keys, key]
}

/** 移动某项到新位置（0 起）；from/to 越界或相同返回原数组 */
export function moveBottomNavKey(keys: string[], from: number, to: number): string[] {
  if (from < 0 || from >= keys.length || to < 0 || to >= keys.length || from === to) return keys
  const out = keys.slice()
  const [k] = out.splice(from, 1)
  out.splice(to, 0, k)
  return out
}

/** 由 key 列表解析出导航项定义（过滤未知 key，保持顺序） */
export function resolveBottomNavItems(keys: string[]): BottomNavItemDef[] {
  const byKey = new Map(BOTTOM_NAV_ALL_ITEMS.map(i => [i.key, i]))
  const out: BottomNavItemDef[] = []
  for (const k of keys) {
    const it = byKey.get(k)
    if (it) out.push(it)
  }
  return out
}
