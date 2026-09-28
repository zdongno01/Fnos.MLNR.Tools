package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"mlnr/logger"
)

// ============================================================
// 事件引擎（M10 日志事件 + M11 监控事件）
//
// 日志事件（type=log）：
//   - 配置来源：引用全局日志事件资源（LogEventID，页签 CRUD），或触发器内联字段
//     （LogEventPath/LogEventRegex + 冷却/连续/轮转）。
//   - 增量读取：以任务启用状态作为读取起点（首次初始化跳到文件末尾，仅处理
//     之后新追加的日志行）；程序重启丢弃启动前全部日志（状态不持久化，重启
//     后首次初始化仍然跳末尾）；任务禁用期间状态被清理，重新启用重新跳末尾。
//   - 轮转（Rotate=false）：不检测 inode，仅 offset 读取，logrotate rename 后
//     旧 fd 读到 EOF 即丢失新文件事件；Rotate=true：inode+offset，旧 inode
//     读完后再重开新文件从头读（新文件刚创建通常为空，安全）。
//   - 内存半行缓冲：缓存未带换行的残缺日志，下一轮拼接完整行再匹配。
//   - 连续匹配：内存滑动窗口计数；冷却（0~99 + 秒/分钟/小时，0=不冷却）期间
//     不处理新行，冷却到达后连续计数清零重新开始；冷却=0 时连续匹配视为 1
//     （命中一次即触发）。
//   - 已抛弃：代码写死的「监控硬盘休眠」预制日志事件及其自动断电处理（不再有
//     预制日志事件）。
//
// 监控事件（type=monitor）：
//   - 按 MonitorIntervalSec（1~600）间隔执行监控脚本（预制 idle / 自定义，支持 sh / python）；
//   - 简化语义：间隔到 → 执行脚本 → 有输出即触发（原"监控持续时间"字段已删除）。
// ============================================================

// 预制资源内容
const (
	// 预制监控事件「监控硬盘AB闲置」：/proc/diskstats 采样 sda/sdb 读写扇区计数；
	// 无 IO 时计数不变 → 输出字符串恒定，持续相同即判定闲置达成。
	prebuiltMonitorIdleCode = "#!/bin/sh\nawk '$3==\"sda\"||$3==\"sdb\"{print $3,$6,$10}' /proc/diskstats\n"
)

// logTailState 日志事件尾随状态（每个 log 触发器一份，仅 loop goroutine 访问）。
type logTailState struct {
	file         *os.File
	initialized  bool      // 是否已跳末尾完成起点初始化
	halfLine     []byte    // 半行缓冲（未带换行的残缺日志）
	consecutive  int       // 连续匹配计数（内存滑动窗口）
	coolingUntil time.Time // 冷却截止时刻（0=不冷却）
	re           *regexp.Regexp
	reErr        error
}

// monitorState 监控事件状态（每个 monitor 触发器一份，仅 loop goroutine 访问）。
type monitorState struct {
	lastSample time.Time // 上次采样时刻（间隔节流）
	hadOutput  bool      // 是否有过有效输出（进入持续计时）
	lastOutput string    // 上次采样输出（严格匹配基准）
	validSince time.Time // 当前持续计时起点（输出变化/无输出/报错时重置）
	lastErr    string    // 最近一次报错（日志）
}

// resolveLogEvent 解析 log 触发器的日志事件配置（全局资源引用 / 内联字段）。
func (s *Scheduler) resolveLogEvent(tr Trigger) (LogEvent, bool) {
	if tr.LogEventID > 0 {
		for _, ev := range s.store.GetLogEvents() {
			if ev.ID == tr.LogEventID && strings.TrimSpace(ev.Regex) != "" {
				return ev, true
			}
		}
	}
	// 内联日志事件（不创建全局资源，直接存 Trigger 内）
	if strings.TrimSpace(tr.LogEventPath) != "" && strings.TrimSpace(tr.LogEventRegex) != "" {
		consec := tr.LogEventConsecutive
		if consec <= 0 {
			consec = 1
		}
		return LogEvent{
			Path:         tr.LogEventPath,
			Regex:        tr.LogEventRegex,
			Cooldown:     tr.LogEventCooldown,
			CooldownUnit: tr.LogEventCooldownUnit,
			Consecutive:  consec,
			Rotate:       tr.LogEventRotate,
		}, true
	}
	return LogEvent{}, false
}

// resolveMonitorScript 解析 monitor 触发器的监控脚本（代码 + 脚本类型 + 资源默认参数）。
// 类型来源：预制/内联 → 触发器 MonitorScriptType（空=按 shebang 识别）；引用资源 → 资源 ScriptType。
// resArgs 为资源默认参数（触发器未显式传参时使用）。
func (s *Scheduler) resolveMonitorScript(tr Trigger) (code string, typ string, resArgs string, ok bool) {
	if tr.MonitorPrebuilt == PrebuiltMonitorIdle {
		return prebuiltMonitorIdleCode, "", "", true
	}
	if tr.MonitorScriptID > 0 {
		for _, ms := range s.store.GetMonitorScripts() {
			if ms.ID == tr.MonitorScriptID {
				code, err := s.store.GetMonitorScriptCode(tr.MonitorScriptID)
				if err != nil || strings.TrimSpace(code) == "" {
					continue
				}
				return code, ms.ScriptType, ms.Args, true
			}
		}
	}
	// 内联监控脚本（不创建全局资源，直接存 Trigger 内）
	if strings.TrimSpace(tr.MonitorCode) != "" {
		return tr.MonitorCode, tr.MonitorScriptType, "", true
	}
	return "", "", "", false
}

// onLogEventFired 日志事件触发器判定（挂接 Scheduler.logEventFired，M10）。
func (s *Scheduler) onLogEventFired(sc Schedule, tr Trigger, now time.Time) bool {
	key := "log:" + triggerEventKey(tr)
	ev, ok := s.resolveLogEvent(tr)
	if !ok {
		return false
	}
	st := s.logTails[key]
	if st == nil {
		st = &logTailState{}
		if ev.Regex != "" {
			st.re, st.reErr = regexp.Compile(ev.Regex)
		}
		s.logTails[key] = st
	}
	if st.reErr != nil || st.re == nil {
		logger.Warn("schedule", "log event #%d %q: regex error: %v", sc.ID, sc.Name, st.reErr)
		return false
	}
	// 冷却期间：不处理新行；冷却到达后连续计数已清零，重新开始计数
	if now.Before(st.coolingUntil) {
		return false
	}
	lines, err := readLogLines(st, ev)
	if err != nil {
		// 文件不存在/权限不足等：本轮无事件；文件出现后自动初始化
		logger.Debug("schedule", "log event #%d %q: read %s: %v", sc.ID, sc.Name, ev.Path, err)
		return false
	}
	if len(lines) == 0 {
		return false
	}
	consec := ev.Consecutive
	if ev.Cooldown == 0 {
		consec = 1 // 不冷却：命中一次即触发
	}
	if consec < 1 {
		consec = 1
	}
	for _, ln := range lines {
		if st.re.MatchString(ln) {
			st.consecutive++
			if st.consecutive >= consec {
				st.consecutive = 0
				if ev.Cooldown > 0 {
					st.coolingUntil = now.Add(cooldownDuration(ev))
				}
				logger.Info("schedule", "log event fired task #%d %q (key %s, line %q)",
					sc.ID, sc.Name, key, truncateStr(ln, 120))
				return true
			}
		} else {
			st.consecutive = 0 // 非匹配行重置连续计数
		}
	}
	return false
}

// onMonitorEventFired 监控事件触发器判定（挂接 Scheduler.monitorEventFired，M11）。
// 简化语义：间隔到 → 执行脚本 → 有输出即触发。
func (s *Scheduler) onMonitorEventFired(sc Schedule, tr Trigger, now time.Time) bool {
	key := "monitor:" + triggerEventKey(tr)
	code, typ, resArgs, ok := s.resolveMonitorScript(tr)
	if !ok {
		return false
	}
	interval := time.Duration(tr.MonitorIntervalSec) * time.Second
	if tr.MonitorIntervalSec <= 0 {
		interval = 30 * time.Second
	}
	st := s.monitorStates[key]
	if st == nil {
		st = &monitorState{}
		s.monitorStates[key] = st
	}
	if now.Sub(st.lastSample) < interval {
		return false // 间隔未到
	}
	st.lastSample = now
	// 参数：触发器显式传入值优先；未传时回退脚本资源的默认参数
	args := tr.MonitorScriptArgs
	if strings.TrimSpace(args) == "" {
		args = resArgs
	}
	out, err := s.runScript(code, typ, args, 30*time.Second)
	if err != nil {
		st.lastErr = err.Error()
		logger.Warn("schedule", "monitor event #%d %q: %v", sc.ID, sc.Name, err)
		return false
	}
	if strings.TrimSpace(out) == "" {
		st.lastErr = "脚本无输出，监控事件未达成"
		logger.Warn("schedule", "monitor event #%d %q: script no output", sc.ID, sc.Name)
		return false
	}
	st.lastErr = ""
	logger.Info("schedule", "monitor event fired task #%d %q (key %s)", sc.ID, sc.Name, key)
	return true
}

// readLogLines 增量读取日志文件新行（含半行缓冲与轮转处理）。
// 返回本轮新增的完整行；首次初始化（跳末尾）不产生行。
func readLogLines(st *logTailState, ev LogEvent) ([]string, error) {
	var lines []string
	for {
		if st.file == nil {
			f, err := os.Open(ev.Path)
			if err != nil {
				return lines, err
			}
			st.file = f
			if !st.initialized {
				// 起点=文件末尾：丢弃启用/重启前的全部历史日志
				if _, err := f.Seek(0, io.SeekEnd); err != nil {
					f.Close()
					st.file = nil
					return lines, err
				}
				st.initialized = true
				return lines, nil // 本轮不处理历史
			}
			if ev.Rotate {
				// 轮转后重开新文件：从头读（新文件刚创建通常为空）
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					f.Close()
					st.file = nil
					return lines, err
				}
			} else {
				// 非轮转重开（文件被删重建等）：丢事件，跳末尾
				if _, err := f.Seek(0, io.SeekEnd); err != nil {
					f.Close()
					st.file = nil
					return lines, err
				}
				return lines, nil
			}
		}
		buf := make([]byte, 32*1024)
		n, err := st.file.Read(buf)
		if n > 0 {
			lines = append(lines, splitLogLines(st, buf[:n])...)
		}
		if err == io.EOF {
			if ev.Rotate {
				// 轮转检测：当前 fd 与路径已非同一文件 → 旧 inode 已读完，重开新文件
				cur, cerr := st.file.Stat()
				pi, perr := os.Stat(ev.Path)
				if cerr == nil && perr == nil && !os.SameFile(cur, pi) {
					st.file.Close()
					st.file = nil
					continue
				}
			}
			break
		}
		if err != nil {
			break
		}
	}
	return lines, nil
}

// splitLogLines 按换行切分缓冲，残缺尾部拼入半行缓冲。
func splitLogLines(st *logTailState, buf []byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			line := append(append([]byte(nil), st.halfLine...), buf[start:i]...)
			st.halfLine = nil
			out = append(out, string(line))
			start = i + 1
		}
	}
	st.halfLine = append(st.halfLine, buf[start:]...)
	return out
}

// cooldownDuration 冷却时间 → duration（second/minute/hour；0=不冷却）。
func cooldownDuration(ev LogEvent) time.Duration {
	if ev.Cooldown <= 0 {
		return 0
	}
	var unit time.Duration
	switch ev.CooldownUnit {
	case CooldownUnitMinute:
		unit = time.Minute
	case CooldownUnitHour:
		unit = time.Hour
	default:
		unit = time.Second
	}
	return time.Duration(ev.Cooldown) * unit
}

// splitShellArgs 按 shell 引号语义切分参数字符串。
// 支持双引号（允许内含空格和转义）、单引号（纯字面量，不解析转义）、反斜杠转义。
// 用于替代 strings.Fields，保证用户输入 "1 \"P02416106993\"" 时能正确得到两个参数。
func splitShellArgs(s string) []string {
	var result []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			current.WriteByte(c)
			escaped = false
			continue
		}

		if c == '\\' && !inSingle {
			escaped = true
			continue
		}

		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}

		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if !inSingle && !inDouble && (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteByte(c)
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// runScriptCode 运行脚本（sh / python，超时默认 30s），返回 stdout（保留原样含换行）。
// typ 指定语言："shell"→sh、"python"→python3；空串按 shebang 自动识别（存量兼容）：
// 首个非空行是 shebang 且含 "python"（如 #!/usr/bin/env python3）→ python3，其余按 sh。
// 位置参数：sh 用 $1 $2 …（sh -c code sh args…），python 用 sys.argv[1:]（python3 -c code python args…）。
func runScriptCode(code string, typ string, args string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	argsList := []string{}
	if strings.TrimSpace(args) != "" {
		argsList = splitShellArgs(args)
	}
	usePython := false
	switch typ {
	case "python":
		usePython = true
	case "shell":
		usePython = false
	default:
		usePython = isPythonScript(code)
	}
	var cmd *exec.Cmd
	if usePython {
		// -c 后首个参数作为 sys.argv[0] 占位（python 脚本内用 sys.argv[1:] 引用传入值）
		parts := append([]string{"-c", code, "python"}, argsList...)
		cmd = exec.CommandContext(ctx, "python3", parts...)
	} else {
		// sh -c 的位置参数方式传 args：code 内可用 $1 $2 … 引用
		parts := append([]string{"-c", code, "sh"}, argsList...)
		cmd = exec.CommandContext(ctx, "sh", parts...)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("脚本执行超时（%v）", timeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("脚本执行失败：%v（输出：%s）", err, truncateStr(strings.TrimSpace(string(out)), 200))
	}
	return string(out), nil
}

// isPythonScript 判定脚本是否为 python 脚本：首个非空行为 #! shebang 且含 "python"。
// 无 shebang 或首行为其它内容一律按 sh 处理（不猜测）。
func isPythonScript(code string) bool {
	for _, line := range strings.Split(code, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#!") {
			return strings.Contains(strings.ToLower(line), "python")
		}
		return false
	}
	return false
}

// testLogEvent 日志事件测试（M13）：读取日志路径 + 正则匹配，返回全部匹配行。
// 仅校验，不修改任何监控游标状态。
func testLogEvent(path, regex string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("日志路径为空")
	}
	if strings.TrimSpace(regex) == "" {
		return nil, fmt.Errorf("日志监控内容为空")
	}
	re, err := regexp.Compile(regex)
	if err != nil {
		return nil, fmt.Errorf("日志表达式错误：%v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("日志文件不存在：%s", path)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("日志文件权限不足：%s", path)
		}
		return nil, fmt.Errorf("读取日志失败：%v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var hits []string
	for scanner.Scan() {
		if re.MatchString(scanner.Text()) {
			hits = append(hits, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取日志失败：%v", err)
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("日志监控内容没有匹配项")
	}
	return hits, nil
}

// truncateStr 截断字符串用于日志（避免刷屏）。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
