package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"mlnr/logger"
)

// wsClient 单个 WebSocket 连接及其独立发送队列。
// 每个连接拥有独立的有界发送队列与 writer goroutine，
// Broadcast 仅做非阻塞投递，避免单个死连接的同步写阻塞整个 Hub 锁、
// 级联冻结 BLE/温度等控制链路。
type wsClient struct {
	conn      *websocket.Conn
	sendCh    chan []byte
	closeOnce sync.Once // 保证 sendCh 只关闭一次（defer 与 CloseAll/Broadcast 均可能触发）
}

// closeSend 关闭发送队列（幂等）。
func (c *wsClient) closeSend() {
	c.closeOnce.Do(func() { close(c.sendCh) })
}

// Hub 管理浏览器 WebSocket 连接，向前端推送状态更新与温度快照。
type Hub struct {
	mu      sync.Mutex
	clients map[*wsClient]bool
	ble     *BLEManager
	thermal *ThermalManager
	disk    *DiskManager
}

var upgrader = websocket.Upgrader{
	// fnOS 内网环境：上位机与浏览器同机/同内网部署，统一放行 Origin。
	// 鉴权由网关层（gatewayUser/requireAdmin）保障，非依赖同源策略。
	CheckOrigin: func(r *http.Request) bool { return true },
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*wsClient]bool)}
}

// SetBLE 注入 BLE 管理器。
func (h *Hub) SetBLE(ble *BLEManager) {
	h.ble = ble
}

// SetThermal 注入温度管理器。
func (h *Hub) SetThermal(t *ThermalManager) {
	h.thermal = t
}

// SetDisk 注入硬盘组管理器。
func (h *Hub) SetDisk(d *DiskManager) {
	h.disk = d
}

// HandleWS 处理浏览器 WebSocket 连接。
func (h *Hub) HandleWS(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Warn("ws", "upgrade failed: %v", err)
		return
	}

	client := &wsClient{
		conn:   conn,
		sendCh: make(chan []byte, 16),
	}

	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()

	// writer goroutine：从 sendCh 读消息写入 WebSocket，设置写超时（10s），
	// 写失败/队列关闭即退出，防止半开连接永久挂起。
	// 定期发送 ping 作为保活探测（I3）。
	go h.writeLoop(client)

	// 发送欢迎消息
	user := getGatewayUser(c)
	hello := gin.H{
		"type": "hello",
		"data": gin.H{"uid": user.UID, "user": user.Username},
		"time": time.Now().Format(time.RFC3339),
	}
	if data, err := json.Marshal(hello); err == nil {
		h.sendTo(client, data)
	}

	// 发送当前状态快照 + 温度快照
	h.sendStateSnapshot(client)

	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
		client.closeSend() // 关闭 sendCh → writer goroutine 退出
		conn.Close()
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// writeLoop 单连接的 writer goroutine：串行写消息 + 写超时 + ping 保活。
func (h *Hub) writeLoop(client *wsClient) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case data, ok := <-client.sendCh:
			if !ok {
				return
			}
			_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := client.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				logger.WarnRateLimited("ws", "write-failed", 30*time.Second, "write failed: %v", err)
				_ = client.conn.Close()
				return
			}
		case <-pingTicker.C:
			_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				logger.WarnRateLimited("ws", "ping-failed", 30*time.Second, "ping failed: %v", err)
				_ = client.conn.Close()
				return
			}
		}
	}
}

// sendTo 向单个连接非阻塞投递消息（队列满则丢弃该条，不阻塞调用方）。
func (h *Hub) sendTo(c *wsClient, data []byte) {
	select {
	case c.sendCh <- data:
	default:
	}
}

// sendStateSnapshot 发送当前状态快照 + 温度快照。
func (h *Hub) sendStateSnapshot(c *wsClient) {
	if h.ble != nil {
		msg := StateUpdateMsg{
			Type:     "state_update",
			Device:   h.ble.GetDeviceInfo(),
			Fans:     h.ble.GetFanStates(),
			Switches: h.ble.GetSwitchStates(),
			Time:     time.Now().Format(time.RFC3339),
		}
		s := sanitizeSettings(h.ble.GetSettings())
		msg.Setting = &s
		if h.disk != nil {
			msg.DiskGroups = h.disk.GetViews()
		}
		if data, err := json.Marshal(msg); err == nil {
			h.sendTo(c, data)
		}
	}
	if h.thermal != nil {
		snap := h.thermal.Snapshot()
		wrapped := gin.H{
			"type": "thermal_update",
			"data": snap,
			"time": snap.Time,
		}
		if data, err := json.Marshal(wrapped); err == nil {
			h.sendTo(c, data)
		}
	}
}

// Broadcast 向所有客户端推送任意 JSON 消息。
// 仅做非阻塞投递：单个连接发送队列满（消费过慢/卡死）时关闭其连接并移除，
// 不在锁内做任何同步 I/O，避免一个死连接冻结整个系统。
func (h *Hub) Broadcast(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	var dead []*wsClient
	for c := range h.clients {
		select {
		case c.sendCh <- data:
		default:
			// 队列满：连接消费不及，关闭连接交由 read loop 清理
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		delete(h.clients, c)
	}
	h.mu.Unlock()
	// 锁外关闭连接，触发对应 read loop 退出 → defer 清理 sendCh
	for _, c := range dead {
		_ = c.conn.Close()
	}
}

// BroadcastState 向所有客户端推送状态更新。
func (h *Hub) BroadcastState(msg StateUpdateMsg) {
	h.Broadcast(msg)
}

// BroadcastThermal 向所有客户端推送温度快照。
func (h *Hub) BroadcastThermal(snap ThermalSnapshot) {
	wrapped := gin.H{
		"type": "thermal_update",
		"data": snap,
		"time": snap.Time,
	}
	h.Broadcast(wrapped)
}

// BroadcastLog 向所有客户端推送日志。
func (h *Hub) BroadcastLog(level, module, message string) {
	msg := LogMsg{
		Type:    "log",
		Level:   level,
		Module:  module,
		Message: message,
		Time:    time.Now().Format(time.RFC3339),
	}
	h.Broadcast(msg)
}

// CloseAll 关闭所有连接。
func (h *Hub) CloseAll() {
	h.mu.Lock()
	clients := h.clients
	h.clients = make(map[*wsClient]bool)
	h.mu.Unlock()
	for c := range clients {
		_ = c.conn.Close()
		c.closeSend()
	}
}
