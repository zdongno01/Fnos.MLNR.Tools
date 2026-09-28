package main

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mlnr/logger"
)

// Handlers 持有所有依赖的处理器组。
type Handlers struct {
	store     *Store
	ble       *BLEManager
	hub       *Hub
	thermal   *ThermalManager
	disk      *DiskManager
	scheduler *Scheduler
}

// ===== 通用 =====

func (h *Handlers) info(c *gin.Context) {
	user := getGatewayUser(c)
	c.JSON(200, gin.H{
		"app":         "mlnr-tools",
		"version":     appVer,
		"runtime":     fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		"serverTime":  time.Now().Format(time.RFC3339),
		"gatewayUser": user,
	})
}

// ===== 日志 =====

func (h *Handlers) getLogs(c *gin.Context) {
	count := 200
	if s := c.Query("count"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 5000 {
			count = n
		}
	}
	// history=1（默认）：返回本次运行（内存）+ 历史运行（日志文件）合并；
	// history=0：仅返回本次运行内存缓冲。
	history := true
	if s := c.Query("history"); s != "" {
		history = s == "1"
	}
	var entries []logger.LogEntry
	if history {
		entries = logger.ReadHistoryLogs(count * 4)
	} else {
		entries = logger.GetRecentLogs(count * 4)
	}

	levelFilter := strings.ToUpper(c.Query("level"))
	levelMap := map[string]logger.LogLevel{
		"DEBUG": logger.DEBUG, "INFO": logger.INFO,
		"WARN": logger.WARN, "ERROR": logger.ERROR,
	}

	result := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		levelName := logger.LevelName(e.Level)
		if levelFilter != "" {
			filterLevel, ok := levelMap[levelFilter]
			if !ok || e.Level < filterLevel {
				continue
			}
		}
		result = append(result, gin.H{
			"time":    e.Time.Format("2006-01-02 15:04:05"),
			"level":   levelName,
			"module":  e.Module,
			"message": e.Message,
		})
	}
	c.JSON(200, gin.H{"logs": result, "count": len(result)})
}

func (h *Handlers) getLogConfig(c *gin.Context) {
	level, maxSizeMB, keepDays := logger.GetLogConfig()
	c.JSON(200, gin.H{
		"level":     logger.LevelName(level),
		"maxSizeMB": maxSizeMB,
		"keepDays":  keepDays,
	})
}

func (h *Handlers) setLogConfig(c *gin.Context) {
	var body struct {
		Level     string `json:"level"`
		MaxSizeMB *int64 `json:"maxSizeMB"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if body.Level != "" {
		lvl, ok := parseLogLevel(body.Level)
		if !ok {
			c.JSON(400, gin.H{"error": "非法日志级别: " + body.Level})
			return
		}
		logger.SetLogLevel(lvl)
	}
	_, curMax, curKeep := logger.GetLogConfig()
	maxSize := curMax
	if body.MaxSizeMB != nil {
		if *body.MaxSizeMB < 1 || *body.MaxSizeMB > 1024 {
			c.JSON(400, gin.H{"error": "日志大小限制范围为 1~1024 MB"})
			return
		}
		maxSize = *body.MaxSizeMB
	}
	logger.SetLogConfig(maxSize, curKeep)

	level, maxSizeMB, keepDays := logger.GetLogConfig()
	c.JSON(200, gin.H{
		"level":     logger.LevelName(level),
		"maxSizeMB": maxSizeMB,
		"keepDays":  keepDays,
	})
}

func parseLogLevel(name string) (logger.LogLevel, bool) {
	switch strings.ToUpper(name) {
	case "DEBUG":
		return logger.DEBUG, true
	case "INFO":
		return logger.INFO, true
	case "WARN":
		return logger.WARN, true
	case "ERROR":
		return logger.ERROR, true
	case "FATAL":
		return logger.FATAL, true
	}
	return logger.INFO, false
}

// ===== 状态查询 =====

func (h *Handlers) getStatus(c *gin.Context) {
	resp := gin.H{
		"device":         h.ble.GetDeviceInfo(),
		"fans":           h.ble.GetFanStates(),
		"switches":       h.ble.GetSwitchStates(),
		"sensor":         h.ble.GetSensorData(),
		"sensorChannels": h.ble.GetSensorChannels(),
		"settings":       sanitizeSettings(h.ble.GetSettings()),
	}
	if h.disk != nil {
		// 统计信息已由 GetViews() 统一填充（HTTP 与 WS 推送共用），此处不再重复
		resp["diskGroups"] = h.disk.GetViews()
	}
	c.JSON(200, resp)
}

// ===== 连接管理 =====

// connect 连接设备：可选指定地址（用户从扫描清单选择），否则自动连接上次设备。
func (h *Handlers) connect(c *gin.Context) {
	var body struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	}
	_ = c.ShouldBindJSON(&body)

	var err error
	if strings.TrimSpace(body.Address) != "" {
		err = h.ble.ConnectTo(DeviceInfo{
			Name:    body.Name,
			Address: body.Address,
		})
	} else {
		err = h.ble.Connect()
	}
	if err != nil {
		logger.Warn("handler", "connect failed: %v", err)
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"ok":     true,
		"device": h.ble.GetDeviceInfo(),
		"fans":   h.ble.GetFanStates(),
	})
}

func (h *Handlers) disconnect(c *gin.Context) {
	if err := h.ble.Disconnect(); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// scan 扫描周围全部 BLE 设备，返回设备清单（NR_F2S4 前缀优先）。
// 状态守卫（M2）：已连接/连接中/重连中禁止扫描，否则 setState(StateScanning)
// 会覆盖正在使用的连接状态，导致连接状态机紊乱。
func (h *Handlers) scan(c *gin.Context) {
	switch st := h.ble.GetState(); st {
	case StateConnected, StateConnecting, StateReconnecting:
		c.JSON(409, gin.H{"error": "设备已" + string(st) + "，请先断开再扫描"})
		return
	}
	devices, err := h.ble.Scan(10 * time.Second)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"devices": devices})
}

// scanStart 启动后台 BLE 扫描（实时推送 scan_result WS 消息）。
// 同 scan 的状态守卫（M2）。
func (h *Handlers) scanStart(c *gin.Context) {
	switch st := h.ble.GetState(); st {
	case StateConnected, StateConnecting, StateReconnecting:
		c.JSON(409, gin.H{"error": "设备已" + string(st) + "，请先断开再扫描"})
		return
	}
	if err := h.ble.StartScan(); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// scanStop 停止后台 BLE 扫描。
func (h *Handlers) scanStop(c *gin.Context) {
	h.ble.StopScan()
	c.JSON(200, gin.H{"ok": true})
}

// handshake 触发初始化握手（会话密钥方案：明文 HELLO 探测 → 密钥分发/装载 → 加密验证；可手动触发）。
func (h *Handlers) handshake(c *gin.Context) {
	if err := h.ble.InitiateHandshake(); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "device": h.ble.GetDeviceInfo()})
}

func (h *Handlers) refresh(c *gin.Context) {
	if err := h.ble.SyncAllStates(); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"ok":       true,
		"fans":     h.ble.GetFanStates(),
		"switches": h.ble.GetSwitchStates(),
	})
}

// ===== 风扇控制 =====

// setFanSpeed 下发指定风扇转速。
func (h *Handlers) setFanSpeed(c *gin.Context) {
	var body struct {
		FanID int `json:"fanId"`
		Speed int `json:"speed"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetFanSpeed(body.FanID, body.Speed)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// setFanMode 切换风扇运行模式（本地配置）。
func (h *Handlers) setFanMode(c *gin.Context) {
	var body struct {
		FanID int    `json:"fanId"`
		Mode  string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	mode := FanMode(body.Mode)
	if mode != ModeAuto && mode != ModeManual {
		c.JSON(400, gin.H{"error": "模式必须为 auto 或 manual"})
		return
	}
	s := h.store.GetSettings()
	for i := range s.Fans {
		if s.Fans[i].ID == body.FanID {
			s.Fans[i].Mode = mode
			break
		}
	}
	h.store.SaveSettings(s)
	h.ble.SetSettings(s)
	c.JSON(200, gin.H{"ok": true, "mode": mode})
}

// ===== 开关控制 =====

func (h *Handlers) setSwitch(c *gin.Context) {
	var body struct {
		SwID  int `json:"swId"`
		State int `json:"state"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetSwitch(body.SwID, body.State)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// ===== 硬盘组管理 =====

// listDisks 查询系统物理磁盘列表（供硬盘组配置页下拉选择）。
// 执行 lsblk 失败时返回空列表 + 错误信息，不 panic。
func (h *Handlers) listDisks(c *gin.Context) {
	if h.disk == nil {
		c.JSON(200, gin.H{"disks": []DiskInfo{}, "error": "磁盘管理器未初始化"})
		return
	}
	// ?refresh=1：强制重新执行 lsblk 等并更新缓存（"刷新硬盘列表"按钮），否则返回缓存
	if c.Query("refresh") == "1" {
		h.disk.RefreshDiskInfo()
	}
	disks, err := h.disk.ListDisks()
	if err != nil {
		logger.Warn("handler", "list disks failed: %v", err)
		c.JSON(200, gin.H{"disks": []DiskInfo{}, "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"disks": disks})
}

// listPartitions 查询指定磁盘的分区列表（?device=/dev/sda）。
// 执行 lsblk 失败时返回空列表 + 错误信息，不 panic。
func (h *Handlers) listPartitions(c *gin.Context) {
	if h.disk == nil {
		c.JSON(200, gin.H{"partitions": []PartitionInfo{}, "error": "磁盘管理器未初始化"})
		return
	}
	device := c.Query("device")
	parts, err := h.disk.ListPartitions(device)
	if err != nil {
		logger.Warn("handler", "list partitions failed: %v", err)
		c.JSON(200, gin.H{"partitions": []PartitionInfo{}, "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"partitions": parts})
}

func (h *Handlers) diskPowerOn(c *gin.Context) {
	groupID, _ := strconv.Atoi(c.Param("id"))
	res, err := h.disk.PowerOn(groupID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) diskPowerOff(c *gin.Context) {
	groupID, _ := strconv.Atoi(c.Param("id"))
	action := ""
	var body struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&body); err == nil && body.Action != "" {
		action = body.Action
	}
	res, err := h.disk.PowerOff(groupID, action)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) diskForcePowerOff(c *gin.Context) {
	groupID, _ := strconv.Atoi(c.Param("id"))
	res, err := h.disk.ForcePowerOff(groupID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) diskMount(c *gin.Context) {
	groupID, _ := strconv.Atoi(c.Param("id"))
	var body struct {
		Device string `json:"device"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.disk.MountDisk(groupID, body.Device)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) diskUnmount(c *gin.Context) {
	groupID, _ := strconv.Atoi(c.Param("id"))
	var body struct {
		Device string `json:"device"`
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.disk.UnmountDisk(groupID, body.Device, body.Action)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// ===== 硬盘组操作日志 =====

// getDiskLogs 返回硬盘组操作日志事件列表（支持分页 + 日期筛选 + 动作筛选）。
// 查询参数：
//   - groupId=int   按硬盘组 ID 过滤（<0 不过滤）
//   - action=str    按动作类型过滤（power_on/power_off/force_off/mount/unmount/auto_on/auto_off）
//   - from=str      起始时间（RFC3339，可选）
//   - to=str        结束时间（RFC3339，可选）
//   - page=int      页码（从 1 开始，默认 1）
//   - pageSize=int  每页条数（默认 20，最大 200）
func (h *Handlers) getDiskLogs(c *gin.Context) {
	dl := GetDiskLogger()
	if dl == nil {
		c.JSON(200, gin.H{"total": 0, "page": 1, "size": 20, "items": []gin.H{}})
		return
	}

	params := DiskLogQueryParams{
		GroupID:  -1,
		Page:     1,
		PageSize: 20,
	}
	if s := c.Query("groupId"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			params.GroupID = n
		}
	}
	if a := c.Query("action"); a != "" {
		params.Action = DiskLogAction(a)
	}
	if s := c.Query("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			params.From = t
		}
	}
	if s := c.Query("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			params.To = t
		}
	}
	if s := c.Query("page"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			params.Page = n
		}
	}
	if s := c.Query("pageSize"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			params.PageSize = n
		}
	}

	res := dl.Query(params)
	items := make([]gin.H, 0, len(res.Items))
	for _, e := range res.Items {
		items = append(items, gin.H{
			"time":    e.Time.Format(time.RFC3339),
			"groupId": e.GroupID,
			"alias":   e.Alias,
			"action":  e.Action,
			"content": e.Content,
			"result":  e.Result,
			"remark":  e.Remark,
			"error":   e.Error, // 兼容旧字段（已规范化清空，保留）
		})
	}
	c.JSON(200, gin.H{
		"total":        res.Total,
		"page":         res.Page,
		"size":         res.Size,
		"items":        items,
		"actionLabels": diskLogActionLabels(),
	})
}

// diskLogActionLabels 返回动作类型 → 显示名映射（供前端筛选下拉使用）。
// 动作已收敛为 4 类：上线/下线/挂载/卸载；原自动/强制/按钮/离线等子类并入动作+备注。
func diskLogActionLabels() map[string]string {
	return map[string]string{
		"":          "全部",
		"power_on":  "上线",
		"power_off": "下线",
		"mount":     "挂载",
		"unmount":   "卸载",
	}
}

// ===== 硬件配置下发 =====

func (h *Handlers) setFanConfig(c *gin.Context) {
	var body struct {
		FanID int    `json:"fanId"`
		Key   string `json:"key"`
		Value int    `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetFanConfig(body.FanID, body.Key, body.Value)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) setSwitchConfig(c *gin.Context) {
	var body struct {
		SwID  int    `json:"swId"`
		Key   string `json:"key"`
		Value int    `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetSwitchConfig(body.SwID, body.Key, body.Value)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// setGlobalConfig 下发全局配置项（FS.md 上位机#7：心跳超时阈值全局化）。
// 下发成功后同步更新本地 settings.hbTimeoutSec 并落盘。
func (h *Handlers) setGlobalConfig(c *gin.Context) {
	var body struct {
		Key   string `json:"key"`
		Value int    `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetGlobalConfig(body.Key, body.Value)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	if res.OK && strings.EqualFold(body.Key, "HB_TIMEOUT_SEC") {
		s := h.store.GetSettings()
		s.HbTimeoutSec = body.Value
		h.store.SaveSettings(s)
		h.ble.SetSettings(s)
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// getGlobalConfig 查询全局配置（GETG），返回心跳超时、调试模式、广播名。
func (h *Handlers) getGlobalConfig(c *gin.Context) {
	cfg, err := h.ble.GetGlobalConfig()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"hb_timeout": cfg.HbTimeout,
		"debug_mode": cfg.DebugMode,
		"ble_name":   cfg.BleName,
	})
}

// ===== BLE 广播名（上位机需求） =====

// getBleName 查询设备 BLE 广播名。
func (h *Handlers) getBleName(c *gin.Context) {
	name, err := h.ble.GetBleName()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"name": name})
}

// setBleName 修改设备 BLE 广播名（1~20 字节可打印 ASCII）。
func (h *Handlers) setBleName(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if err := h.ble.SetBleName(body.Name); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "name": body.Name})
}

// ===== 调试模式 =====

// getDebugMode 查询调试模式开关。
func (h *Handlers) getDebugMode(c *gin.Context) {
	enabled, err := h.ble.GetDebugMode()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"enabled": enabled})
}

// setDebugMode 设置调试模式开关。
func (h *Handlers) setDebugMode(c *gin.Context) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if err := h.ble.SetDebugMode(body.Enabled); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "enabled": body.Enabled})
}

func (h *Handlers) saveHardwareConfig(c *gin.Context) {
	res, err := h.ble.SaveConfig()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) factoryReset(c *gin.Context) {
	res, err := h.ble.FactoryReset()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// ===== 传感器配置（FS5: I2C AHT20+BMP280） =====

func (h *Handlers) setSensorEnabled(c *gin.Context) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetSensorEnabled(body.Enabled)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

func (h *Handlers) setSensorInterval(c *gin.Context) {
	var body struct {
		IntervalSec int `json:"intervalSec"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	res, err := h.ble.SetSensorInterval(body.IntervalSec)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// ===== 多通道传感器（FS5-B5：CH0~CH3，AHT20/BMP280/LM75/HTU21D） =====

// getSensorChannels 返回全部通道配置与实时读数。
func (h *Handlers) getSensorChannels(c *gin.Context) {
	s := h.ble.GetSettings()
	chans := s.SensorChannels
	if len(chans) != MaxSensorChannels {
		chans = DefaultSensorChannels()
	}
	// 保证配置与读数对齐
	for len(chans) < MaxSensorChannels {
		chans = append(chans, DefaultSensorChannels()[len(chans)])
	}
	c.JSON(200, gin.H{
		"channels": chans,
		"readings": h.ble.GetSensorChannels(),
	})
}

// setSensorChannel 下发单通道传感器配置（支持部分字段更新）。
// 下发 CFG,SENSOR,<CH>,KIND/ADDR/ENABLED/INTERVAL_SEC 到硬件并落盘本地。
func (h *Handlers) setSensorChannel(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id < 0 || id >= MaxSensorChannels {
		c.JSON(400, gin.H{"error": "通道编号必须在 0~3 之间"})
		return
	}
	var body struct {
		Kind        *int    `json:"kind"`
		Addr        *int    `json:"addr"`
		Enabled     *bool   `json:"enabled"`
		IntervalSec *int    `json:"intervalSec"`
		Alias       *string `json:"alias"`
		ShowTemp    *bool   `json:"showTemp"`
		ShowHumi    *bool   `json:"showHumi"`
		ShowPress   *bool   `json:"showPress"`
		ShowAlt     *bool   `json:"showAlt"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	// 以现有配置为基座做部分更新
	chans := h.ble.GetSettings().SensorChannels
	if len(chans) != MaxSensorChannels {
		chans = DefaultSensorChannels()
	}
	base := chans[id]
	if body.Kind != nil {
		base.Kind = SensorKind(*body.Kind)
	}
	if body.Addr != nil {
		base.Addr = *body.Addr
	}
	if body.Enabled != nil {
		base.Enabled = *body.Enabled
	}
	if body.IntervalSec != nil {
		base.IntervalSec = *body.IntervalSec
	}
	if body.Alias != nil {
		base.Alias = *body.Alias
	}
	if body.ShowTemp != nil {
		base.ShowTemp = *body.ShowTemp
	}
	if body.ShowHumi != nil {
		base.ShowHumi = *body.ShowHumi
	}
	if body.ShowPress != nil {
		base.ShowPress = *body.ShowPress
	}
	if body.ShowAlt != nil {
		base.ShowAlt = *body.ShowAlt
	}
	res, err := h.ble.UpdateSensorChannel(id, base)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(200, gin.H{"ok": res.OK, "result": res})
}

// ===== 上位机设置 =====

// validateDiskGroupTaskRefs 校验硬盘组上电后/下电前任务引用（settings 保存时）。
// 规则：任务不存在，或任务直接执行器包含对同一硬盘组的下电操作 → 返回错误消息（空=通过）。
// 说明：仅检查任务直接执行器；经 control_task 间接引用的任务无法静态检查。
func validateDiskGroupTaskRefs(s *Settings, schedules []Schedule) string {
	for i := range s.DiskGroups {
		g := &s.DiskGroups[i]
		for _, t := range []struct {
			kind string
			task *DiskGroupTask
		}{
			{"上电后", g.OnTask},
			{"下电前", g.OffTask},
		} {
			if t.task == nil || t.task.TaskID <= 0 {
				continue
			}
			var sc *Schedule
			for j := range schedules {
				if schedules[j].ID == t.task.TaskID {
					sc = &schedules[j]
					break
				}
			}
			if sc == nil {
				return fmt.Sprintf("硬盘组 %d 的%s任务引用的任务 #%d 不存在", g.ID, t.kind, t.task.TaskID)
			}
			for _, e := range sc.Executors {
				if e.Type == ExecutorDiskGroupControl && e.DiskGroupID == g.ID && e.DiskAction == DiskActionOffline {
					return fmt.Sprintf("硬盘组 %d 的%s任务「%s」包含对同一硬盘组的下电操作，不允许保存", g.ID, t.kind, sc.Name)
				}
			}
		}
	}
	return ""
}

func (h *Handlers) getSettings(c *gin.Context) {
	c.JSON(200, gin.H{"settings": sanitizeSettings(h.store.GetSettings())})
}

func (h *Handlers) saveSettings(c *gin.Context) {
	var s Settings
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	// 校验风扇配置
	for i := range s.Fans {
		if s.Fans[i].TargetSpd < 0 || s.Fans[i].TargetSpd > 100 {
			s.Fans[i].TargetSpd = 30
		}
		if s.Fans[i].Mode != ModeAuto && s.Fans[i].Mode != ModeManual {
			s.Fans[i].Mode = ModeAuto
		}
		// 优先验证 Curves[ActiveCurve]，回退旧 SpeedCurve
		curves := s.Fans[i].Curves
		active := s.Fans[i].ActiveCurve
		var targetCurve *[]CurvePoint
		if curves != nil && active != "" {
			if c, ok := curves[active]; ok {
				targetCurve = &c
			}
		}
		if targetCurve == nil {
			targetCurve = &s.Fans[i].SpeedCurve
		}
		if len(*targetCurve) < 2 {
			*targetCurve = DefaultSpeedCurve()
		} else {
			for j := range *targetCurve {
				if (*targetCurve)[j].Temp < 0 {
					(*targetCurve)[j].Temp = 0
				}
				if (*targetCurve)[j].Temp > 150 {
					(*targetCurve)[j].Temp = 150
				}
				if (*targetCurve)[j].Spd < 0 {
					(*targetCurve)[j].Spd = 0
				}
				if (*targetCurve)[j].Spd > 100 {
					(*targetCurve)[j].Spd = 100
				}
			}
		}
		// 如果是 Curves 里的曲线，写回 map；同时同步 SpeedCurve 保持旧字段有效
		if curves != nil && active != "" && targetCurve != &s.Fans[i].SpeedCurve {
			curves[active] = *targetCurve
			s.Fans[i].SpeedCurve = *targetCurve
		} else {
			s.Fans[i].SpeedCurve = *targetCurve
		}
	}
	// 通道数上限
	if len(s.Fans) > MaxFanChannels {
		s.Fans = s.Fans[:MaxFanChannels]
	}
	if len(s.DiskGroups) > MaxDiskGroups {
		s.DiskGroups = s.DiskGroups[:MaxDiskGroups]
	}
	// #Fix（无默认自动绑定）：过滤空硬盘条目（device 与 serial 均空）
	for i := range s.DiskGroups {
		g := &s.DiskGroups[i]
		kept := g.Disks[:0]
		for _, d := range g.Disks {
			if d.Device == "" && d.Serial == "" {
				continue
			}
			kept = append(kept, d)
		}
		g.Disks = kept
	}
	// 硬盘组上电后/下电前任务校验：引用任务中不允许包含对同一硬盘组的下电操作，
	// 避免电源流程与任务执行器互相冲突（仅检查任务直接执行器；经 control_task 间接
	// 引用无法静态检查，由用户配置自担）。
	if msg := validateDiskGroupTaskRefs(&s, h.store.GetSchedules()); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	// 设备名空则回退默认
	if s.DeviceName == "" {
		s.DeviceName = defaultDeviceName
	}

	// 底部导航栏配置规范化（去空白/去重/限 8 项；空数组保留=隐藏底部导航）
	s.BottomNavKeys = normalizeBottomNavKeys(s.BottomNavKeys)

	prev := h.store.GetSettings()
	// 设备名变更表示换设备：清除上次连接地址（Connect 优先 LastAddress，避免仍连旧 MAC），
	// 之后 Connect 走 Scan 按新广播名前缀重新发现设备。
	if s.DeviceName != prev.DeviceName {
		s.LastAddress = ""
	}
	h.store.SaveSettings(s)
	h.ble.SetSettings(s)
	if h.disk != nil {
		h.disk.UpdateConfig(s.DiskGroups)
	}

	// DeviceName 变更：当前若非 Disconnected，则断开重连使用新名称
	if s.DeviceName != prev.DeviceName {
		state := h.ble.GetState()
		if state != StateDisconnected {
			logger.Info("handler", "deviceName changed %q -> %q, reconnecting", prev.DeviceName, s.DeviceName)
			_ = h.ble.Disconnect()
			go func() {
				time.Sleep(500 * time.Millisecond)
				_ = h.ble.Connect()
			}()
		}
	}

	c.JSON(200, gin.H{"settings": sanitizeSettings(h.store.GetSettings())})
}

// ===== 温度 =====

func (h *Handlers) getTemperature(c *gin.Context) {
	if h.thermal == nil {
		c.JSON(503, gin.H{"error": "温度采集模块未启用"})
		return
	}
	c.JSON(200, h.thermal.Snapshot())
}

// ===== 历史 =====

func (h *Handlers) getHistory(c *gin.Context) {
	if h.thermal == nil {
		c.JSON(503, gin.H{"error": "历史模块未启用"})
		return
	}
	series := c.Query("series")
	if series == "" {
		c.JSON(400, gin.H{"error": "缺少参数 series"})
		return
	}

	from := c.Query("from")
	var startMs, endMs int64
	if from != "" {
		startMs = timeAliasToStart(from)
	} else if s := c.Query("startMs"); s != "" {
		// 容错：前端拖动产生的毫秒戳可能是浮点（String() 带小数点），ParseInt 会失败置 0，
		// 进而触发全量降采样/窗口错位。ParseInt 失败时降级 ParseFloat 再取整。
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			startMs = v
		} else if f, err := strconv.ParseFloat(s, 64); err == nil {
			startMs = int64(f)
		}
	}
	if to := c.Query("to"); to != "" {
		if to == "now" {
			endMs = time.Now().UnixMilli()
		}
	} else if s := c.Query("endMs"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			endMs = v
		} else if f, err := strconv.ParseFloat(s, 64); err == nil {
			endMs = int64(f)
		}
	}
	customStart := c.Query("customStart")
	customEnd := c.Query("customEnd")
	if customStart != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", customStart, time.Local); err == nil {
			startMs = t.UnixMilli()
		}
	}
	if customEnd != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", customEnd, time.Local); err == nil {
			endMs = t.UnixMilli()
		}
	}

	// #Fix：历史窗口兜底——startMs 未指定（且非显式 from=all 全量）时默认最近 1 小时，
	// endMs 未指定时默认当前时间。否则 HistoryStore.Query 会对全量缓冲做 800 点等距
	// 下采样，小窗口（如 1 小时）拖动到历史区域时返回 4~6 分钟间隔的稀疏散点。
	if startMs == 0 && from != "all" {
		startMs = time.Now().Add(-1 * time.Hour).UnixMilli()
	}
	if endMs == 0 {
		endMs = time.Now().UnixMilli()
	}

	pts := h.thermal.History(series, startMs, endMs)
	if pts == nil {
		pts = []HistoryPoint{}
	}
	logger.Debug("history", "get %s [%d, %d] -> %d pts", series, startMs, endMs, len(pts))
	c.JSON(200, gin.H{
		"series": series,
		"start":  startMs,
		"end":    endMs,
		"points": pts,
	})
}

func timeAliasToStart(alias string) int64 {
	now := time.Now()
	switch alias {
	case "1h":
		return now.Add(-1 * time.Hour).UnixMilli()
	case "12h":
		return now.Add(-12 * time.Hour).UnixMilli()
	case "1d":
		return now.Add(-24 * time.Hour).UnixMilli()
	case "1w":
		return now.Add(-7 * 24 * time.Hour).UnixMilli()
	case "1m":
		return now.Add(-30 * 24 * time.Hour).UnixMilli()
	case "all", "":
		return 0
	default:
		return now.Add(-1 * time.Hour).UnixMilli()
	}
}

// ===== 定时计划（Schedule CRUD） =====

// listSchedules 返回全部定时计划任务。
func (h *Handlers) listSchedules(c *gin.Context) {
	c.JSON(200, gin.H{"schedules": h.store.GetSchedules()})
}

// createSchedule 新建定时计划任务（校验 + 归一化后落盘）。
func (h *Handlers) createSchedule(c *gin.Context) {
	var sc Schedule
	if err := c.ShouldBindJSON(&sc); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeSchedule(&sc); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	if msg := validateScheduleTargets(h.store, sc); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	// 环检测：现有任务 + 新任务
	probe := append(append([]Schedule{}, h.store.GetSchedules()...), sc)
	if msg := checkScheduleCycles(probe); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	id := h.store.AddSchedule(sc)
	logger.Info("schedule", "created schedule #%d %q (%s/%s)", id, sc.Name, sc.Category, sc.ScheduleType)
	// 读回含时间戳的完整记录（AddSchedule 在内部补齐 CreatedAt/UpdatedAt）
	created := h.findSchedule(id)
	c.JSON(200, gin.H{"schedule": created})
}

// findSchedule 按 ID 查找任务（用于创建/更新后读回完整记录；不存在返回空结构）。
func (h *Handlers) findSchedule(id int) Schedule {
	for _, e := range h.store.GetSchedules() {
		if e.ID == id {
			return e
		}
	}
	return Schedule{ID: id}
}

// updateSchedule 更新指定任务（保留 CreatedAt，刷新 UpdatedAt）。
func (h *Handlers) updateSchedule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的任务 ID"})
		return
	}
	var sc Schedule
	if err := c.ShouldBindJSON(&sc); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeSchedule(&sc); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	if msg := validateScheduleTargets(h.store, sc); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	// 环检测：替换目标任务后的全量任务列表
	existing := h.store.GetSchedules()
	probe := make([]Schedule, 0, len(existing)+1)
	for _, e := range existing {
		if e.ID == id {
			probe = append(probe, sc)
		} else {
			probe = append(probe, e)
		}
	}
	if msg := checkScheduleCycles(probe); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	if !h.store.UpdateSchedule(id, sc) {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	logger.Info("schedule", "updated schedule #%d %q", id, sc.Name)
	// 读回含时间戳的完整记录（UpdateSchedule 在内部刷新 UpdatedAt）
	c.JSON(200, gin.H{"schedule": h.findSchedule(id)})
}

// deleteSchedule 删除指定任务。
func (h *Handlers) deleteSchedule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的任务 ID"})
		return
	}
	if !h.store.DeleteSchedule(id) {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	logger.Info("schedule", "deleted schedule #%d", id)
	c.JSON(200, gin.H{"ok": true})
}

// runSchedule 手动触发指定任务（全部执行器执行，不消费触发器）。
func (h *Handlers) runSchedule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的任务 ID"})
		return
	}
	if h.scheduler == nil {
		c.JSON(500, gin.H{"error": "调度引擎未启动"})
		return
	}
	if msg, ok := h.scheduler.TriggerManual(id); !ok {
		c.JSON(404, gin.H{"error": msg})
		return
	}
	logger.Info("schedule", "manual run schedule #%d via api", id)
	c.JSON(200, gin.H{"ok": true})
}

// normalizeSchedule 校验并归一化任务字段；返回空串表示通过，否则为错误消息。
// 归一化规则（与前端表单联动一致）：
//   - 触发任务强制类别=disk、动作=offline；
//   - 按触发方式清空不适用字段（one_time/daily/weekly/monthly/trigger 互斥）；
//   - 按类别清空另一类别的字段。
func normalizeSchedule(sc *Schedule) string {
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" {
		return "任务名称不能为空"
	}
	if len([]rune(sc.Name)) > 50 {
		return "任务名称不能超过 50 字"
	}
	if len([]rune(sc.Description)) > 200 {
		sc.Description = string([]rune(sc.Description)[:200])
	}

	// 类别：可省略（M8 由触发器/动作推导），显式值必须合法
	if sc.Category != "" && sc.Category != ScheduleCategoryFan && sc.Category != ScheduleCategoryDisk {
		return "任务类别无效"
	}
	switch sc.ScheduleType {
	case ScheduleTypeOneTime, ScheduleTypeDaily, ScheduleTypeWeekly, ScheduleTypeMonthly, ScheduleTypeTrigger:
	case "":
		// 允许：新前端提交触发器清单，scheduleType 由回填（backfillTriggerFields）确定
	default:
		return "触发方式无效"
	}

	// ===== 触发器清单（M9：定时/日志/监控三类；可为空 = 不自动触发）=====
	// 1) 迁移一：Triggers 为空时从上层便捷字段构造（存量任务/旧前端）。
	//    仅时间型（one_time/daily/weekly/monthly）迁移；旧 sleep/idle 事件型
	//    （scheduleType=trigger）配置直接抛弃：保持空清单，任务仅手动/被调用执行。
	if len(sc.Triggers) == 0 {
		switch sc.ScheduleType {
		case ScheduleTypeOneTime:
			sc.Triggers = []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodOnce, RunAt: sc.RunAt}}
		case ScheduleTypeDaily:
			sc.Triggers = []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodDaily, Time: sc.Time}}
		case ScheduleTypeWeekly:
			sc.Triggers = []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodWeekly, Time: sc.Time, Weekdays: sc.Weekdays}}
		case ScheduleTypeMonthly:
			sc.Triggers = []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodMonthly, Time: sc.Time, MonthDays: sc.MonthDays}}
		}
	}

	// 2) 迁移二：旧时间型 Trigger（one_time/daily/weekly/monthly）→ 新格式；
	//    旧 sleep/idle 事件型不再迁移（旧配置直接抛弃，运行时不再触发）。
	for i := range sc.Triggers {
		migrateTrigger(&sc.Triggers[i])
	}

	// 3) 逐张校验 + 清空无关字段（空清单合法：任务仅手动触发/被调用）
	for i := range sc.Triggers {
		tr := &sc.Triggers[i]
		switch tr.Type {
		case TriggerTypeTime:
			switch tr.Period {
			case TriggerPeriodOnce:
				if tr.RunAt == nil {
					return "一次性触发器必须设置执行日期时间"
				}
				tr.Time, tr.Weekdays, tr.MonthDays = "", nil, nil
				clearTriggerEventFields(tr)
			case TriggerPeriodDaily, TriggerPeriodWeekly, TriggerPeriodMonthly:
				if !validTimeHHMM(tr.Time) {
					return "定时触发器执行时间格式应为 HH:mm（24 小时制）"
				}
				if tr.Period == TriggerPeriodWeekly && (len(tr.Weekdays) == 0 || !validIDs(tr.Weekdays, 1, 7)) {
					return "每周触发器至少选择一个星期（周一~周日）"
				}
				if tr.Period == TriggerPeriodMonthly && (len(tr.MonthDays) == 0 || !validIDs(tr.MonthDays, 1, 31)) {
					return "每月触发器至少选择一个日期（1~31）"
				}
				if tr.Period == TriggerPeriodDaily {
					tr.Weekdays, tr.MonthDays = nil, nil
				}
				tr.RunAt = nil
				clearTriggerEventFields(tr)
			case TriggerPeriodLoop:
				if !validHHMMSS(tr.LoopInterval) {
					return "循环时间格式应为 HH:MM:SS（非零，最大 23:59:59）"
				}
				tr.RunAt = nil
				tr.Time = ""
				tr.Weekdays = nil
				tr.MonthDays = nil
				clearTriggerEventFields(tr)
			default:
				return "定时事件周期无效"
			}
		case TriggerTypeLog:
			// 日志事件：引用全局日志事件资源（LogEventID>0）或内联字段
			// （读取 path/regex + 结果处理冷却/连续/轮转）
			if tr.LogEventID <= 0 && (strings.TrimSpace(tr.LogEventPath) == "" || strings.TrimSpace(tr.LogEventRegex) == "") {
				return "日志事件触发器必须选择日志事件或填写日志路径与日志监控内容"
			}
			if tr.LogEventCooldown < 0 || tr.LogEventCooldown > 99 {
				return "冷却时间范围为 0~99"
			}
			switch tr.LogEventCooldownUnit {
			case "", CooldownUnitSecond:
				tr.LogEventCooldownUnit = CooldownUnitSecond
			case CooldownUnitMinute, CooldownUnitHour:
			default:
				return "冷却时间单位无效"
			}
			if tr.LogEventCooldown == 0 {
				tr.LogEventConsecutive = 1 // 不冷却：连续匹配强制 1 次
			} else if tr.LogEventConsecutive < 1 || tr.LogEventConsecutive > 9 {
				return "连续匹配次数范围为 1~9"
			}
			clearTriggerTimeFields(tr)
		case TriggerTypeMonitor:
			if tr.MonitorPrebuilt != PrebuiltMonitorIdle && tr.MonitorScriptID <= 0 && strings.TrimSpace(tr.MonitorCode) == "" {
				return "监控事件触发器必须选择预制监控、监控脚本或填写内联脚本代码"
			}
			if tr.MonitorIntervalSec < 1 || tr.MonitorIntervalSec > 600 {
				return "监控执行间隔范围 1~600 秒"
			}
			// 内联代码脚本类型规范化；引用资源/预制时类型字段不生效
			tr.MonitorScriptType = normalizeScriptType(tr.MonitorScriptType)
			if tr.MonitorScriptID > 0 || tr.MonitorPrebuilt != "" {
				tr.MonitorScriptType = ""
			}
			clearTriggerTimeFields(tr)
		default:
			return "触发器类型无效"
		}
	}

	// 类别（M9 修订：新规划删除任务类别属性，不再做日志分类推导；
	// 旧 Category 字段仅保留存量兼容，新任务一律置空）
	sc.Category = ""

	// 4) 回填上层便捷字段（兼容展示/日志/旧前端；执行与防重复以 Triggers 为准）
	backfillTriggerFields(sc)

	// ===== 执行器清单（M9：原动作改名执行器；任务级成功/失败分支移除）=====
	// 迁移一：Executors 为空时从旧 Actions/FailureActions 构造（存量任务/旧前端）。
	// 旧 FailureActions 的 run_task 挂到首个主执行器的 FailureTaskID（执行器级失败串联）。
	if len(sc.Executors) == 0 && len(sc.Actions) > 0 {
		for _, a := range sc.Actions {
			sc.Executors = append(sc.Executors, actionToExecutor(a))
		}
		if len(sc.FailureActions) > 0 {
			for _, a := range sc.FailureActions {
				if a.Type == ActionRunTask && a.TaskID > 0 {
					sc.Executors[0].FailureTaskID = a.TaskID
					break
				}
			}
		}
	}
	// 迁移二：顶层便捷字段 → 执行器（存量任务/旧前端）。磁盘字段优先
	// （旧触发任务语义：有 DiskGroups/DiskAction 时优先视为硬盘任务）。
	if len(sc.Executors) == 0 {
		if len(sc.DiskGroups) > 0 || len(sc.DiskAction) > 0 {
			if !validIDs(sc.DiskGroups, 1, MaxDiskGroups) {
				return "请至少选择一个有效的硬盘组"
			}
			if sc.DiskAction != "" && sc.DiskAction != DiskActionOnline && sc.DiskAction != DiskActionOffline {
				return "硬盘动作无效"
			}
			for _, g := range sc.DiskGroups {
				sc.Executors = append(sc.Executors, Executor{Type: ExecutorDiskGroupControl, DiskGroupID: g, DiskAction: sc.DiskAction, ForceOff: sc.ForceOff})
			}
		} else if len(sc.FanChannels) > 0 || len(sc.FanMode) > 0 {
			if !validIDs(sc.FanChannels, 1, MaxFanChannels) {
				return "请至少选择一个有效的风扇通道（FAN1/FAN2）"
			}
			if sc.FanMode != "" && sc.FanMode != FanCurveEfficient && sc.FanMode != FanCurveDaily && sc.FanMode != FanCurveQuiet {
				return "风扇模式无效"
			}
			for _, ch := range sc.FanChannels {
				sc.Executors = append(sc.Executors, Executor{Type: ExecutorFanControl, FanID: ch, FanManual: false, FanCurve: sc.FanMode})
			}
		}
	}
	if len(sc.Executors) == 0 {
		return "至少需要一个执行器"
	}
	if msg := normalizeExecutors(sc.Executors); msg != "" {
		return msg
	}

	// 清空旧便捷动作字段（新模型以 Executors 为准）
	sc.Actions, sc.FailureActions = nil, nil
	sc.FanChannels, sc.FanMode, sc.DiskGroups, sc.DiskAction, sc.ForceOff = nil, "", nil, "", false
	return ""
}

// migrateTrigger 旧格式触发器（one_time/daily/weekly/monthly）→ 新格式（time）。
// 幂等：已是新格式的触发器原样保留。旧 sleep/idle 硬盘事件型已由
// log/monitor 预制事件替代，此函数不再迁移（旧配置直接抛弃）。
func migrateTrigger(tr *Trigger) {
	switch tr.Type {
	case ScheduleTypeOneTime:
		*tr = Trigger{Type: TriggerTypeTime, Period: TriggerPeriodOnce, RunAt: tr.RunAt}
	case ScheduleTypeDaily:
		*tr = Trigger{Type: TriggerTypeTime, Period: TriggerPeriodDaily, Time: tr.Time}
	case ScheduleTypeWeekly:
		*tr = Trigger{Type: TriggerTypeTime, Period: TriggerPeriodWeekly, Time: tr.Time, Weekdays: tr.Weekdays}
	case ScheduleTypeMonthly:
		*tr = Trigger{Type: TriggerTypeTime, Period: TriggerPeriodMonthly, Time: tr.Time, MonthDays: tr.MonthDays}
	}
}

// clearTriggerTimeFields 清空定时事件字段（日志/监控事件用）。
func clearTriggerTimeFields(tr *Trigger) {
	tr.Period, tr.RunAt, tr.Time, tr.Weekdays, tr.MonthDays = "", nil, "", nil, nil
}

// clearTriggerEventFields 清空事件字段与旧规则字段（定时事件用）。
func clearTriggerEventFields(tr *Trigger) {
	tr.MonitorPrebuilt, tr.MonitorScriptID, tr.MonitorDurationMin, tr.MonitorIntervalSec = "", 0, 0, 0
	tr.DiskGroups, tr.AllDay, tr.TimeRange, tr.WeekLimit, tr.MonthDayLimit, tr.ThresholdMin = nil, false, "", nil, nil, 0
}

// actionToExecutor 旧动作 → 新执行器（存量迁移）。
func actionToExecutor(a ScheduleAction) Executor {
	switch a.Type {
	case ActionFanCurve:
		if len(a.FanChannels) > 0 {
			return Executor{Type: ExecutorFanControl, FanID: a.FanChannels[0], FanManual: false, FanCurve: a.FanMode}
		}
	case ActionDiskPower:
		if len(a.DiskGroups) > 0 {
			return Executor{Type: ExecutorDiskGroupControl, DiskGroupID: a.DiskGroups[0], DiskAction: a.DiskAction, ForceOff: a.ForceOff}
		}
	case ActionRunTask:
		return Executor{Type: ExecutorControlTask, TaskID: a.TaskID, TaskAction: TaskActionRun,
			DelaySec: a.DelaySec, RespectEnabled: a.RespectEnabled, RespectTimeRule: a.RespectTimeRule}
	case ActionEnableTask:
		return Executor{Type: ExecutorControlTask, TaskID: a.TaskID, TaskAction: TaskActionEnable}
	case ActionDisableTask:
		return Executor{Type: ExecutorControlTask, TaskID: a.TaskID, TaskAction: TaskActionDisable}
	}
	return Executor{Type: ExecutorControlTask}
}

// normalizeExecutors 校验并归一化执行器清单；返回空串表示通过，否则为错误消息。
// 每类执行器清空无关字段（保持存储一致）。
func normalizeExecutors(executors []Executor) string {
	for i := range executors {
		e := &executors[i]
		if e.DelaySec < 0 || e.DelaySec > maxScheduleActionDelaySec {
			return fmt.Sprintf("执行器延迟范围 0~%d 秒", maxScheduleActionDelaySec)
		}
		switch e.Type {
		case ExecutorFanControl:
			if e.FanID < 1 || e.FanID > MaxFanChannels {
				return "风扇控制执行器必须选择有效风扇（FAN1/FAN2）"
			}
			if e.FanManual {
				if e.FanPercent < 1 || e.FanPercent > 100 {
					return "手动风扇转速范围 1~100%"
				}
				e.FanCurve = ""
			} else {
				if e.FanCurve != "" && e.FanCurve != FanCurveEfficient && e.FanCurve != FanCurveDaily && e.FanCurve != FanCurveQuiet {
					return "自动风扇曲线无效"
				}
				e.FanPercent = 0
			}
			e.DiskGroupID, e.DiskAction, e.KillOccupied, e.ForceOff = 0, "", false, false
			e.RetryEnabled, e.MaxRetries, e.RetryIntervalSec = false, 0, 0
			e.TaskID, e.TaskAction, e.RespectEnabled, e.RespectTimeRule = 0, "", false, false
			e.ScriptPrebuilt, e.ScriptID, e.ScriptCode, e.ScriptType = "", 0, "", ""
		case ExecutorDiskGroupControl:
			if e.DiskGroupID < 1 || e.DiskGroupID > MaxDiskGroups {
				return "硬盘组控制执行器必须选择有效硬盘组"
			}
			if e.DiskAction != DiskActionOnline && e.DiskAction != DiskActionOffline {
				return "硬盘组控制动作无效"
			}
			if e.DiskAction == DiskActionOnline {
				e.KillOccupied, e.ForceOff = false, false
			}
			if e.RetryEnabled {
				if e.MaxRetries < 1 || e.MaxRetries > 10 {
					e.MaxRetries = 3
				}
				if e.RetryIntervalSec < 1 || e.RetryIntervalSec > 3600 {
					e.RetryIntervalSec = 10
				}
			} else {
				e.MaxRetries, e.RetryIntervalSec = 0, 0
			}
			e.FanID, e.FanEnabled, e.FanManual, e.FanPercent, e.FanCurve = 0, nil, false, 0, ""
			e.TaskID, e.TaskAction, e.RespectEnabled, e.RespectTimeRule = 0, "", false, false
			e.ScriptPrebuilt, e.ScriptID, e.ScriptCode, e.ScriptType = "", 0, "", ""
		case ExecutorControlTask:
			if e.TaskID <= 0 {
				return "控制任务执行器必须选择目标任务"
			}
			switch e.TaskAction {
			case TaskActionEnable, TaskActionDisable, TaskActionRun:
			default:
				return "控制任务操作无效"
			}
			e.FanID, e.FanEnabled, e.FanManual, e.FanPercent, e.FanCurve = 0, nil, false, 0, ""
			e.DiskGroupID, e.DiskAction, e.KillOccupied, e.ForceOff = 0, "", false, false
			e.RetryEnabled, e.MaxRetries, e.RetryIntervalSec = false, 0, 0
			e.ScriptPrebuilt, e.ScriptID, e.ScriptCode, e.ScriptType = "", 0, "", ""
		case ExecutorExecScript:
			if e.ScriptPrebuilt == "" && e.ScriptID <= 0 && strings.TrimSpace(e.ScriptCode) == "" {
				return "执行脚本执行器必须选择预制/自定义脚本或填写脚本代码"
			}
			// 内联代码脚本类型规范化；引用资源/预制时类型字段不生效
			e.ScriptType = normalizeScriptType(e.ScriptType)
			if e.ScriptID > 0 || e.ScriptPrebuilt != "" {
				e.ScriptType = ""
			}
			if e.RetryEnabled {
				if e.MaxRetries < 1 || e.MaxRetries > 10 {
					e.MaxRetries = 3
				}
				if e.RetryIntervalSec < 1 || e.RetryIntervalSec > 3600 {
					e.RetryIntervalSec = 10
				}
			} else {
				e.MaxRetries, e.RetryIntervalSec = 0, 0
			}
			e.FanID, e.FanEnabled, e.FanManual, e.FanPercent, e.FanCurve = 0, nil, false, 0, ""
			e.DiskGroupID, e.DiskAction, e.KillOccupied, e.ForceOff = 0, "", false, false
			e.TaskID, e.TaskAction, e.RespectEnabled, e.RespectTimeRule = 0, "", false, false
		default:
			return "未知执行器类型"
		}
	}
	return ""
}

// topLevelAction 顶层便捷动作字段 → 动作结构（迁移用）。
func topLevelAction(sc Schedule) ScheduleAction {
	if sc.Category == ScheduleCategoryFan {
		return ScheduleAction{Type: ActionFanCurve, FanChannels: sc.FanChannels, FanMode: sc.FanMode}
	}
	return ScheduleAction{Type: ActionDiskPower, DiskGroups: sc.DiskGroups, DiskAction: sc.DiskAction, ForceOff: sc.ForceOff}
}

// inferScheduleCategory 未显式指定类别时的推导（fan/disk）。
func inferScheduleCategory(sc *Schedule) string {
	for _, a := range sc.Actions {
		if a.Type == ActionDiskPower {
			return ScheduleCategoryDisk
		}
	}
	if len(sc.FanChannels) > 0 || len(sc.FanMode) > 0 {
		return ScheduleCategoryFan
	}
	if len(sc.DiskGroups) > 0 || len(sc.DiskAction) > 0 {
		return ScheduleCategoryDisk
	}
	return ScheduleCategoryFan
}

// backfillTriggerFields 从 Triggers 回填 Schedule 顶层便捷字段
// （存量展示/日志/旧前端兼容；执行与 per-trigger 防重复以 Triggers 为准）。
// 取首张触发器映射：时间型 → ScheduleType/RunAt/Time/Weekdays/MonthDays；
// 事件型 → ScheduleType=trigger/TriggerRule + 顶层 DiskGroups
// （顶层 DiskGroups 同时供 topLevelMode 的动作迁移使用，与旧语义一致；
// 旧 triggerKind 字段已移除，不再回填）。
func backfillTriggerFields(sc *Schedule) {
	if len(sc.Triggers) == 0 {
		return
	}
	t0 := sc.Triggers[0]
	switch t0.Type {
	case TriggerTypeTime:
		switch t0.Period {
		case TriggerPeriodOnce:
			sc.ScheduleType, sc.RunAt = ScheduleTypeOneTime, t0.RunAt
			sc.Time, sc.Weekdays, sc.MonthDays = "", nil, nil
		case TriggerPeriodDaily:
			sc.ScheduleType, sc.Time = ScheduleTypeDaily, t0.Time
			sc.RunAt, sc.Weekdays, sc.MonthDays = nil, nil, nil
		case TriggerPeriodWeekly:
			sc.ScheduleType, sc.Time, sc.Weekdays = ScheduleTypeWeekly, t0.Time, t0.Weekdays
			sc.RunAt, sc.MonthDays = nil, nil
		case TriggerPeriodMonthly:
			sc.ScheduleType, sc.Time, sc.MonthDays = ScheduleTypeMonthly, t0.Time, t0.MonthDays
			sc.RunAt, sc.Weekdays = nil, nil
		case TriggerPeriodLoop:
			// loop 周期无对应 ScheduleType（仅通过 Triggers[].Period 生效）
			sc.RunAt, sc.Time, sc.Weekdays, sc.MonthDays = nil, "", nil, nil
		}
		sc.TriggerRule = nil
	case TriggerTypeLog:
		sc.ScheduleType = ScheduleTypeTrigger
		sc.TriggerRule = &TriggerRule{
			AllDay:        t0.AllDay,
			TimeRange:     t0.TimeRange,
			WeekLimit:     t0.WeekLimit,
			MonthDayLimit: t0.MonthDayLimit,
			ThresholdMin:  t0.ThresholdMin,
		}
		sc.RunAt, sc.Time, sc.Weekdays, sc.MonthDays = nil, "", nil, nil
		sc.DiskGroups = t0.DiskGroups
	case TriggerTypeMonitor:
		sc.ScheduleType = ScheduleTypeTrigger
		sc.TriggerRule = &TriggerRule{
			AllDay:        t0.AllDay,
			TimeRange:     t0.TimeRange,
			WeekLimit:     t0.WeekLimit,
			MonthDayLimit: t0.MonthDayLimit,
		}
		sc.RunAt, sc.Time, sc.Weekdays, sc.MonthDays = nil, "", nil, nil
		sc.DiskGroups = t0.DiskGroups
	}
}

// normalizeActions 校验并归一化一组动作；返回空串表示通过，否则为错误消息。
// 每类动作清空无关字段（保持存储一致）。
func normalizeActions(actions []ScheduleAction) string {
	for i := range actions {
		a := &actions[i]
		switch a.Type {
		case ActionFanCurve:
			if len(a.FanChannels) == 0 || !validIDs(a.FanChannels, 1, MaxFanChannels) {
				return "风扇动作至少选择一个有效通道（FAN1/FAN2）"
			}
			if a.FanMode != FanCurveEfficient && a.FanMode != FanCurveDaily && a.FanMode != FanCurveQuiet {
				return "风扇模式无效"
			}
			a.DiskGroups, a.DiskAction, a.ForceOff = nil, "", false
			a.TaskID, a.DelaySec, a.RespectEnabled, a.RespectTimeRule = 0, 0, false, false
		case ActionDiskPower:
			if len(a.DiskGroups) == 0 || !validIDs(a.DiskGroups, 1, MaxDiskGroups) {
				return "硬盘动作至少选择一个有效硬盘组"
			}
			if a.DiskAction != DiskActionOnline && a.DiskAction != DiskActionOffline {
				return "硬盘动作无效"
			}
			if a.DiskAction == DiskActionOnline {
				a.ForceOff = false
			}
			a.FanChannels, a.FanMode = nil, ""
			a.TaskID, a.DelaySec, a.RespectEnabled, a.RespectTimeRule = 0, 0, false, false
		case ActionRunTask:
			if a.TaskID <= 0 {
				return "执行任务动作必须选择目标任务"
			}
			if a.DelaySec < 0 || a.DelaySec > maxScheduleActionDelaySec {
				return fmt.Sprintf("投递延迟范围 0~%d 秒", maxScheduleActionDelaySec)
			}
			a.FanChannels, a.FanMode, a.DiskGroups, a.DiskAction, a.ForceOff = nil, "", nil, "", false
		case ActionEnableTask, ActionDisableTask:
			if a.TaskID <= 0 {
				return "任务开关动作必须选择目标任务"
			}
			a.FanChannels, a.FanMode, a.DiskGroups, a.DiskAction, a.ForceOff = nil, "", nil, "", false
			a.DelaySec, a.RespectEnabled, a.RespectTimeRule = 0, false, false
		default:
			return "未知动作类型"
		}
	}
	return ""
}

// validateScheduleTargets 校验执行器引用的目标任务存在
// （control_task 的 TaskID + 成功/失败后执行任务 SuccessTaskID/FailureTaskID）。
func validateScheduleTargets(store *Store, sc Schedule) string {
	all := store.GetSchedules()
	exists := func(id int) bool {
		for _, e := range all {
			if e.ID == id {
				return true
			}
		}
		return false
	}
	for _, e := range sc.Executors {
		if e.Type == ExecutorControlTask {
			if e.TaskID <= 0 {
				return "控制任务执行器缺少目标任务"
			}
			if !exists(e.TaskID) {
				return fmt.Sprintf("目标任务 #%d 不存在", e.TaskID)
			}
		}
		if e.SuccessTaskID > 0 && !exists(e.SuccessTaskID) {
			return fmt.Sprintf("成功后执行任务 #%d 不存在", e.SuccessTaskID)
		}
		if e.FailureTaskID > 0 && !exists(e.FailureTaskID) {
			return fmt.Sprintf("失败后执行任务 #%d 不存在", e.FailureTaskID)
		}
	}
	return ""
}

// checkScheduleCycles 检测任务串联环（含自环 A→A）；返回错误消息（空=无环）。
// 基于执行器清单：control_task 的 run 操作 + 成功/失败后执行任务均参与。
func checkScheduleCycles(schedules []Schedule) string {
	edges := map[int][]int{}
	ids := map[int]bool{}
	for _, sc := range schedules {
		ids[sc.ID] = true
		var targets []int
		for _, e := range sc.Executors {
			if e.Type == ExecutorControlTask && e.TaskAction == TaskActionRun && e.TaskID > 0 {
				targets = append(targets, e.TaskID)
			}
			if e.SuccessTaskID > 0 {
				targets = append(targets, e.SuccessTaskID)
			}
			if e.FailureTaskID > 0 {
				targets = append(targets, e.FailureTaskID)
			}
		}
		if len(targets) > 0 {
			edges[sc.ID] = targets
		}
	}
	state := map[int]int{} // 0=未访问 1=访问中(当前路径) 2=已完成
	var visit func(id int) string
	visit = func(id int) string {
		switch state[id] {
		case 1:
			return fmt.Sprintf("任务串联存在循环引用（任务 #%d）", id)
		case 2:
			return ""
		}
		state[id] = 1
		for _, t := range edges[id] {
			if !ids[t] {
				continue // 目标不存在由 validateScheduleTargets 处理
			}
			if msg := visit(t); msg != "" {
				return msg
			}
		}
		state[id] = 2
		return ""
	}
	for id := range edges {
		if msg := visit(id); msg != "" {
			return msg
		}
	}
	return ""
}

// validTimeHHMM 校验 "HH:mm"（24 小时制）时间格式。
func validTimeHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
}

// validTimeRange 校验 "HH:mm-HH:mm" 时间范围格式（起点早于终点）。
func validTimeRange(s string) bool {
	if len(s) != 11 || s[5] != '-' {
		return false
	}
	if !validTimeHHMM(s[:5]) || !validTimeHHMM(s[6:]) {
		return false
	}
	h1, _ := strconv.Atoi(s[:2])
	h2, _ := strconv.Atoi(s[6:8])
	m1, _ := strconv.Atoi(s[3:5])
	m2, _ := strconv.Atoi(s[9:11])
	return h1*60+m1 < h2*60+m2
}

// validIDs 校验整型列表：空列表视为"未设置"（合法，不限）；
// 非空时值必须全部在 [min,max] 且无重复。
func validIDs(ids []int, min, max int) bool {
	if len(ids) == 0 {
		return true
	}
	seen := make(map[int]bool)
	for _, v := range ids {
		if v < min || v > max || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// ===== 任务执行日志 =====

// getScheduleLogs 查询任务执行日志（筛选 + 分页 + 下拉标签）。
func (h *Handlers) getScheduleLogs(c *gin.Context) {
	st := GetScheduleLogStore()
	if st == nil {
		c.JSON(200, gin.H{"total": 0, "page": 1, "size": 20, "items": []gin.H{}})
		return
	}
	p := ScheduleLogQueryParams{Page: 1, PageSize: 20}
	p.TaskName = strings.TrimSpace(c.Query("name"))
	p.Category = c.Query("category")
	p.ScheduleType = c.Query("type")
	p.TriggerType = c.Query("triggerType")
	p.Result = c.Query("result")
	if s := c.Query("from"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.From = t
		}
	}
	if s := c.Query("to"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.To = t
		}
	}
	if s := c.Query("page"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			p.Page = n
		}
	}
	if s := c.Query("pageSize"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			p.PageSize = n
		}
	}
	res := st.Query(p)
	items := make([]gin.H, 0, len(res.Items))
	for _, e := range res.Items {
		items = append(items, gin.H{
			"id":               e.ID,
			"scheduleId":       e.ScheduleID,
			"taskName":         e.TaskName,
			"category":         e.Category,
			"scheduleType":     e.ScheduleType,
			"channels":         e.Channels,
			"result":           e.Result,
			"detail":           e.Detail,
			"time":             e.Time.Format(time.RFC3339),
			"sourceScheduleId": e.SourceScheduleID,
			"sourceResult":     e.SourceResult,
			"triggerKey":       e.TriggerKey,
			"triggerType":      e.TriggerType,
		})
	}
	c.JSON(200, gin.H{
		"total":             res.Total,
		"page":              res.Page,
		"size":              res.Size,
		"items":             items,
		"typeLabels":        scheduleLogTypeLabels(),
		"categoryLabels":    scheduleLogCategoryLabels(),
		"triggerTypeLabels": scheduleLogTriggerTypeLabels(),
		"resultLabels":      scheduleLogResultLabels(),
		"retainDays":        st.RetainDays(),
	})
}

// setScheduleLogConfig 设置任务日志保留天数（1/7/30）。
func (h *Handlers) setScheduleLogConfig(c *gin.Context) {
	var body struct {
		RetainDays int `json:"retainDays"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if body.RetainDays != ScheduleRetainDays1 && body.RetainDays != ScheduleRetainDays7 && body.RetainDays != ScheduleRetainDays30 {
		c.JSON(400, gin.H{"error": "保留天数仅支持 1/7/30 天"})
		return
	}
	s := h.store.GetSettings()
	s.ScheduleLogRetainDays = body.RetainDays
	h.store.SaveSettings(s)
	if st := GetScheduleLogStore(); st != nil {
		st.SetRetainDays(body.RetainDays)
	}
	logger.Info("schedule", "schedule log retain days set to %d", body.RetainDays)
	c.JSON(200, gin.H{"ok": true, "retainDays": body.RetainDays})
}
