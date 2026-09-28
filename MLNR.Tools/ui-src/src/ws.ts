// 浏览器 WebSocket 客户端：接收任务状态推送，自动重连。
// F1: 应用层心跳——TCP 半开时 onclose 不会触发，45s 未收到任何消息则主动关闭连接触发重连。
// F3: 重连指数退避——1s→2s→4s→…→上限 30s，连接成功重置。

type Listener = (msg: any) => void
type LifecycleListener = () => void

class WSClient {
  private ws: WebSocket | null = null
  private listeners = new Set<Listener>()
  private openListeners = new Set<LifecycleListener>()
  private closeListeners = new Set<LifecycleListener>()
  private reconnectTimer: number | null = null
  private closed = false
  private reconnectAttempts = 0
  private lastMsgAt = 0
  private heartbeatTimer: number | null = null

  private static readonly HEARTBEAT_CHECK_MS = 15000   // 每 15s 检查一次链路活性
  private static readonly MSG_TIMEOUT_MS = 45000       // 45s 未收到任何消息视为链路失效
  private static readonly RECONNECT_MAX_MS = 30000     // 重连退避上限 30s

  connect() {
    if (this.ws) return
    this.closed = false
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${location.host}/app/mlnr/ws`
    this.lastMsgAt = Date.now()
    this.ws = new WebSocket(url)
    this.ws.onopen = () => {
      console.log('[ws] connected')
      this.reconnectAttempts = 0
      this.startHeartbeat()
      this.openListeners.forEach((l) => l())
    }
    this.ws.onmessage = (ev) => {
      this.lastMsgAt = Date.now()
      try {
        const msg = JSON.parse(ev.data)
        this.listeners.forEach((l) => l(msg))
      } catch (e) {
        console.warn('[ws] parse error', e)
      }
    }
    this.ws.onclose = () => {
      this.stopHeartbeat()
      this.ws = null
      this.closeListeners.forEach((l) => l())
      if (!this.closed) {
        // F3: 指数退避重连（1s, 2s, 4s, ... 30s 封顶）
        const delay = Math.min(
          WSClient.RECONNECT_MAX_MS,
          1000 * 2 ** this.reconnectAttempts,
        )
        this.reconnectAttempts++
        console.log(`[ws] disconnected, reconnect in ${delay}ms`)
        this.reconnectTimer = window.setTimeout(() => this.connect(), delay)
      }
    }
    this.ws.onerror = () => this.ws?.close()
  }

  private startHeartbeat() {
    this.stopHeartbeat()
    this.heartbeatTimer = window.setInterval(() => {
      if (this.closed || !this.ws) return
      // 服务端每 30s 发送 ping + 数据推送；45s 内无任何消息 → 主动断开触发重连
      if (Date.now() - this.lastMsgAt > WSClient.MSG_TIMEOUT_MS) {
        console.warn('[ws] heartbeat timeout, force close')
        this.ws.close()
      }
    }, WSClient.HEARTBEAT_CHECK_MS)
  }

  private stopHeartbeat() {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer)
      this.heartbeatTimer = null
    }
  }

  on(listener: Listener) {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** 注册连接建立回调，返回退订函数 */
  onOpen(listener: LifecycleListener) {
    this.openListeners.add(listener)
    return () => this.openListeners.delete(listener)
  }

  /** 注册连接关闭回调，返回退订函数 */
  onClose(listener: LifecycleListener) {
    this.closeListeners.add(listener)
    return () => this.closeListeners.delete(listener)
  }

  close() {
    this.closed = true
    this.stopHeartbeat()
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.ws?.close()
  }
}

export const ws = new WSClient()
