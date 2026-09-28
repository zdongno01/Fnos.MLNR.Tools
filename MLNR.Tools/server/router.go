package main

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// newRouter 构建所有路由，统一挂在网关前缀 /app/mlnr 下。
func newRouter(cfg Config, h *Handlers) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gatewayUser())

	g := r.Group(gwPrefix)

	// ===== REST API =====
	api := g.Group("/api")
	{
		api.GET("/info", h.info)
		api.GET("/status", h.getStatus)

		// 连接管理
		api.POST("/connect", requireAdmin(), h.connect)
		api.POST("/disconnect", requireAdmin(), h.disconnect)
		api.POST("/scan", requireAdmin(), h.scan)
		api.POST("/scan/start", requireAdmin(), h.scanStart)
		api.POST("/scan/stop", requireAdmin(), h.scanStop)
		api.POST("/handshake", requireAdmin(), h.handshake)
		api.POST("/refresh", requireAdmin(), h.refresh)

		// 风扇控制
		api.POST("/fan/speed", requireAdmin(), h.setFanSpeed)
		api.POST("/fan/mode", requireAdmin(), h.setFanMode)

		// 开关控制
		api.POST("/switch", requireAdmin(), h.setSwitch)

		// 硬盘组管理
		api.GET("/disks/list", h.listDisks)
		api.GET("/disks/partitions", h.listPartitions)
		api.POST("/disk/group/:id/poweron", requireAdmin(), h.diskPowerOn)
		api.POST("/disk/group/:id/poweroff", requireAdmin(), h.diskPowerOff)
		api.POST("/disk/group/:id/forceoff", requireAdmin(), h.diskForcePowerOff)
		api.POST("/disk/group/:id/mount", requireAdmin(), h.diskMount)
		api.POST("/disk/group/:id/unmount", requireAdmin(), h.diskUnmount)
		// 硬盘组操作日志
		api.GET("/disk/logs", h.getDiskLogs)

		// 定时计划（任务 CRUD + 执行日志）
		api.GET("/schedules", h.listSchedules)
		api.POST("/schedules", requireAdmin(), h.createSchedule)
		api.PUT("/schedules/:id", requireAdmin(), h.updateSchedule)
		api.DELETE("/schedules/:id", requireAdmin(), h.deleteSchedule)
		api.POST("/schedules/:id/run", requireAdmin(), h.runSchedule)
		api.GET("/schedule-logs", h.getScheduleLogs)
		api.PUT("/schedule-log-config", requireAdmin(), h.setScheduleLogConfig)

		// 定时计划资源（M13：监控脚本 / 执行脚本 / 日志事件）
		api.GET("/monitor-scripts", h.listMonitorScripts)
		api.POST("/monitor-scripts", requireAdmin(), h.createMonitorScript)
		api.PUT("/monitor-scripts/:id", requireAdmin(), h.updateMonitorScript)
		api.DELETE("/monitor-scripts/:id", requireAdmin(), h.deleteMonitorScript)
		api.POST("/monitor-scripts/test", requireAdmin(), h.testMonitorScript)
		api.GET("/exec-scripts", h.listExecScripts)
		api.POST("/exec-scripts", requireAdmin(), h.createExecScript)
		api.PUT("/exec-scripts/:id", requireAdmin(), h.updateExecScript)
		api.DELETE("/exec-scripts/:id", requireAdmin(), h.deleteExecScript)
		api.POST("/exec-scripts/test", requireAdmin(), h.testExecScript)
		api.GET("/log-events", h.listLogEvents)
		api.POST("/log-events", requireAdmin(), h.createLogEvent)
		api.PUT("/log-events/:id", requireAdmin(), h.updateLogEvent)
		api.DELETE("/log-events/:id", requireAdmin(), h.deleteLogEvent)
		api.POST("/log-events/test", requireAdmin(), h.testLogEvent)

		// 硬件配置下发
		api.POST("/fan/config", requireAdmin(), h.setFanConfig)
		api.POST("/switch/config", requireAdmin(), h.setSwitchConfig)
		api.GET("/global/config", h.getGlobalConfig)
		api.POST("/global/config", requireAdmin(), h.setGlobalConfig)
		api.POST("/sensor/enabled", requireAdmin(), h.setSensorEnabled)
		api.POST("/sensor/interval", requireAdmin(), h.setSensorInterval)
		api.GET("/sensor/channels", h.getSensorChannels)
		api.POST("/sensor/channels/:id", requireAdmin(), h.setSensorChannel)
		api.POST("/hw/save", requireAdmin(), h.saveHardwareConfig)
		api.POST("/hw/reset", requireAdmin(), h.factoryReset)

		// BLE 广播名与调试模式
		api.GET("/ble/name", h.getBleName)
		api.PUT("/ble/name", requireAdmin(), h.setBleName)
		api.GET("/ble/debug-mode", h.getDebugMode)
		api.PUT("/ble/debug-mode", requireAdmin(), h.setDebugMode)

		// 上位机设置
		api.GET("/settings", h.getSettings)
		api.PUT("/settings", requireAdmin(), h.saveSettings)

		// 温度与历史
		api.GET("/temperature", h.getTemperature)
		api.GET("/history", h.getHistory)

		// 系统信息（需求 #6：fnOS 主题，供上位机"自动"模式跟随）
		api.GET("/system/theme", h.getSystemTheme)

		// 日志
		api.GET("/logs", requireAdmin(), h.getLogs)

		// 日志配置
		api.GET("/logconfig", h.getLogConfig)
		api.PUT("/logconfig", requireAdmin(), h.setLogConfig)
	}

	// WebSocket
	g.GET("/ws", h.hub.HandleWS)

	// ===== 前端静态资源 =====
	serveUI(g, cfg.UIDir)

	// SPA 兜底
	if cfg.UIDir != "" {
		indexFile := filepath.Join(cfg.UIDir, "index.html")
		r.NoRoute(func(c *gin.Context) {
			p := c.Request.URL.Path
			if !strings.HasPrefix(p, gwPrefix+"/") && p != gwPrefix {
				c.String(http.StatusNotFound, "not found")
				return
			}
			if strings.HasPrefix(p, gwPrefix+"/config") {
				c.String(http.StatusForbidden, "forbidden")
				return
			}
			c.File(indexFile)
		})
	}

	return r
}

// serveUI 提供前端静态文件。
func serveUI(g *gin.RouterGroup, uiDir string) {
	if uiDir == "" {
		return
	}
	indexFile := filepath.Join(uiDir, "index.html")
	g.GET("/", func(c *gin.Context) { c.File(indexFile) })
	for _, sub := range []string{"assets", "images"} {
		g.StaticFS("/"+sub, http.Dir(filepath.Join(uiDir, sub)))
	}
}
