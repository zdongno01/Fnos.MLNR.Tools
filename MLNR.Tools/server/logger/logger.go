package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	FATAL
)

var levelNames = []string{"DEBUG", "INFO", "WARN", "ERROR", "FATAL"}
var levelColors = []string{"\033[90m", "\033[37m", "\033[33m", "\033[31m", "\033[35m"}

type LogEntry struct {
	Time    time.Time
	Level   LogLevel
	Module  string
	Message string
}

var (
	logMutex      sync.RWMutex
	logBuffer     []LogEntry
	maxBufferSize = 1000
	logHooks      []func(entry LogEntry)
	fileWriter    *os.File
	logDir        string
	logDataDir    string // 数据目录（mlnr_DATADIR），用于持久化 log_config.env 供启动脚本轮转 info.log
	logMaxSizeMB  int64    = 10
	logKeepDays   int      = 7
	logLevel      LogLevel = INFO
	// filterPatterns：日志消息包含这些子串时跳过（tinygo 库噪声）
	filterPatterns []string
	// consoleCh：stdout 输出异步队列（有界）。
	// 背景：log() 曾在调用 goroutine 内同步 fmt.Print 写 stdout，而 BLE 的
	// Scan 回调会高频打日志；fnOS 上 stdout 重定向（服务脚本管道/慢文件）一旦
	// 变慢，fmt.Print 阻塞 → 回调阻塞 → tinygo 的 DBus 信号循环卡死 → StopScan
	// 无法生效 → 自动连接永远卡在扫描。改为异步队列 + 非阻塞发送，日志 I/O
	// 不再阻塞任何业务路径。
	consoleCh chan string
)

func init() {
	logDir = filepath.Join(getAppDir(), "logs")
	os.MkdirAll(logDir, 0755)
	rotateLogFile()
	go startCleanupTimer()

	// stdout 异步输出：有界缓冲，缓冲满时丢弃（日志不丢主流程）
	consoleCh = make(chan string, 512)
	go func() {
		for line := range consoleCh {
			fmt.Print(line)
		}
	}()

	// 历史说明：曾在此处 RegisterLogFilter("org.freedesktop.DBus.Properties...doesn't exist")
	// 过滤 tinygo 蓝牙库的 BlueZ DBus 瞬时错误，但过滤器会误伤我们自己打的 WARN
	// 级日志（因为错误子串出现在 WARN 消息里）。现在 connectAddress() 内部已将 transient
	// 错误降为 DEBUG 级别，WARN 只保留真实故障，不再需要过滤器。
}

func getAppDir() string {
	exePath, err := os.Executable()
	if err != nil {
		dir, err := os.Getwd()
		if err != nil {
			return "."
		}
		return dir
	}
	return filepath.Dir(exePath)
}

func SetLogLevel(level LogLevel) {
	logMutex.Lock()
	defer logMutex.Unlock()
	logLevel = level
}

func GetLogLevel() LogLevel {
	logMutex.RLock()
	defer logMutex.RUnlock()
	return logLevel
}

// LevelName 将 LogLevel 转为字符串名称。
func LevelName(level LogLevel) string {
	i := int(level)
	if i >= 0 && i < len(levelNames) {
		return levelNames[i]
	}
	return "UNKNOWN"
}

func SetLogConfig(maxSizeMB int64, keepDays int) {
	logMutex.Lock()
	defer logMutex.Unlock()
	logMaxSizeMB = maxSizeMB
	logKeepDays = keepDays
	persistLogConfigLocked()
}

// persistLogConfigLocked 把当前单文件上限写入数据目录下的 log_config.env，
// 供 cmd/main 启动脚本的 info.log 轮转循环读取（UI 配置与 stdout 日志轮转联动）。
// 必须在持有 logMutex 时调用。
func persistLogConfigLocked() {
	if logDataDir == "" {
		return
	}
	p := filepath.Join(logDataDir, "log_config.env")
	content := fmt.Sprintf("MAX_LOG_MB=%d\n", logMaxSizeMB)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		// 写配置失败不阻塞日志主流程，仅记录一次（走 stderr 可能也被重定向，静默即可）
		return
	}
}

// SetLogDir 设置日志根目录：生产环境传入数据目录（mlnr_DATADIR），
// 日志写入 <dataDir>/logs/<日期>/app.log，并落盘 log_config.env。
// dataDir 为空时回退到可执行文件所在目录（本地开发默认行为）。
func SetLogDir(dataDir string) {
	logMutex.Lock()
	defer logMutex.Unlock()
	logDataDir = dataDir
	if dataDir != "" {
		logDir = filepath.Join(dataDir, "logs")
	} else {
		logDir = filepath.Join(getAppDir(), "logs")
	}
	os.MkdirAll(logDir, 0o755)
	persistLogConfigLocked()
	rotateLogFileInternal()
}

// GetLogConfig 返回当前日志配置（级别、单文件大小上限 MB、保留天数）。
func GetLogConfig() (level LogLevel, maxSizeMB int64, keepDays int) {
	logMutex.RLock()
	defer logMutex.RUnlock()
	return logLevel, logMaxSizeMB, logKeepDays
}

func rotateLogFile() {
	logMutex.Lock()
	defer logMutex.Unlock()
	rotateLogFileInternal()
}

func rotateLogFileInternal() {
	if fileWriter != nil {
		fileWriter.Close()
		fileWriter = nil
	}

	today := time.Now().Format("2006-01-02")
	dir := filepath.Join(logDir, today)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	path := filepath.Join(dir, "app.log")

	if _, err := os.Stat(path); err == nil {
		backupPath := filepath.Join(dir, fmt.Sprintf("app_%s.log", time.Now().Format("150405")))
		os.Rename(path, backupPath)
	}

	var err error
	fileWriter, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
}

func startCleanupTimer() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		cleanupOldLogs()
		checkLogSize()
	}
}

func cleanupOldLogs() {
	logMutex.RLock()
	keepDays := logKeepDays
	logMutex.RUnlock()
	if keepDays <= 0 {
		keepDays = 7
	}

	cutoff := time.Now().AddDate(0, 0, -keepDays)

	entries, err := os.ReadDir(logDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			date, err := time.Parse("2006-01-02", entry.Name())
			if err == nil && date.Before(cutoff) {
				os.RemoveAll(filepath.Join(logDir, entry.Name()))
			}
		}
	}
}

func checkLogSize() {
	logMutex.RLock()
	if fileWriter == nil {
		logMutex.RUnlock()
		return
	}

	fileInfo, err := fileWriter.Stat()
	// 在持锁状态下读取 logMaxSizeMB，避免与 SetLogConfig 数据竞争
	maxSizeBytes := logMaxSizeMB * 1024 * 1024
	logMutex.RUnlock()

	if err != nil {
		return
	}

	if maxSizeBytes <= 0 {
		maxSizeBytes = 10 * 1024 * 1024
	}

	if fileInfo.Size() >= maxSizeBytes {
		rotateLogFile()
	}
}

func AddUILogHook(hook func(entry LogEntry)) {
	logMutex.Lock()
	defer logMutex.Unlock()
	logHooks = append(logHooks, hook)
}

func Debug(module, format string, args ...interface{}) {
	log(DEBUG, module, fmt.Sprintf(format, args...))
}

func Info(module, format string, args ...interface{}) {
	log(INFO, module, fmt.Sprintf(format, args...))
}

func Warn(module, format string, args ...interface{}) {
	log(WARN, module, fmt.Sprintf(format, args...))
}

// 限频告警：同一 key 在 interval 内最多输出一条 WARN，用于抑制高频刷屏
// （如 BLE 应答通道满、WS 连接断开等预期内/持续发生的场景），
// 避免 stdout 重定向的 info.log 被瞬间灌爆。
var (
	rateLimitMu   sync.Mutex
	rateLimitLast = map[string]time.Time{}
)

func WarnRateLimited(module, key string, interval time.Duration, format string, args ...interface{}) {
	rateLimitMu.Lock()
	now := time.Now()
	last, ok := rateLimitLast[key]
	if ok && now.Sub(last) < interval {
		rateLimitMu.Unlock()
		return
	}
	rateLimitLast[key] = now
	rateLimitMu.Unlock()
	Warn(module, format, args...)
}

func Error(module, format string, args ...interface{}) {
	log(ERROR, module, fmt.Sprintf(format, args...))
}

func Fatal(module, format string, args ...interface{}) {
	// FS5: log() 内部已对 FATAL 级别执行 os.Exit，此处不再重复调用
	log(FATAL, module, fmt.Sprintf(format, args...))
}

// RegisterLogFilter 注册消息子串过滤规则；日志消息包含任一字串时跳过（不写入 buffer/file/console）。
// 用于抑制 tinygo / BlueZ 等底层库的瞬时、重复、非致命警告。
func RegisterLogFilter(substring string) {
	logMutex.Lock()
	defer logMutex.Unlock()
	filterPatterns = append(filterPatterns, substring)
}

func shouldFilter(message string) bool {
	logMutex.RLock()
	defer logMutex.RUnlock()
	for _, p := range filterPatterns {
		if strings.Contains(message, p) {
			return true
		}
	}
	return false
}

func log(level LogLevel, module, message string) {
	logMutex.RLock()
	currentLevel := logLevel
	logMutex.RUnlock()

	if level < currentLevel {
		return
	}

	// Item 1：自动过滤特定子串（tinygo 蓝牙库 DBus 噪声）
	if shouldFilter(message) {
		return
	}

	message = sanitize(message)

	entry := LogEntry{
		Time:    time.Now(),
		Level:   level,
		Module:  module,
		Message: message,
	}

	logMutex.Lock()
	logBuffer = append(logBuffer, entry)
	if len(logBuffer) > maxBufferSize {
		logBuffer = logBuffer[len(logBuffer)-maxBufferSize:]
	}

	if fileWriter == nil {
		// 持锁调用 rotateLogFileInternal，避免多 goroutine 并发打开文件、覆盖 fileWriter
		rotateLogFileInternal()
	}

	if fileWriter != nil {
		logLine := fmt.Sprintf("[%s] [%s] [%s] %s\n",
			entry.Time.Format("2006-01-02 15:04:05"),
			levelNames[level],
			module,
			message)
		if _, err := fileWriter.WriteString(logLine); err != nil {
			fileWriter.Close()
			fileWriter = nil
		} else if level >= ERROR {
			// FS5: 仅 ERROR/FATAL 强制落盘，避免每行 fsync 在 NAS 上拖垮性能
			fileWriter.Sync()
		}
	}

	// 持锁拷贝 hook 列表，释放锁后再同步调用，避免：
	// 1) 每条日志×每个 hook 起一个 goroutine 的开销；
	// 2) hook 内部若再次打日志会与持锁状态死锁。
	hooks := make([]func(entry LogEntry), len(logHooks))
	copy(hooks, logHooks)

	logMutex.Unlock()

	for _, hook := range hooks {
		hook(entry)
	}

	consoleLine := fmt.Sprintf("%s[%s] [%s] [%s] %s\033[0m\n",
		levelColors[level],
		entry.Time.Format("2006-01-02 15:04:05"),
		levelNames[level],
		module,
		message)
	// 非阻塞发送：stdout 缓冲满时丢弃该行，绝不阻塞业务 goroutine
	select {
	case consoleCh <- consoleLine:
	default:
	}

	if level == FATAL {
		os.Exit(1)
	}
}

func sanitize(message string) string {
	return message
}

func GetRecentLogs(count int) []LogEntry {
	logMutex.RLock()
	defer logMutex.RUnlock()

	start := 0
	if len(logBuffer) > count {
		start = len(logBuffer) - count
	}

	return logBuffer[start:]
}

// ============================================================
// 历史日志（需求：运行日志页显示以往运行记录）
// 文件布局：<exe_dir>/logs/<YYYY-MM-DD>/app.log（+ 超限备份 app_HHMMSS.log）
// ============================================================

// ReadHistoryLogs 返回 内存缓冲（本次运行）+ 历史日志文件 合并去重后的结果，
// 按时间倒序（最新在前），最多返回 count 条。
func ReadHistoryLogs(count int) []LogEntry {
	merged := make([]LogEntry, 0, count)
	seen := make(map[string]bool, count)

	// 本次运行（内存缓冲）
	for _, e := range GetRecentLogs(count) {
		key := logEntryKey(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, e)
	}

	// 历史运行（日志文件，新→旧）
	for _, e := range readHistoryFiles() {
		key := logEntryKey(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, e)
	}

	// 按时间倒序（最新在前）
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Time.After(merged[j].Time)
	})
	if len(merged) > count {
		merged = merged[:count]
	}
	return merged
}

func logEntryKey(e LogEntry) string {
	return e.Time.Format("2006-01-02 15:04:05") + "|" + e.Module + "|" + e.Message
}

// readHistoryFiles 遍历日志目录读取历史文件条目（新日期目录在前、文件倒序）。
// 为避免超大目录拖慢接口，总解析量上限 maxHistoryEntries（20000 条）。
const maxHistoryEntries = 20000

func readHistoryFiles() []LogEntry {
	dirs, err := os.ReadDir(logDir)
	if err != nil {
		return nil
	}
	// 日期目录倒序（新的在前）
	var dateDirs []os.DirEntry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if _, err := time.Parse("2006-01-02", d.Name()); err != nil {
			continue
		}
		dateDirs = append(dateDirs, d)
	}
	sort.Slice(dateDirs, func(i, j int) bool { return dateDirs[i].Name() > dateDirs[j].Name() })

	var out []LogEntry
	for _, dd := range dateDirs {
		files, err := os.ReadDir(filepath.Join(logDir, dd.Name()))
		if err != nil {
			continue
		}
		// 文件倒序（app.log 当天主文件 > app_HHMMSS.log 备份）
		sort.Slice(files, func(i, j int) bool { return files[i].Name() > files[j].Name() })
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".log") {
				continue
			}
			entries := parseLogFile(filepath.Join(logDir, dd.Name(), f.Name()))
			out = append(out, entries...)
			if len(out) >= maxHistoryEntries {
				return out
			}
		}
	}
	return out
}

// parseLogFile 解析日志文件行格式：`[2006-01-02 15:04:05] [LEVEL] [module] message`
func parseLogFile(path string) []LogEntry {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	out := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		if e, ok := parseLogLine(line); ok {
			out = append(out, e)
		}
	}
	return out
}

var logLineRe = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\] \[(DEBUG|INFO|WARN|ERROR|FATAL)\] \[([^\]]*)\] (.*)$`)

func parseLogLine(line string) (LogEntry, bool) {
	m := logLineRe.FindStringSubmatch(line)
	if m == nil {
		return LogEntry{}, false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local)
	if err != nil {
		return LogEntry{}, false
	}
	level, _ := parseLevelName(m[2])
	return LogEntry{Time: t, Level: level, Module: m[3], Message: m[4]}, true
}

func parseLevelName(name string) (LogLevel, bool) {
	for i, n := range levelNames {
		if n == name {
			return LogLevel(i), true
		}
	}
	return DEBUG, false
}

func ExportLogs(w io.Writer) error {
	logMutex.RLock()
	defer logMutex.RUnlock()

	for _, entry := range logBuffer {
		line := fmt.Sprintf("[%s] [%s] [%s] %s\n",
			entry.Time.Format("2006-01-02 15:04:05"),
			levelNames[entry.Level],
			entry.Module,
			entry.Message)
		if _, err := w.Write([]byte(line)); err != nil {
			return err
		}
	}

	return nil
}

func ClearLogs() {
	logMutex.Lock()
	defer logMutex.Unlock()
	logBuffer = []LogEntry{}
}
