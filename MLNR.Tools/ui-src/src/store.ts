import type {
  FanHWState, SwitchHWState, DiskGroupView, Settings, AppInfo, DeviceInfo,
  FanMode, FanConfigKey, SwitchConfigKey, GlobalConfigKey, SensorData,
  ThermalSnapshot, HistoryResponse, FanChannelConfig, DiskGroupConfig,
  SensorChannelConfig, SensorChannelReading,
  LogEntry, SystemDisk, DiskPartition, DiskEntry, CommandResult,
} from './types'
import {
  defaultSettings, defaultSpeedCurve, defaultSensorChannels,
  MAX_FAN_CHANNELS, MAX_DISK_GROUPS,
  ModeAuto, ModeManual,
} from './types'
import { api, type HistoryQuery } from './api'
import { ws } from './ws'
import { BOTTOM_NAV_DEFAULT_KEYS, normalizeBottomNavKeys } from './bottom-nav-config'

/**
 * 全局状态管理（发布订阅模式）。
 * 存储设备连接状态、多风扇实时状态、开关通道、硬盘组视图、温度和上位机设置，
 * 并通过 WebSocket 订阅后端推送实时更新。
 */
type Listener = () => void

// Fix #5：保存 WS 消息退订函数，重连前先退订避免重复注册
let wsUnsub: (() => void) | null = null

class Store {
  /** 设备连接信息（名称、MAC、信号强度、连接状态、会话类型、运行状态） */
  device: DeviceInfo | null = null
  /** 各路风扇硬件实时状态 */
  fans: FanHWState[] = []
  /** 各路开关硬件实时状态 */
  switches: SwitchHWState[] = []
  /** 硬盘组实时视图（含挂载状态） */
  diskGroups: DiskGroupView[] = []
  /** I2C 传感器最新数据（兼容旧字段，FS5；null 表示未启用/未收到推送） */
  sensor: SensorData | null = null
  /** 多通道 I2C 传感器实时读数（CH0~CH3，null 表示该通道无数据） */
  sensorReadings: (SensorChannelReading | null)[] = []
  /** 多通道 I2C 传感器配置（来自 settings.sensorChannels） */
  sensorChannelConfigs: SensorChannelConfig[] = defaultSensorChannels()
  /** 上位机运行设置（风扇/硬盘组/传感器配置，本地持久化） */
  settings: Settings = defaultSettings()
  /** 应用基础信息（版本号、网关用户等） */
  appInfo: AppInfo | null = null
  /** 温度快照（所有传感器，含 CPU/MB/HDD） */
  thermal: ThermalSnapshot | null = null
  /** 日志环形缓冲（API 拉取 + WS 实时推送合并） */
  logs: LogEntry[] = []
  /** 日志数据版本号：每次日志新增/重载自增，供视图层判断"日志是否真的变了"（Fix：避免无关推送触发日志页整页重建） */
  logVersion = 0
  /** 固件 Debug 模式（CFG GLOBAL DEBUG_MODE；决定技术字段是否显示） */
  debugMode: boolean = false

  /** 实时扫描状态 */
  scanResults: DeviceInfo[] = []
  scanRunning: boolean = false

  /** 后端连通性（HTTP 读失败或 WS 断开时为 false） */
  backendOnline: boolean = true

  /** 有 CFG 已下发到硬件内存但尚未 SAVE(0x0A) 落盘 NVS（顶栏提示依据；直接写入模式下恒为 false） */
  pendingNvsSave: boolean = false

  // #22：最近修改的硬件配置字段（防止 WS state_update 用后端旧值覆盖刚修改的值）
  // key 格式：fan:{index}:{configKey} 或 switch:{index}:{configKey}
  // value：修改时间戳（毫秒），超过 10 秒自动失效
  private dirtyConfig = new Map<string, number>()
  private DIRTY_CONFIG_TTL = 10_000 // 10 秒
  private DIRTY_CONFIG_MAX = 200 // F4: 超过该条数时先惰性清理过期项，防止长期操作后 map 无界增长

  /** 标记某个配置字段为"刚修改"，WS 推送在 TTL 内不会覆盖该字段 */
  private markConfigDirty(scope: 'fan' | 'switch' | 'sensor', index: number, key: string): void {
    this.dirtyConfig.set(`${scope}:${index}:${key}`, Date.now())
    // F4: map 膨胀时顺带清理过期项（访问时淘汰只在被查询的键上触发，未查询的过期键会永久残留）
    if (this.dirtyConfig.size > this.DIRTY_CONFIG_MAX) {
      const cutoff = Date.now() - this.DIRTY_CONFIG_TTL
      for (const [k, ts] of this.dirtyConfig) {
        if (ts < cutoff) this.dirtyConfig.delete(k)
      }
    }
  }

  /** 检查某个配置字段是否为"刚修改"状态（TTL 内） */
  private isConfigDirty(scope: 'fan' | 'switch' | 'sensor', index: number, key: string): boolean {
    const ts = this.dirtyConfig.get(`${scope}:${index}:${key}`)
    if (!ts) return false
    if (Date.now() - ts > this.DIRTY_CONFIG_TTL) {
      this.dirtyConfig.delete(`${scope}:${index}:${key}`)
      return false
    }
    return true
  }

  private listeners = new Set<Listener>()

  subscribe(l: Listener) {
    this.listeners.add(l)
    return () => this.listeners.delete(l)
  }
  notify() {
    this.listeners.forEach((l) => l())
  }

  // ===== 初始化加载 =====

  async loadInfo() {
    try {
      this.appInfo = await api.info()
      this.backendOnline = true
      this.notify()
    } catch (e) {
      console.warn('[store] loadInfo failed', e)
      this.backendOnline = false
      this.notify()
    }
  }

  async loadAll() {
    await Promise.allSettled([this.loadStatus(), this.loadSettings(), this.loadTemperature()])
  }

  async loadStatus() {
    try {
      const r = await api.getStatus()
      this.device = r.device
      this.fans = r.fans ?? []
      this.switches = r.switches ?? []
      this.diskGroups = r.diskGroups ?? []
      this.sensor = r.sensor ?? null
      this.sensorReadings = Array.isArray(r.sensorChannels) ? r.sensorChannels : []
      // #22：I2C 通道 dirty 保护（与 WS 推送同一合并逻辑）
      this.applySettingMerge(r.settings)
      this.backendOnline = true
      this.notify()
      // 固件 Debug 模式：连接后可查询（未加密/失败时静默忽略，下次再试）
      if (this.device?.connectionState === 'connected') {
        void api.getBleDebugMode().then(r => {
          this.debugMode = !!r.enabled
          this.notify()
        }).catch(() => { /* 保持上次值 */ })
      }
    } catch (e) {
      console.warn('[store] loadStatus failed', e)
      this.backendOnline = false
      this.notify()
    }
  }

  async loadSettings() {
    try {
      const r = await api.getSettings()
      this.settings = this.normalizeSettings(r.settings)
      this.backendOnline = true
      this.notify()
    } catch (e) {
      console.warn('[store] loadSettings failed', e)
      this.backendOnline = false
      this.notify()
    }
  }

  async loadTemperature() {
    try {
      this.thermal = await api.getTemperature()
      this.backendOnline = true
      this.notify()
    } catch (e) {
      console.warn('[store] loadTemperature failed', e)
      this.backendOnline = false
      if (!this.thermal) {
        this.thermal = { temps: [], time: new Date().toISOString() }
      }
      this.notify()
    }
  }

  async loadLogs(count = 300, level?: string, history = true) {
    try {
      const r = await api.getLogs(count, level, history)
      this.logs = r.logs ?? []
      this.logVersion++
      this.backendOnline = true
      this.notify()
    } catch (e) {
      console.warn('[store] loadLogs failed', e)
      this.backendOnline = false
      this.notify()
    }
  }

  async history(q: HistoryQuery): Promise<HistoryResponse> {
    return api.getHistory(q)
  }

  /** 规范化 Settings：补齐缺失字段、校验通道上限 */
  private normalizeSettings(s: Settings): Settings {
    const def = defaultSettings()
    const out: Settings = {
      deviceName: (s.deviceName && s.deviceName.trim()) || def.deviceName,
      autoConnect: s.autoConnect !== false,
      lastAddress: s.lastAddress ?? '',
      bleKey: Array.isArray(s.bleKey) ? s.bleKey : [],
      hbTimeoutSec: clamp(s.hbTimeoutSec ?? def.hbTimeoutSec, 3, 60),
      sensorEnabled: s.sensorEnabled ?? false,
      sensorIntervalSec: clamp(s.sensorIntervalSec ?? def.sensorIntervalSec, 1, 30),
      sensorChannels: this.normalizeSensorChannels(s.sensorChannels),
      fans: [],
      diskGroups: [],
      tempSensors: Array.isArray(s.tempSensors) ? s.tempSensors : [],
      theme: (s.theme === 'light' || s.theme === 'dark' || s.theme === 'auto' || s.theme === 'sepia' || s.theme === 'midnight') ? s.theme : 'auto',
      directSaveNVS: s.directSaveNVS === true,
      homeLayout: Array.isArray(s.homeLayout)
        ? s.homeLayout.filter(x => x && typeof x.id === 'string').map(x => ({ id: x.id, size: typeof x.size === 'string' ? x.size : '' }))
        : [],
      diskStatsEnabled: Array.isArray(s.diskStatsEnabled)
        ? s.diskStatsEnabled.filter(x => typeof x === 'string' && x.length > 0)
        : [],
      // 底部导航栏：null/缺省回退默认 4 项；空数组（已保存）保留=隐藏底部导航
      bottomNavKeys: Array.isArray(s.bottomNavKeys)
        ? normalizeBottomNavKeys(s.bottomNavKeys)
        : BOTTOM_NAV_DEFAULT_KEYS.slice(),
    }
    // 风扇配置补齐
    if (Array.isArray(s.fans) && s.fans.length > 0) {
      for (const f of s.fans.slice(0, MAX_FAN_CHANNELS)) {
        out.fans.push(this.normalizeFan(f))
      }
    }
    while (out.fans.length < MAX_FAN_CHANNELS) {
      out.fans.push(this.normalizeFan({ ...def.fans[out.fans.length] }))
    }
    // 硬盘组配置补齐
    if (Array.isArray(s.diskGroups) && s.diskGroups.length > 0) {
      for (const g of s.diskGroups.slice(0, MAX_DISK_GROUPS)) {
        out.diskGroups.push(this.normalizeDiskGroup(g))
      }
    }
    while (out.diskGroups.length < MAX_DISK_GROUPS) {
      const idx = out.diskGroups.length
      out.diskGroups.push(this.normalizeDiskGroup({ ...def.diskGroups[idx] }))
    }
    return out
  }

  private normalizeFan(f: Partial<FanChannelConfig>): FanChannelConfig {
    const def = defaultSettings().fans[0]
    // 迁移曲线：Curves 为空时从旧 SpeedCurve 填充 3 条
    let curves: Record<string, import('./types').CurvePoint[]> | undefined = undefined
    let activeCurve = f.activeCurve
    if (f.curves && Object.keys(f.curves).length > 0) {
      curves = f.curves
    } else {
      let src = f.speedCurve
      if (!Array.isArray(src) || src.length < 2) {
        src = def.speedCurve
      }
      curves = {
        efficient: src.map(p => ({ temp: Number(p.temp) || 0, spd: clamp(Number(p.spd) || 0, 0, 100) })),
        daily: src.map(p => ({ temp: Number(p.temp) || 0, spd: clamp(Number(p.spd) || 0, 0, 100) })),
        quiet: src.map(p => ({ temp: Number(p.temp) || 0, spd: clamp(Number(p.spd) || 0, 0, 100) })),
      }
      if (!activeCurve) activeCurve = 'daily'
    }
    // 确保每条曲线合法
    if (curves) {
      for (const k of Object.keys(curves)) {
        if (!Array.isArray(curves[k]) || curves[k].length < 2) {
          curves[k] = defaultSpeedCurve()
        }
      }
    }
    // ActiveCurve 校验
    if (!activeCurve || !curves?.[activeCurve]) {
      activeCurve = 'daily'
    }
    // 同步 speedCurve 为当前 activeCurve（向后兼容）
    const speedCurve = curves[activeCurve].map(p => ({ ...p }))

    // TempRefs 迁移：优先用 tempRefs；为空则从旧 tempRef 迁移
    let tempRefs: string[] = Array.isArray(f.tempRefs) ? f.tempRefs.filter(Boolean) : []
    if (tempRefs.length === 0 && f.tempRef) {
      tempRefs = [f.tempRef]
    }

    return {
      id: f.id ?? 1,
      alias: f.alias ?? '',
      enabled: f.enabled !== false,
      tempRefs,
      // tempRef 不再填充（保留 undefined 让后端 omitempty 生效）
      mode: (f.mode === ModeAuto || f.mode === ModeManual) ? f.mode! : ModeAuto,
      targetSpd: clamp(f.targetSpd ?? 30, 0, 100),
      speedCurve,
      curves,
      activeCurve,
      faultSpd: clamp(f.faultSpd ?? 50, 0, 100),
      diskOfflineSpd: clamp(f.diskOfflineSpd ?? 0, 0, 100),
    }
  }

  private normalizeDiskGroup(g: Partial<DiskGroupConfig>): DiskGroupConfig {
    const id = g.id ?? 1
    return {
      id,
      alias: g.alias ?? '',
      switchN: (g.switchN && g.switchN >= 1 && g.switchN <= MAX_DISK_GROUPS) ? g.switchN : id,
      enabled: g.enabled ?? false,
      autoOnline: g.autoOnline ?? false,
      // 过滤"空硬盘条目"（device 与 serial 均空）：不自动保留/绑定任何未绑定硬盘
      disks: Array.isArray(g.disks)
        ? g.disks.map(d => this.normalizeDiskEntry(d)).filter(d => d.device !== '' || d.serial !== '')
        : [],
      // 等待时间（必须保留，否则保存后退出重进会回退默认值）
      powerOnDelaySec: (g.powerOnDelaySec && g.powerOnDelaySec >= 1) ? g.powerOnDelaySec : 8,
      forceOffDelaySec: (g.forceOffDelaySec && g.forceOffDelaySec >= 1) ? g.forceOffDelaySec : 2,
    }
  }

  /** 规范化硬盘条目：旧数据 mountPath 迁移为 mounts 数组，补齐 serial */
  private normalizeDiskEntry(d: Partial<DiskEntry> & { mountPath?: string }): DiskEntry {
    // 向后兼容：旧数据只有 mountPath，迁移为 mounts 数组第一项
    let mounts = Array.isArray(d.mounts) ? d.mounts : []
    if (mounts.length === 0 && d.mountPath) {
      mounts = [{ partition: d.device || '', mountPoint: d.mountPath }]
    }
    return {
      device: d.device ?? '',
      alias: d.alias ?? '',
      serial: d.serial ?? '',
      mounts,
      autoMount: d.autoMount !== false,
      isNvme: d.isNvme === true,
    }
  }

  /** 规范化多通道传感器配置：补齐 4 通道、校验型号/地址/间隔 */
  private normalizeSensorChannels(list?: SensorChannelConfig[]): SensorChannelConfig[] {
    const def = defaultSensorChannels()
    const src = Array.isArray(list) ? list : []
    const out: SensorChannelConfig[] = []
    for (let i = 0; i < def.length; i++) {
      const raw = src[i]
      const d = def[i]
      out.push({
        id: i,
        alias: (raw && raw.alias) || d.alias,
        kind: (raw && raw.kind >= 1 && raw.kind <= 4 ? raw.kind : d.kind),
        addr: (raw && raw.addr > 0 ? raw.addr : d.addr),
        enabled: !!(raw && raw.enabled),
        intervalSec: clamp((raw && raw.intervalSec > 0 ? raw.intervalSec : d.intervalSec), 1, 3600),
        showTemp: !!(raw && raw.showTemp),
        showHumi: !!(raw && raw.showHumi),
        showPress: !!(raw && raw.showPress),
        showAlt: !!(raw && raw.showAlt),
      })
    }
    return out
  }

  // ===== WebSocket 推送应用 =====

  /** 合并后端/固件同步来的 settings：I2C 通道刚修改的字段保留本地值（#22 同机制，防同步覆盖）。
   * 固件基线 sensorChannelConfigs 始终用固件值（dirty 保护不作用基线，否则保存时 hwDiff 判断失效）。 */
  private applySettingMerge(incoming: Settings): void {
    const normalized = this.normalizeSettings(incoming)
    const local = this.settings
    const sc = (normalized.sensorChannels || []).map((ch, i) => {
      const lc = local?.sensorChannels?.[i]
      if (!lc) return ch
      const out = { ...ch }
      if (this.isConfigDirty('sensor', i, 'KIND')) out.kind = lc.kind
      if (this.isConfigDirty('sensor', i, 'ADDR')) out.addr = lc.addr
      if (this.isConfigDirty('sensor', i, 'ENABLED')) out.enabled = lc.enabled
      if (this.isConfigDirty('sensor', i, 'INTERVALSEC')) out.intervalSec = lc.intervalSec
      if (this.isConfigDirty('sensor', i, 'ALIAS')) out.alias = lc.alias
      if (this.isConfigDirty('sensor', i, 'SHOWTEMP')) out.showTemp = lc.showTemp
      if (this.isConfigDirty('sensor', i, 'SHOWHUMI')) out.showHumi = lc.showHumi
      if (this.isConfigDirty('sensor', i, 'SHOWPRESS')) out.showPress = lc.showPress
      if (this.isConfigDirty('sensor', i, 'SHOWALT')) out.showAlt = lc.showAlt
      return out
    })
    this.settings = { ...normalized, sensorChannels: sc }
    this.sensorChannelConfigs = normalized.sensorChannels
  }

  private applyStateUpdate(msg: any) {
    if (msg.type !== 'state_update') return
    if (msg.device) this.device = msg.device
    // #22：fans 智能合并 — 对刚修改的配置字段（powerSpd/hbFallback/pwmFreq/enabled）保留本地值，
    // 防止后端 WS 推送旧缓存值覆盖刚下发的新值
    if (msg.fans !== undefined) {
      this.fans = this.mergeFans(msg.fans ?? [])
    }
    // #22：switches 智能合并 — 对刚修改的配置字段保留本地值
    if (msg.switches !== undefined) {
      this.switches = this.mergeSwitches(msg.switches ?? [])
    }
    if (msg.diskGroups !== undefined) this.diskGroups = msg.diskGroups ?? []
    if (msg.sensor !== undefined) this.sensor = msg.sensor ?? null
    if (msg.sensorChannels !== undefined) this.sensorReadings = msg.sensorChannels ?? []
    if (msg.setting) {
      // #22：I2C 通道同机制 dirty 保护——刚修改的字段保留本地值，防 WS 推送固件旧值覆盖
      this.applySettingMerge(msg.setting)
    }
    this.notify()
  }

  // #22：合并 WS 推送的风扇状态，保留刚修改的配置字段
  private mergeFans(incoming: FanHWState[]): FanHWState[] {
    if (this.fans.length === 0) return incoming
    return incoming.map(f => {
      const local = this.fans.find(l => l.index === f.index)
      if (!local) return f
      const merged: FanHWState = { ...f }
      // 对配置类字段，若本地刚修改过则保留本地值
      if (this.isConfigDirty('fan', f.index, 'POWER_SPD')) merged.powerSpd = local.powerSpd
      if (this.isConfigDirty('fan', f.index, 'HB_FALLBACK_SPD')) merged.hbFallback = local.hbFallback
      if (this.isConfigDirty('fan', f.index, 'PWM_FREQ')) merged.pwmFreq = local.pwmFreq
      if (this.isConfigDirty('fan', f.index, 'ENABLED')) merged.enabled = local.enabled
      return merged
    })
  }

  // #22：合并 WS 推送的开关状态，保留刚修改的配置字段
  private mergeSwitches(incoming: SwitchHWState[]): SwitchHWState[] {
    if (this.switches.length === 0) return incoming
    return incoming.map(s => {
      const local = this.switches.find(l => l.index === s.index)
      if (!local) return s
      const merged: SwitchHWState = { ...s }
      if (this.isConfigDirty('switch', s.index, 'POWER_ON_STATE')) merged.powerOnState = local.powerOnState
      if (this.isConfigDirty('switch', s.index, 'POWER_ON_DELAY')) merged.powerOnDelay = local.powerOnDelay
      if (this.isConfigDirty('switch', s.index, 'SYSTEM')) merged.system = local.system
      if (this.isConfigDirty('switch', s.index, 'ENABLED')) merged.enabled = local.enabled
      return merged
    })
  }

  private applyThermalUpdate(msg: any) {
    if (msg.type !== 'thermal_update') return
    if (msg.data) this.thermal = msg.data
    this.notify()
  }

  private applyLog(msg: any) {
    if (msg.type !== 'log') return
    const entry: LogEntry = {
      time: msg.time || new Date().toISOString(),
      level: msg.level || 'INFO',
      module: msg.module || 'BLE',
      message: msg.message || '',
    }
    this.logs.push(entry)
    // 环形缓冲：保留最近 500 条
    if (this.logs.length > 500) {
      this.logs.splice(0, this.logs.length - 500)
    }
    this.logVersion++
    this.notify()
  }

  startWS() {
    ws.connect()
    // Fix #1：WS 连接建立/关闭时同步后端连通性状态
    ws.onOpen(() => {
      this.backendOnline = true
      this.notify()
    })
    ws.onClose(() => {
      this.backendOnline = false
      this.notify()
    })
    // Fix #5：保存退订函数，重连前先退订避免重复注册
    if (wsUnsub) wsUnsub()
    wsUnsub = ws.on((msg) => {
      if (msg.type === 'state_update') this.applyStateUpdate(msg)
      if (msg.type === 'thermal_update') this.applyThermalUpdate(msg)
      if (msg.type === 'log') this.applyLog(msg)
      if (msg.type === 'scan_started') {
        this.scanResults = []
        this.scanRunning = true
        this.notify()
      }
      if (msg.type === 'scan_result' && msg.device) {
        const dev: DeviceInfo = msg.device
        // 去重（按 address）
        const idx = this.scanResults.findIndex(d => d.address === dev.address)
        if (idx === -1) {
          this.scanResults.push(dev)
        } else {
          // 更新已有设备的 RSSI
          this.scanResults[idx] = dev
        }
        this.notify()
      }
      if (msg.type === 'scan_stopped') {
        this.scanRunning = false
        this.notify()
      }
    })
  }

  // ===== 业务操作 =====

  async connect(): Promise<boolean> {
    try {
      const r = await api.connect()
      this.device = r.device
      this.fans = r.fans ?? []
      this.notify()
      return true
    } catch (e: any) {
      console.warn('[store] connect failed', e)
      return false
    }
  }

  /** 一次性扫描全部蓝牙设备（阻塞等待服务端 10s 扫描完成，已由 startScan 实时模式替代，保留兼容） */
  async scanOnce(): Promise<DeviceInfo[]> {
    try {
      const r = await api.scan()
      return r.devices ?? []
    } catch (e) {
      console.warn('[store] scan failed', e)
      return []
    }
  }

  /** 启动后台实时扫描，WS 推送 scan_result 消息更新 this.scanResults */
  async startScan(): Promise<boolean> {
    try {
      await api.scanStart()
      return true
    } catch (e) {
      console.warn('[store] scanStart failed', e)
      return false
    }
  }

  /** 停止后台实时扫描 */
  async stopScan(): Promise<void> {
    try {
      await api.scanStop()
    } catch { /* ignore */ }
  }

  /** FS.md 上位机#8：按用户选择的设备地址连接 */
  async connectTo(address: string, name?: string): Promise<boolean> {
    try {
      const r = await api.connect(address, name)
      this.device = r.device
      this.fans = r.fans ?? []
      this.notify()
      return true
    } catch (e: any) {
      console.warn('[store] connectTo failed', e)
      return false
    }
  }

  async disconnect(): Promise<boolean> {
    try {
      await api.disconnect()
      this.fans = []
      this.switches = []
      this.diskGroups = []
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] disconnect failed', e)
      return false
    }
  }

  async handshake(): Promise<boolean> {
    try {
      const r = await api.handshake()
      if (r.device) this.device = r.device
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] handshake failed', e)
      return false
    }
  }

  async refresh(): Promise<boolean> {
    try {
      const r = await api.refresh()
      if (r.fans) this.fans = r.fans
      if (r.switches) this.switches = r.switches
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] refresh failed', e)
      return false
    }
  }

  async setFanSpeed(fanId: number, speed: number): Promise<boolean> {
    try {
      await api.setFanSpeed(fanId, speed)
      // 乐观更新本地目标转速
      this.fans = this.fans.map(f => f.index === fanId ? { ...f, targetSpd: speed } : f)
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] setFanSpeed failed', e)
      return false
    }
  }

  async setFanMode(fanId: number, mode: FanMode): Promise<boolean> {
    try {
      await api.setFanMode(fanId, mode)
      this.settings = {
        ...this.settings,
        fans: this.settings.fans.map(f => f.id === fanId ? { ...f, mode } : f),
      }
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] setFanMode failed', e)
      return false
    }
  }

  async setSwitch(swId: number, state: number): Promise<boolean> {
    try {
      const r = await api.setSwitch(swId, state)
      if (r.ok) {
        this.switches = this.switches.map(s => s.index === swId ? { ...s, state } : s)
      }
      this.notify()
      return r.ok
    } catch (e) {
      console.warn('[store] setSwitch failed', e)
      return false
    }
  }

  // ===== 硬盘组管理 =====

  async diskPowerOn(groupId: number): Promise<CommandResult | null> {
    try {
      const r = await api.diskPowerOn(groupId)
      if (r.ok) await this.loadStatus()
      return r.result ?? null
    } catch (e) {
      console.warn('[store] diskPowerOn failed', e)
      return null
    }
  }

  async diskPowerOff(groupId: number, action?: string): Promise<CommandResult | null> {
    try {
      const r = await api.diskPowerOff(groupId, action)
      if (r.ok) await this.loadStatus()
      return r.result ?? null
    } catch (e) {
      console.warn('[store] diskPowerOff failed', e)
      return null
    }
  }

  async diskForcePowerOff(groupId: number): Promise<CommandResult | null> {
    try {
      const r = await api.diskForcePowerOff(groupId)
      if (r.ok) await this.loadStatus()
      return r.result ?? null
    } catch (e) {
      console.warn('[store] diskForcePowerOff failed', e)
      return null
    }
  }

  async diskMount(groupId: number, device: string): Promise<CommandResult | null> {
    try {
      const r = await api.diskMount(groupId, device)
      if (r.ok) await this.loadStatus()
      return r.result ?? null
    } catch (e) {
      console.warn('[store] diskMount failed', e)
      return null
    }
  }

  async diskUnmount(groupId: number, device: string, action?: string): Promise<CommandResult | null> {
    try {
      const r = await api.diskUnmount(groupId, device, action)
      if (r.ok) await this.loadStatus()
      return r.result ?? null
    } catch (e) {
      console.warn('[store] diskUnmount failed', e)
      return null
    }
  }

  // ===== 系统硬盘列表与分区（用于下拉选择） =====

  /** 加载系统硬盘列表（排除已被其它通道绑定的硬盘由调用方过滤）；refresh=true 强制后端重新收集 */
  async loadDisksList(refresh = false): Promise<SystemDisk[]> {
    try {
      const r = await api.getDisksList(refresh)
      return r.disks ?? []
    } catch (e) {
      console.warn('[store] loadDisksList failed', e)
      return []
    }
  }

  /** 加载指定硬盘的分区列表 */
  async loadDiskPartitions(device: string): Promise<DiskPartition[]> {
    try {
      const r = await api.getDiskPartitions(device)
      return r.partitions ?? []
    } catch (e) {
      console.warn('[store] loadDiskPartitions failed', e)
      return []
    }
  }

  // ===== 硬件配置下发 =====

  /** CFG 下发成功后的 NVS 落盘处理：直接写入模式立即跟发 SAVE(0x0A)，否则标记待落盘（顶栏提示） */
  private afterCfgApplied(): void {
    if (this.settings.directSaveNVS) {
      void this.saveNvsNow()
    } else {
      this.pendingNvsSave = true
      this.notify()
    }
  }

  /** 发送 SAVE(0x0A) 将硬件内存中的 CFG 配置落盘 NVS；成功后清除待落盘标记 */
  async saveNvsNow(): Promise<boolean> {
    try {
      const r = await api.saveHardwareConfig()
      if (r.ok) this.pendingNvsSave = false
      this.notify()
      return r.ok
    } catch (e) {
      console.warn('[store] saveNvsNow failed', e)
      return false
    }
  }

  async setFanConfig(fanId: number, key: FanConfigKey, value: number): Promise<boolean> {
    try {
      const r = await api.setFanConfig(fanId, key, value)
      if (r.ok) {
        // #22：标记该配置字段为刚修改，防止 WS 旧值覆盖
        this.markConfigDirty('fan', fanId, key)
        this.fans = this.fans.map(f => {
          if (f.index !== fanId) return f
          const nf = { ...f }
          switch (key) {
            case 'ENABLED': nf.enabled = value === 1; break
            case 'POWER_SPD': nf.powerSpd = value; break
            case 'HB_FALLBACK_SPD': nf.hbFallback = value; break
            case 'PWM_FREQ': nf.pwmFreq = value; break
          }
          return nf
        })
        this.notify()
        this.afterCfgApplied()
      }
      return r.ok
    } catch (e) {
      console.warn(`[store] setFanConfig ${key} failed`, e)
      return false
    }
  }

  async setSwitchConfig(swId: number, key: SwitchConfigKey, value: number): Promise<boolean> {
    try {
      const r = await api.setSwitchConfig(swId, key, value)
      if (r.ok) {
        // #22：标记该配置字段为刚修改，防止 WS 旧值覆盖
        this.markConfigDirty('switch', swId, key)
        this.switches = this.switches.map(s => {
          if (s.index !== swId) return s
          const ns = { ...s }
          switch (key) {
            case 'POWER_ON_STATE': ns.powerOnState = value; break
            case 'POWER_ON_DELAY': ns.powerOnDelay = value; break
            case 'SYSTEM': ns.system = value === 1; break
          }
          return ns
        })
        // ENABLED 开关：同步 store.settings.diskGroups 的 enabled 字段
        if (key === 'ENABLED') {
          const newEnabled = value === 1
          const updated = this.settings.diskGroups.map(g =>
            g.switchN === swId ? { ...g, enabled: newEnabled } : g,
          )
          this.settings = { ...this.settings, diskGroups: updated }
        }
        this.notify()
        this.afterCfgApplied()
      }
      return r.ok
    } catch (e) {
      console.warn(`[store] setSwitchConfig ${key} failed`, e)
      return false
    }
  }

  /** FS.md 上位机#7：下发全局配置（心跳超时阈值）并同步本地设置 */
  async setGlobalConfig(key: GlobalConfigKey, value: number): Promise<boolean> {
    try {
      const r = await api.setGlobalConfig(key, value)
      if (r.ok) {
        if (key === 'HB_TIMEOUT_SEC') {
          this.settings = { ...this.settings, hbTimeoutSec: value }
          this.notify()
        }
        this.afterCfgApplied()
      }
      return r.ok
    } catch (e) {
      console.warn(`[store] setGlobalConfig ${key} failed`, e)
      return false
    }
  }

  /** 下发 Debug 模式开关（CFG GLOBAL DEBUG_MODE，固件仅写内存、SAVE 时持久化） */
  async setDebugMode(enabled: boolean): Promise<boolean> {
    try {
      await api.setBleDebugMode(enabled)
      this.debugMode = enabled
      this.afterCfgApplied()
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] setDebugMode failed', e)
      return false
    }
  }

  /** 固件是否处于 Debug 模式（决定 FANx/SWx/GLOBAL/传感器型号等技术字段是否显示） */
  isDebugMode(): boolean {
    return this.debugMode
  }

  async saveHardwareConfig(): Promise<boolean> {
    try {
      const r = await api.saveHardwareConfig()
      if (r.ok) {
        // 手动 SAVE 成功后清除顶栏待落盘提示
        this.pendingNvsSave = false
        this.notify()
      }
      return r.ok
    } catch (e) {
      console.warn('[store] saveHardwareConfig failed', e)
      return false
    }
  }

  // ===== I2C 传感器配置（FS5: AHT20+BMP280） =====

  async setSensorEnabled(enabled: boolean): Promise<boolean> {
    try {
      const r = await api.setSensorEnabled(enabled)
      if (r.ok) {
        this.settings = { ...this.settings, sensorEnabled: enabled }
        if (!enabled) this.sensor = null
        this.notify()
      }
      return r.ok
    } catch (e) {
      console.warn('[store] setSensorEnabled failed', e)
      return false
    }
  }

  async setSensorInterval(intervalSec: number): Promise<boolean> {
    try {
      const r = await api.setSensorInterval(intervalSec)
      if (r.ok) {
        this.settings = { ...this.settings, sensorIntervalSec: intervalSec }
        this.notify()
      }
      return r.ok
    } catch (e) {
      console.warn('[store] setSensorInterval failed', e)
      return false
    }
  }

  // ===== 多通道 I2C 传感器（FS5-B5：CH0~CH3） =====

  /** 下发单通道配置到硬件并同步本地配置 */
  async updateSensorChannel(id: number, patch: Partial<SensorChannelConfig>): Promise<boolean> {
    try {
      const r = await api.updateSensorChannel(id, patch)
      if (r.ok) {
        // 成功后拉取最新配置同步本地（含硬件下发的字段）
        try {
          const sc = await api.getSensorChannels()
          if (Array.isArray(sc.channels)) {
            const s = this.normalizeSettings({ ...this.settings, sensorChannels: sc.channels })
            this.settings = s
            this.sensorChannelConfigs = s.sensorChannels
          }
        } catch { /* 同步失败不阻断成功返回 */ }
        // 下发成功后固件已确认新值，清除该通道 dirty（本地与固件一致）
        for (const k of [...this.dirtyConfig.keys()]) {
          if (k.startsWith(`sensor:${id}:`)) this.dirtyConfig.delete(k)
        }
        this.notify()
        this.afterCfgApplied()
      }
      return r.ok
    } catch (e) {
      console.warn('[store] updateSensorChannel failed', e)
      return false
    }
  }

  /** 标记 I2C 通道某配置字段为"刚修改"（视图层直接改 store 后调用，防同步覆盖） */
  markSensorChannelDirty(chId: number, key: string): void {
    this.markConfigDirty('sensor', chId, key)
  }

  /** 标记 I2C 通道全部配置字段（删除/重置场景） */
  markSensorChannelAllDirty(chId: number): void {
    for (const k of ['KIND', 'ADDR', 'ENABLED', 'INTERVALSEC', 'ALIAS', 'SHOWTEMP', 'SHOWHUMI', 'SHOWPRESS', 'SHOWALT']) {
      this.markConfigDirty('sensor', chId, k)
    }
  }

  /** 清除全部 I2C 通道 dirty（保存成功后调用：本地已与固件一致） */
  clearSensorChannelDirty(): void {
    for (const k of [...this.dirtyConfig.keys()]) {
      if (k.startsWith('sensor:')) this.dirtyConfig.delete(k)
    }
  }

  async factoryReset(): Promise<boolean> {
    try {
      const r = await api.factoryReset()
      if (r.ok) await this.loadStatus()
      return r.ok
    } catch (e) {
      console.warn('[store] factoryReset failed', e)
      return false
    }
  }

  // ===== 上位机设置 =====

  async saveSettings(s: Settings): Promise<boolean> {
    try {
      const normalized = this.normalizeSettings(s)
      const r = await api.saveSettings(normalized)
      this.settings = this.normalizeSettings(r.settings)
      this.notify()
      return true
    } catch (e) {
      console.warn('[store] saveSettings failed', e)
      return false
    }
  }

  async getLogConfig(): Promise<{ level: string; maxSizeMB: number; keepDays: number } | null> {
    try {
      return await api.getLogConfig()
    } catch (e) {
      console.warn('[store] getLogConfig failed', e)
      return null
    }
  }

  async saveLogConfig(level: string, maxSizeMB: number): Promise<{ level: string; maxSizeMB: number; keepDays: number } | null> {
    try {
      return await api.saveLogConfig(level, maxSizeMB)
    } catch (e) {
      console.warn('[store] saveLogConfig failed', e)
      return null
    }
  }
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v))
}

export const store = new Store()
