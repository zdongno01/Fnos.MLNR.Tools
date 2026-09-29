// Package main 是 Fnos Fan Controller的常驻后端服务。
//
// 通过统一网关 (gatewayPrefix=/app/mlnr) 暴露 HTTP 与 WebSocket，
// 监听 fnOS 转发到的 Unix Socket (默认 ${TRIM_APPDEST}/app.sock)。
//
// 运行时环境变量 (由 cmd/main 注入):
//
//	mlnr_SOCK     - Unix Socket 路径
//	mlnr_UIDIR    - 前端静态资源目录 (app/ui)
//	mlnr_DATADIR  - 数据/日志目录 (${TRIM_PKGVAR})
//	mlnr_DEV=1    - 本地开发模式，监听 TCP 127.0.0.1:8088 而非 Unix Socket
//	mlnr_ADDR     - 开发模式监听地址 (默认 127.0.0.1:8088)
package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"mlnr/logger"
)

const (
	appName  = "mlnr"
	appVer   = "0.1.0"
	gwPrefix = "/app/mlnr"
	sockName = "app.sock"
)

// Config 保存服务运行配置。
type Config struct {
	SockPath string // Unix Socket 路径 (生产)
	UIDir    string // 前端静态资源目录
	DataDir  string // 数据目录
	DevMode  bool   // 是否本地开发模式
	DevAddr  string // 开发模式 TCP 监听地址
}

func loadConfig() Config {
	c := Config{
		SockPath: os.Getenv("mlnr_SOCK"),
		UIDir:    os.Getenv("mlnr_UIDIR"),
		DataDir:  os.Getenv("mlnr_DATADIR"),
		DevMode:  os.Getenv("mlnr_DEV") == "1",
		DevAddr:  os.Getenv("mlnr_ADDR"),
	}
	if c.DevAddr == "" {
		c.DevAddr = "127.0.0.1:8088"
	}
	return c
}

// applyDevDefaults 填充开发模式下的默认值。
func applyDevDefaults(c *Config) {
	if !c.DevMode {
		return
	}
	if c.UIDir == "" {
		c.UIDir = filepath.Join("..", "app", "ui")
	}
	if c.DataDir == "" {
		c.DataDir = "."
	}
	if c.SockPath == "" {
		c.SockPath = filepath.Join(os.TempDir(), "mlnr.sock")
	}
	c.UIDir = absPath(c.UIDir)
	c.DataDir = absPath(c.DataDir)
}

func absPath(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func main() {
	dev := flag.Bool("dev", false, "本地开发模式：监听 TCP 而非 Unix Socket")
	flag.Parse()

	cfg := loadConfig()
	if *dev {
		cfg.DevMode = true
	}
	applyDevDefaults(&cfg)

	logger.Info("main", "tf-fan %s starting (dev=%v, dataDir=%s, uiDir=%s)", appVer, cfg.DevMode, cfg.DataDir, cfg.UIDir)

	// 确保数据目录存在
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			logger.Warn("main", "mkdir data dir %s: %v", cfg.DataDir, err)
		} else {
			logger.Debug("main", "data directory ready: %s", cfg.DataDir)
		}
	}

	// 日志写入数据目录（@appdata/<app>/logs）：安装目录 @appcenter 在应用更新时会
	// 重建且可能只读，日志必须落在数据目录才能持久并按"单文件上限"轮转
	logger.SetLogDir(cfg.DataDir)

	// 初始化存储
	store := NewStore(cfg.DataDir)
	if err := store.Load(); err != nil {
		logger.Fatal("main", "load store: %v", err)
	}

	// 初始化硬盘组操作日志（文件持久化，数据目录）
	InitDiskLogger(cfg.DataDir)
	// 统计快照定期落盘（每 10 分钟；停机时再保存一次）
	if dl := GetDiskLogger(); dl != nil {
		dl.StartAutoSave(10 * time.Minute)
	}

	// 初始化定时计划任务执行日志（按天文件 + 保留天数清理）
	if st := InitScheduleLogStore(cfg.DataDir); st != nil {
		st.SetRetainDays(store.GetSettings().ScheduleLogRetainDays)
	}

	// 从设置中加载日志级别
	var logLevel logger.LogLevel
	switch strings.ToUpper(os.Getenv("mlnr_LOG_LEVEL")) {
	case "DEBUG":
		logLevel = logger.DEBUG
	case "WARN":
		logLevel = logger.WARN
	case "ERROR":
		logLevel = logger.ERROR
	case "FATAL":
		logLevel = logger.FATAL
	default:
		logLevel = logger.INFO
	}
	logger.SetLogLevel(logLevel)

	// 初始化 WebSocket Hub
	hub := NewHub()

	// 初始化 BLE 管理器
	ble := NewBLEManager(store, hub)
	if err := ble.Start(); err != nil {
		logger.Warn("main", "BLE start failed: %v", err)
	}
	hub.SetBLE(ble)

	// 初始化硬盘组管理器
	disk := NewDiskManager(store, hub)
	disk.SetBLE(ble)
	ble.SetDiskManager(disk)
	hub.SetDisk(disk)

	// 启动时一次性加载磁盘信息全量缓存（参考 zdaitest001_go buildDiskSummary）
	// 缓存包含：lsblk + smartctl JSON + hwmon 温度 + df + /proc/mounts + sysfs 扇区
	// 后续 ListDisks/ListPartitions/温度采集都从内存读取，不再重复执行 lsblk/smartctl
	disk.LoadDiskInfo()

	// 初始化温度管理（采集温度、历史存储、用于自动调速）
	thermal := NewThermalManager(cfg.DataDir, hub, ble, disk)
	if err := thermal.Start(); err != nil {
		logger.Warn("main", "thermal start failed: %v", err)
	}
	hub.SetThermal(thermal)
	// 3.md：温度管理器按 SN 关联磁盘温度，注入硬盘组管理器
	disk.SetThermal(thermal)

	// 演示模式（mlnr_BLE_MOCK=1）：无真实硬件时的界面演示数据（截图/演示用）。
	// 必须在 Start 之后注入，避免真实采集覆盖演示快照；生产不设置该变量则无任何影响。
	if demoModeEnabled() {
		thermal.mockMode = true
		installBLEMock(ble)
		installThermalMock(thermal)
	}

	// 启动定时计划调度引擎（M2 时间任务 + M8 事件型触发任务）
	sched := NewScheduler(store, ble, disk)
	sched.Start()
	defer sched.Stop()
	// 硬盘组上电后/下电前任务触发接口注入（上电后任务异步触发；下电前任务同步等待）
	disk.SetScheduler(sched)

	// 启动时自动连接（失败时 reconnect goroutine 会重试）
	// Item 1：必须有上次成功连接的 MAC 地址才自动连，避免首次启动时盲目扫描
	// 关键修复：开机后蓝牙 daemon (bluetoothd) 需要时间初始化适配器和对象树，
	// 过早 Connect() 会遇到 DBus "Method Get with signature ss doesn't exist" transient 错误
	// (tinygo 内部 gap_linux.go Connect 在 BlueZ Device1 对象不存在时调用 Properties.Get 失败)。
	// 等待 8 秒让系统蓝牙服务完全就绪，再尝试首次连接。
	if s := store.GetSettings(); s.AutoConnect && strings.TrimSpace(s.LastAddress) != "" {
		logger.Debug("main", "auto-connect armed: AutoConnect=%v LastAddress=%q", s.AutoConnect, s.LastAddress)
		go func() {
			// 给 BlueZ daemon 充足的初始化时间（适配器上电 + 对象树构建）
			logger.Info("main", "waiting for BlueZ daemon to settle (8s)...")
			time.Sleep(8 * time.Second)
			logger.Debug("main", "8s settle done, invoking ble.Connect()")
			logger.Info("main", "auto-connecting to device...")
			if err := ble.Connect(); err != nil {
				logger.Warn("main", "auto-connect failed: %v (reconnect watcher will retry)", err)
			}
		}()
	} else if s := store.GetSettings(); s.AutoConnect {
		logger.Info("main", "auto-connect skipped: no previous successful connection (LastAddress empty)")
	} else {
		logger.Debug("main", "auto-connect disabled (AutoConnect=%v)", s.AutoConnect)
	}

	// 构建路由
	h := &Handlers{store: store, ble: ble, hub: hub, thermal: thermal, disk: disk, scheduler: sched}
	gin.SetMode(gin.ReleaseMode)
	router := newRouter(cfg, h)

	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := buildListener(cfg)
	if err != nil {
		logger.Fatal("main", "build listener: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("main", "tf-fan %s listening on %s (dev=%v)", appVer, ln.Addr(), cfg.DevMode)
		if err := http.Serve(ln, srv.Handler); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("main", "serve: %v", err)
		}
	}()

	<-ctx.Done()
	logger.Info("main", "shutting down ...")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	// 停机前落盘统计快照（累计值不因日志滚动丢失）
	if dl := GetDiskLogger(); dl != nil {
		dl.SaveStats()
	}
	// 先停调度器（不再发起新任务），再停依赖组件
	sched.Stop()
	thermal.Stop()
	ble.Stop()
	hub.CloseAll()
	_ = ln.Close()
	if !cfg.DevMode && cfg.SockPath != "" {
		_ = os.Remove(cfg.SockPath)
	}
	logger.Info("main", "stopped.")
}

// buildListener 根据模式创建 Unix Socket 或 TCP 监听器。
func buildListener(cfg Config) (net.Listener, error) {
	if cfg.DevMode {
		return net.Listen("tcp", cfg.DevAddr)
	}
	if cfg.SockPath == "" {
		return nil, errors.New("mlnr_SOCK 未设置")
	}
	if err := os.Remove(cfg.SockPath); err != nil && !os.IsNotExist(err) {
		logger.Warn("main", "remove stale socket %s: %v", cfg.SockPath, err)
	}
	ln, err := net.Listen("unix", cfg.SockPath)
	if err != nil {
		return nil, err
	}
	// FS5: 收紧 socket 权限——0o666 会允许本机任意用户伪造网关身份（X-Trim-* 头）
	_ = os.Chmod(cfg.SockPath, 0o660)
	return ln, nil
}
