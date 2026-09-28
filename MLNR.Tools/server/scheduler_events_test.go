package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ===== M10 日志事件引擎 =====

// newEventScheduler 事件引擎测试调度器（假 store + 假脚本执行器）。
func newEventScheduler(t *testing.T, scheds []Schedule) (*Scheduler, *Store, *fakeScriptRunner) {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := store.Load(); err != nil {
		t.Fatalf("store load: %v", err)
	}
	InitScheduleLogStore(t.TempDir())
	store.SaveSchedules(scheds)
	s := NewScheduler(store, &fakeScheduleSession{online: true}, &fakeDiskExecutor{fail: map[string]bool{}})
	runner := &fakeScriptRunner{}
	s.runScript = runner.run
	return s, store, runner
}

// fakeScriptRunner 假脚本执行器：按脚本内容映射输出，并记录最近一次类型与参数。
type fakeScriptRunner struct {
	outputs  map[string]string // 脚本 code → 输出
	errs     map[string]error  // 脚本 code → 错误
	calls    int
	lastTyp  string // 最近一次传入的脚本类型（"shell"/"python"/""）
	lastArgs string // 最近一次传入的命令行参数
}

func (f *fakeScriptRunner) run(code string, typ string, args string, timeout time.Duration) (string, error) {
	f.calls++
	f.lastTyp = typ
	f.lastArgs = args
	if f.errs != nil {
		if err, ok := f.errs[code]; ok {
			return "", err
		}
	}
	if f.outputs != nil {
		if out, ok := f.outputs[code]; ok {
			return out, nil
		}
	}
	return "", nil
}

func nextMonitorScriptID(list []MonitorScript) int {
	maxID := 0
	for _, e := range list {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	return maxID + 1
}

func nextExecScriptID(list []ExecScript) int {
	maxID := 0
	for _, e := range list {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	return maxID + 1
}

func saveLogEvent(t *testing.T, store *Store, ev LogEvent) int {
	t.Helper()
	return store.AddLogEvent(ev)
}

// closeLogTails 关闭全部日志尾随 fd（测试清理辅助；Windows 文件锁规避）。
func closeLogTails(s *Scheduler) {
	for _, st := range s.logTails {
		if st.file != nil {
			st.file.Close()
			st.file = nil
		}
	}
}

func writeLogFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}
}

func appendLogFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("append log file: %v", err)
	}
	defer f.Close()
	for _, ln := range lines {
		if _, err := f.WriteString(ln + "\n"); err != nil {
			t.Fatalf("append line: %v", err)
		}
	}
}

func TestLogEventInline_IncrementalRead_StartsAtEnd(t *testing.T) {
	// 增量读取：初始化跳末尾，历史行不处理，仅新追加行触发
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD no-match", "OLD sleep-line")

	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, _, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "sleep-line", LogEventConsecutive: 1}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	if s.onLogEventFired(scheds[0], tr, now) {
		t.Fatal("初始化跳末尾：历史行不应触发")
	}
	appendLogFile(t, path, "NEW sleep-line")
	if !s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("新追加匹配行应触发")
	}
	// 已消费：无新行不再触发
	if s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("无新行不应重复触发")
	}
}

func TestLogEventInline_HalfLineBuffer(t *testing.T) {
	// 半行缓冲：残缺行下一轮补全后再匹配
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, _, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "SLEEP-OK", LogEventConsecutive: 1}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now) // 初始化

	// 追加半行（无换行）
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("SLEEP-")
	f.Close()
	if s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("半行不应触发")
	}
	// 补全
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("OK\n")
	f.Close()
	if !s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("补全的完整行应触发")
	}
}

func TestLogEventInline_ConsecutiveMatch(t *testing.T) {
	// 连续匹配 2 次：第一次命中不触发，第二次命中触发；非匹配行重置计数
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, _, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	// 冷却>0 才能启用连续匹配（冷却=0 时强制连续 1 次）
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "HIT", LogEventConsecutive: 2, LogEventCooldown: 1, LogEventCooldownUnit: CooldownUnitMinute}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now)

	appendLogFile(t, path, "HIT-1")
	if s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("连续 1 次不应触发")
	}
	appendLogFile(t, path, "HIT-2")
	if !s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("连续 2 次应触发")
	}
}

func TestLogEventInline_Cooldown(t *testing.T) {
	// 冷却 1 分钟：触发后冷却期内不触发；冷却到达后计数清零可再触发
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, _, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "HIT", LogEventConsecutive: 1, LogEventCooldown: 1, LogEventCooldownUnit: CooldownUnitMinute}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now)

	appendLogFile(t, path, "HIT-1")
	if !s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("首次命中应触发")
	}
	// 冷却期内（30s < 1min）
	appendLogFile(t, path, "HIT-2")
	if s.onLogEventFired(scheds[0], tr, now.Add(40*time.Second)) {
		t.Fatal("冷却期内不应触发")
	}
	// 冷却到达后（> 1min）：连续计数已清零，再次命中触发
	appendLogFile(t, path, "HIT-3")
	if !s.onLogEventFired(scheds[0], tr, now.Add(70*time.Second)) {
		t.Fatal("冷却到达后应可再触发")
	}
}

func TestLogEventInline_Rotate(t *testing.T) {
	// 轮转：旧 inode 读完（含补全行）→ 重开新文件从头读
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, _, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "HIT", LogEventConsecutive: 1, LogEventRotate: true}
	defer closeLogTails(s)
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now)

	appendLogFile(t, path, "HIT-OLD-FILE")
	if !s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("轮转前旧文件新行应触发")
	}

	// logrotate 模拟：rename 旧文件 → 新建文件写入新行
	closeLogTails(s) // Windows 文件锁：先释放旧 fd（生产 Linux 无需）
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeLogFile(t, path, "HIT-NEW-FILE")
	if !s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("轮转后新文件应从头读取并触发")
	}
}

func TestLogEventInline_RegexError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, _, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "([", LogEventConsecutive: 1}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	appendLogFile(t, path, "any")
	if s.onLogEventFired(scheds[0], tr, now) {
		t.Fatal("非法正则不应触发")
	}
}

func TestLogEventInline_DisabledTaskStatePruned(t *testing.T) {
	// 任务停用后状态清理：重新启用重新跳末尾（停用期间日志被忽略）
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	s, _, _ := newEventScheduler(t, nil)
	defer closeLogTails(s)
	tr := Trigger{Type: TriggerTypeLog, LogEventPath: path, LogEventRegex: "HIT", LogEventConsecutive: 1}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	sc := Schedule{ID: 1, Name: "日志任务", Enabled: true}
	s.onLogEventFired(sc, tr, now) // 初始化（跳末尾）
	appendLogFile(t, path, "HIT-A")
	if !s.onLogEventFired(sc, tr, now.Add(10*time.Second)) {
		t.Fatal("启用期间应触发")
	}
	// 任务禁用：清理状态
	sc.Enabled = false
	s.pruneEventStates([]Schedule{sc})
	if len(s.logTails) != 0 {
		t.Fatal("停用任务状态应被清理")
	}
	// 停用期间新增行
	appendLogFile(t, path, "HIT-DISABLED")
	// 重新启用：重新跳末尾（忽略停用期间日志）
	sc.Enabled = true
	if s.onLogEventFired(sc, tr, now.Add(20*time.Second)) {
		t.Fatal("重新启用应跳末尾，停用期间日志不触发")
	}
	appendLogFile(t, path, "HIT-AGAIN")
	if !s.onLogEventFired(sc, tr, now.Add(30*time.Second)) {
		t.Fatal("重新启用后新行应触发")
	}
}

func TestLogEvent_IncrementalRead_StartsAtEnd(t *testing.T) {
	// 资源引用：增量读取，初始化跳末尾，历史行不处理，仅新追加行触发
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD no-match", "OLD sleep-line")

	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, store, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "sleep-line", Consecutive: 1})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	if s.onLogEventFired(scheds[0], tr, now) {
		t.Fatal("初始化跳末尾：历史行不应触发")
	}
	appendLogFile(t, path, "NEW sleep-line")
	if !s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("新追加匹配行应触发")
	}
	// 已消费：无新行不再触发
	if s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("无新行不应重复触发")
	}
}

func TestLogEvent_HalfLineBuffer(t *testing.T) {
	// 资源引用：半行缓冲，残缺行下一轮补全后再匹配
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, store, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "SLEEP-OK", Consecutive: 1})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now) // 初始化

	// 追加半行（无换行）
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("SLEEP-")
	f.Close()
	if s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("半行不应触发")
	}
	// 补全
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("OK\n")
	f.Close()
	if !s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("补全的完整行应触发")
	}
}

func TestLogEvent_ConsecutiveMatch(t *testing.T) {
	// 资源引用：连续匹配 2 次，第一次命中不触发，第二次命中触发
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, store, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	// 冷却>0 才能启用连续匹配（冷却=0 时强制连续 1 次）
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "HIT", Consecutive: 2, Cooldown: 1, CooldownUnit: CooldownUnitMinute})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now)

	appendLogFile(t, path, "HIT-1")
	if s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("连续 1 次不应触发")
	}
	appendLogFile(t, path, "HIT-2")
	if !s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("连续 2 次应触发")
	}
}

func TestLogEvent_Cooldown(t *testing.T) {
	// 资源引用：冷却 1 分钟，触发后冷却期内不触发；冷却到达后计数清零可再触发
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, store, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "HIT", Consecutive: 1, Cooldown: 1, CooldownUnit: CooldownUnitMinute})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now)

	appendLogFile(t, path, "HIT-1")
	if !s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("首次命中应触发")
	}
	// 冷却期内（30s < 1min）
	appendLogFile(t, path, "HIT-2")
	if s.onLogEventFired(scheds[0], tr, now.Add(40*time.Second)) {
		t.Fatal("冷却期内不应触发")
	}
	// 冷却到达后（> 1min）：连续计数已清零，再次命中触发
	appendLogFile(t, path, "HIT-3")
	if !s.onLogEventFired(scheds[0], tr, now.Add(70*time.Second)) {
		t.Fatal("冷却到达后应可再触发")
	}
}

func TestLogEvent_Rotate(t *testing.T) {
	// 资源引用：轮转，旧 inode 读完（含补全行）→ 重开新文件从头读
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, store, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "HIT", Consecutive: 1, Rotate: true})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	s.onLogEventFired(scheds[0], tr, now)

	appendLogFile(t, path, "HIT-OLD-FILE")
	if !s.onLogEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("轮转前旧文件新行应触发")
	}

	// logrotate 模拟：rename 旧文件 → 新建文件写入新行
	closeLogTails(s) // Windows 文件锁：先释放旧 fd（生产 Linux 无需）
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeLogFile(t, path, "HIT-NEW-FILE")
	if !s.onLogEventFired(scheds[0], tr, now.Add(20*time.Second)) {
		t.Fatal("轮转后新文件应从头读取并触发")
	}
}

func TestLogEvent_RegexError(t *testing.T) {
	// 资源引用：非法正则不触发
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	scheds := []Schedule{{ID: 1, Name: "日志任务", Enabled: true}}
	s, store, _ := newEventScheduler(t, scheds)
	defer closeLogTails(s)
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "([", Consecutive: 1})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	appendLogFile(t, path, "any")
	if s.onLogEventFired(scheds[0], tr, now) {
		t.Fatal("非法正则不应触发")
	}
}

func TestLogEvent_DisabledTaskStatePruned(t *testing.T) {
	// 资源引用：任务停用后状态清理，重新启用重新跳末尾（停用期间日志被忽略）
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	writeLogFile(t, path, "OLD")
	s, store, _ := newEventScheduler(t, nil)
	defer closeLogTails(s)
	evID := saveLogEvent(t, store, LogEvent{Name: "ev", Path: path, Regex: "HIT", Consecutive: 1})
	tr := Trigger{Type: TriggerTypeLog, LogEventID: evID}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	sc := Schedule{ID: 1, Name: "日志任务", Enabled: true}
	s.onLogEventFired(sc, tr, now) // 初始化（跳末尾）
	appendLogFile(t, path, "HIT-A")
	if !s.onLogEventFired(sc, tr, now.Add(10*time.Second)) {
		t.Fatal("启用期间应触发")
	}
	// 任务禁用：清理状态
	sc.Enabled = false
	s.pruneEventStates([]Schedule{sc})
	if len(s.logTails) != 0 {
		t.Fatal("停用任务状态应被清理")
	}
	// 停用期间新增行
	appendLogFile(t, path, "HIT-DISABLED")
	// 重新启用：重新跳末尾（忽略停用期间日志）
	sc.Enabled = true
	if s.onLogEventFired(sc, tr, now.Add(20*time.Second)) {
		t.Fatal("重新启用应跳末尾，停用期间日志不触发")
	}
	appendLogFile(t, path, "HIT-AGAIN")
	if !s.onLogEventFired(sc, tr, now.Add(30*time.Second)) {
		t.Fatal("重新启用后新行应触发")
	}
}


func TestCooldownDuration(t *testing.T) {
	cases := []struct {
		ev   LogEvent
		want time.Duration
	}{
		{LogEvent{Cooldown: 0}, 0},
		{LogEvent{Cooldown: 5}, 5 * time.Second},
		{LogEvent{Cooldown: 2, CooldownUnit: CooldownUnitMinute}, 2 * time.Minute},
		{LogEvent{Cooldown: 3, CooldownUnit: CooldownUnitHour}, 3 * time.Hour},
	}
	for i, tc := range cases {
		if got := cooldownDuration(tc.ev); got != tc.want {
			t.Errorf("case %d: cooldownDuration=%v want %v", i, got, tc.want)
		}
	}
}

// ===== M11 监控事件引擎 =====

func saveMonitorScript(t *testing.T, store *Store, ms MonitorScript) int {
	t.Helper()
	return store.AddMonitorScript(ms, ms.Code)
}

func TestMonitorEvent_FiresOnOutput(t *testing.T) {
	// 简化语义：间隔到 → 执行脚本 → 有输出即触发（监控持续时间字段已删除）
	code := "echo stable"
	scheds := []Schedule{{ID: 1, Name: "监控任务", Enabled: true}}
	s, store, runner := newEventScheduler(t, scheds)
	msID := saveMonitorScript(t, store, MonitorScript{Name: "ms", Code: code})
	runner.outputs = map[string]string{code: "IDLE\n"}
	tr := Trigger{Type: TriggerTypeMonitor, MonitorScriptID: msID, MonitorIntervalSec: 10}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	// 首采样有输出 → 触发
	if !s.onMonitorEventFired(scheds[0], tr, now) {
		t.Fatal("间隔到且脚本有输出应触发")
	}
	// 间隔未到：不采样、不触发
	runner.outputs[code] = "CHANGED\n"
	if s.onMonitorEventFired(scheds[0], tr, now.Add(5*time.Second)) {
		t.Fatal("间隔未到不应采样触发")
	}
	// 间隔到达后再次采样触发
	runner.outputs[code] = "IDLE\n"
	if !s.onMonitorEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("间隔到达后应再次触发")
	}
}

func TestMonitorEvent_NoOutputDoesNotFire(t *testing.T) {
	code := "echo maybe-empty"
	scheds := []Schedule{{ID: 1, Name: "监控任务", Enabled: true}}
	s, store, runner := newEventScheduler(t, scheds)
	msID := saveMonitorScript(t, store, MonitorScript{Name: "ms", Code: code})
	tr := Trigger{Type: TriggerTypeMonitor, MonitorScriptID: msID, MonitorIntervalSec: 10}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	// 无输出 → 不触发
	runner.outputs = map[string]string{code: ""}
	if s.onMonitorEventFired(scheds[0], tr, now) {
		t.Fatal("脚本无输出不应触发")
	}
	// 恢复输出：间隔到达后即触发
	runner.outputs[code] = "X\n"
	if !s.onMonitorEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("恢复输出后间隔到达应触发")
	}
}

func TestMonitorEvent_ScriptErrorDoesNotFire(t *testing.T) {
	code := "false"
	scheds := []Schedule{{ID: 1, Name: "监控任务", Enabled: true}}
	s, store, runner := newEventScheduler(t, scheds)
	msID := saveMonitorScript(t, store, MonitorScript{Name: "ms", Code: code})
	tr := Trigger{Type: TriggerTypeMonitor, MonitorScriptID: msID, MonitorIntervalSec: 10}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)

	// 脚本报错 → 不触发
	runner.errs = map[string]error{code: fmt.Errorf("boom")}
	if s.onMonitorEventFired(scheds[0], tr, now) {
		t.Fatal("脚本报错不应触发")
	}
	// 报错恢复：间隔到达后触发
	runner.errs = nil
	runner.outputs = map[string]string{code: "OK\n"}
	if !s.onMonitorEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("报错恢复后间隔到达应触发")
	}
}

func TestMonitorEvent_PrebuiltIdleScript(t *testing.T) {
	// 预制 idle：使用预制脚本执行；有输出即触发
	scheds := []Schedule{{ID: 1, Name: "闲置监控", Enabled: true}}
	s, _, runner := newEventScheduler(t, scheds)
	code := prebuiltMonitorIdleCode
	runner.outputs = map[string]string{code: "sda 100 200\nsdb 50 60\n"}
	tr := Trigger{Type: TriggerTypeMonitor, MonitorPrebuilt: PrebuiltMonitorIdle, MonitorIntervalSec: 5}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	if !s.onMonitorEventFired(scheds[0], tr, now) {
		t.Fatal("预制脚本有输出应触发")
	}
	// 间隔未到不重复触发
	if s.onMonitorEventFired(scheds[0], tr, now.Add(2*time.Second)) {
		t.Fatal("间隔未到不应重复触发")
	}
}

func TestMonitorEvent_InlineCodeWithType(t *testing.T) {
	// 内联自定义代码（不创建资源）：脚本类型随触发器保存，执行时按类型传递
	code := "print('hello')"
	scheds := []Schedule{{ID: 1, Name: "监控任务", Enabled: true}}
	s, _, runner := newEventScheduler(t, scheds)
	runner.outputs = map[string]string{code: "hello\n"}
	tr := Trigger{Type: TriggerTypeMonitor, MonitorCode: code, MonitorScriptType: "python", MonitorScriptArgs: "-f ssd", MonitorIntervalSec: 10}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	if !s.onMonitorEventFired(scheds[0], tr, now) {
		t.Fatal("内联代码有输出应触发")
	}
	if runner.lastTyp != "python" {
		t.Fatalf("脚本类型应传递为 python，got %q", runner.lastTyp)
	}
	if runner.lastArgs != "-f ssd" {
		t.Fatalf("触发器参数应透传，got %q", runner.lastArgs)
	}
}

func TestMonitorEvent_ResourceTypeAndArgsFallback(t *testing.T) {
	// 引用资源：按资源保存的脚本类型执行；触发器未传参时回退资源默认参数
	code := "#!/usr/bin/env python3\nprint('x')"
	scheds := []Schedule{{ID: 1, Name: "监控任务", Enabled: true}}
	s, store, runner := newEventScheduler(t, scheds)
	msID := saveMonitorScript(t, store, MonitorScript{Name: "ms", Code: code, ScriptType: "python", Args: "-f ssd"})
	runner.outputs = map[string]string{code: "x\n"}
	tr := Trigger{Type: TriggerTypeMonitor, MonitorScriptID: msID, MonitorIntervalSec: 10}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.Local)
	if !s.onMonitorEventFired(scheds[0], tr, now) {
		t.Fatal("资源脚本有输出应触发")
	}
	if runner.lastTyp != "python" {
		t.Fatalf("应使用资源脚本类型 python，got %q", runner.lastTyp)
	}
	if runner.lastArgs != "-f ssd" {
		t.Fatalf("未显式传参时应回退资源默认参数，got %q", runner.lastArgs)
	}
	// 触发器显式传参：优先使用触发器参数
	tr.MonitorScriptArgs = "-f nvme"
	if !s.onMonitorEventFired(scheds[0], tr, now.Add(10*time.Second)) {
		t.Fatal("显式传参后应触发")
	}
	if runner.lastArgs != "-f nvme" {
		t.Fatalf("显式参数应优先，got %q", runner.lastArgs)
	}
}
