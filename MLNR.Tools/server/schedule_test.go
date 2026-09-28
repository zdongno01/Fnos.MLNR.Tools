package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ===== normalizeSchedule 校验与归一化 =====

func mustTime(t *testing.T, s string) *time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &v
}

func TestNormalizeScheduleFanDaily(t *testing.T) {
	sc := Schedule{
		Category:     ScheduleCategoryFan,
		Name:         " 晚间静音 ",
		Description:  "desc",
		Enabled:      true,
		ScheduleType: ScheduleTypeDaily,
		FanChannels:  []int{1, 2}, // 多选
		FanMode:      FanCurveQuiet,
		Time:         "22:30",
		// 不应适用字段（应被清空）
		RunAt:      mustTime(t, "2026-09-18 10:00"),
		Weekdays:   []int{1},
		MonthDays:  []int{15},
		DiskGroups: []int{1},
		DiskAction: DiskActionOnline,
	}
	if msg := normalizeSchedule(&sc); msg != "" {
		t.Fatalf("expected pass, got: %s", msg)
	}
	if sc.Name != "晚间静音" {
		t.Errorf("name should be trimmed, got %q", sc.Name)
	}
	if sc.RunAt != nil || len(sc.Weekdays) != 0 || len(sc.MonthDays) != 0 {
		t.Errorf("daily task should clear one_time/weekly/monthly fields")
	}
	if len(sc.DiskGroups) != 0 || sc.DiskAction != "" {
		t.Errorf("fan task should clear disk fields")
	}
}

func TestNormalizeScheduleDiscardsLegacyTriggerKind(t *testing.T) {
	// 旧 sleep/idle 触发任务配置（scheduleType=trigger + triggerKind）直接抛弃：
	// 不再构造新格式触发器（Triggers 保持空），仅顶层磁盘字段迁移为执行器。
	sc := Schedule{
		Name:         "休眠自动下线",
		ScheduleType: ScheduleTypeTrigger,
		DiskGroups:   []int{2},
		DiskAction:   DiskActionOffline,
		TriggerRule: &TriggerRule{
			AllDay:       true,
			ThresholdMin: 30,
		},
	}
	if msg := normalizeSchedule(&sc); msg != "" {
		t.Fatalf("expected pass, got: %s", msg)
	}
	if len(sc.Triggers) != 0 {
		t.Errorf("legacy trigger config should be discarded (no triggers), got %+v", sc.Triggers)
	}
	if len(sc.Executors) != 1 || sc.Executors[0].Type != ExecutorDiskGroupControl ||
		sc.Executors[0].DiskGroupID != 2 || sc.Executors[0].DiskAction != DiskActionOffline {
		t.Errorf("top-level disk fields must migrate to disk_group_control executor, got %+v", sc.Executors)
	}

	// 旧格式 Triggers 里的 sleep/idle 触发器：校验拒绝（触发器类型无效）
	bad := Schedule{Name: "旧触发器", Triggers: []Trigger{{Type: "sleep", DiskGroups: []int{1}}},
		Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}
	if msg := normalizeSchedule(&bad); msg == "" || !strings.Contains(msg, "触发器类型无效") {
		t.Fatalf("legacy sleep trigger should be rejected, got %q", msg)
	}

	// 空触发器清单合法（仅手动触发）
	sc3 := Schedule{Name: "手动节点", Executors: []Executor{{Type: ExecutorControlTask, TaskID: 1, TaskAction: TaskActionRun}}}
	if msg := normalizeSchedule(&sc3); msg != "" {
		t.Fatalf("empty triggers must be allowed, got: %s", msg)
	}
	if len(sc3.Triggers) != 0 {
		t.Errorf("triggers should stay empty, got %+v", sc3.Triggers)
	}
}

func TestNormalizeScheduleExecutors(t *testing.T) {
	// M9 执行器校验：四类执行器参数校验 + 无关字段清空 + 旧动作迁移
	cases := []struct {
		name string
		sc   Schedule
		want string // 期望错误消息片段（""=通过）
	}{
		{"no executor at all", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00"}, "至少需要一个执行器"},
		{"fan control ok", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, ""},
		{"fan control bad channel", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorFanControl, FanID: 9, FanManual: false, FanCurve: FanCurveDaily}}}, "风扇"},
		{"fan control bad curve", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: "turbo"}}}, "风扇曲线"},
		{"fan manual percent ok", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: true, FanPercent: 60}}}, ""},
		{"fan manual bad percent", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: true, FanPercent: 150}}}, "转速"},
		{"disk control ok", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 1, DiskAction: DiskActionOffline}}}, ""},
		{"disk online clears force", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 1, DiskAction: DiskActionOnline, ForceOff: true, KillOccupied: true}}}, ""},
		{"disk no group", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskAction: DiskActionOffline}}}, "硬盘组"},
		{"disk bad action", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 1, DiskAction: "reboot"}}}, "硬盘组控制动作"},
		{"disk retry defaults", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 1, DiskAction: DiskActionOffline,
				RetryEnabled: true, MaxRetries: 99, RetryIntervalSec: 99999}}}, ""},
		{"control task ok", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorControlTask, TaskID: 5, TaskAction: TaskActionRun, DelaySec: 60, RespectEnabled: true}}}, ""},
		{"control task no target", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorControlTask, TaskAction: TaskActionRun}}}, "目标任务"},
		{"control task bad action", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorControlTask, TaskID: 5, TaskAction: "reboot"}}}, "控制任务操作"},
		{"delay over limit", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorControlTask, TaskID: 5, TaskAction: TaskActionRun, DelaySec: 86401}}}, "延迟范围"},
		{"exec script ok inline", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorExecScript, ScriptCode: "echo hi"}}}, ""},
		{"exec script no script", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: ExecutorExecScript}}}, "执行脚本"},
		{"unknown executor", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
			Executors: []Executor{{Type: "reboot"}}}, "未知执行器类型"},
	}
	for _, tc := range cases {
		msg := normalizeSchedule(&tc.sc)
		if tc.want == "" && msg != "" {
			t.Errorf("%s: expected pass, got: %s", tc.name, msg)
			continue
		}
		if tc.want != "" && (msg == "" || !strings.Contains(msg, tc.want)) {
			t.Errorf("%s: want error containing %q, got %q", tc.name, tc.want, msg)
		}
	}
	// 无关字段清空：control_task 执行器的风扇字段被清空
	sc := Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
		Executors: []Executor{{Type: ExecutorControlTask, TaskID: 5, TaskAction: TaskActionRun, FanID: 1, FanCurve: FanCurveDaily}}}
	if msg := normalizeSchedule(&sc); msg != "" {
		t.Fatalf("expected pass, got: %s", msg)
	}
	if sc.Executors[0].FanID != 0 || sc.Executors[0].FanCurve != "" {
		t.Errorf("control_task executor should clear fan fields, got %+v", sc.Executors[0])
	}
	// 旧动作迁移：Actions → Executors，FailureActions 首个 run_task 挂到首个执行器
	sc2 := Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00",
		Actions:        []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}},
		FailureActions: []ScheduleAction{{Type: ActionRunTask, TaskID: 9}}}
	if msg := normalizeSchedule(&sc2); msg != "" {
		t.Fatalf("expected pass, got: %s", msg)
	}
	if len(sc2.Executors) != 1 || sc2.Executors[0].Type != ExecutorDiskGroupControl ||
		sc2.Executors[0].FailureTaskID != 9 {
		t.Errorf("actions must migrate to executor with failure task, got %+v", sc2.Executors)
	}
}

func TestCheckScheduleCycles(t *testing.T) {
	mk := func(id int, targets ...int) Schedule {
		exs := make([]Executor, 0, len(targets))
		for _, t := range targets {
			exs = append(exs, Executor{Type: ExecutorControlTask, TaskID: t, TaskAction: TaskActionRun})
		}
		return Schedule{ID: id, Name: "t", Executors: exs}
	}
	// 无环
	if msg := checkScheduleCycles([]Schedule{mk(1, 2), mk(2)}); msg != "" {
		t.Errorf("no cycle expected, got: %s", msg)
	}
	// 链 1→2→3
	if msg := checkScheduleCycles([]Schedule{mk(1, 2), mk(2, 3), mk(3)}); msg != "" {
		t.Errorf("linear chain no cycle, got: %s", msg)
	}
	// 自环 1→1
	if msg := checkScheduleCycles([]Schedule{mk(1, 1)}); msg == "" {
		t.Error("self cycle should be detected")
	}
	// 双环 1→2→1
	if msg := checkScheduleCycles([]Schedule{mk(1, 2), mk(2, 1)}); msg == "" {
		t.Error("2-cycle should be detected")
	}
	// 三角环 1→2→3→1
	if msg := checkScheduleCycles([]Schedule{mk(1, 2), mk(2, 3), mk(3, 1)}); msg == "" {
		t.Error("3-cycle should be detected")
	}
	// 环外的链不受影响：1→2→3→1 与 4→3（4 不进环）
	if msg := checkScheduleCycles([]Schedule{mk(1, 2), mk(2, 3), mk(3, 1), mk(4, 3)}); msg == "" {
		t.Error("cycle with extra edge should be detected")
	}
	// 执行器的成功/失败后执行任务也参与环检测
	a := Schedule{ID: 1, Name: "t", Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily, SuccessTaskID: 2}}}
	b := Schedule{ID: 2, Name: "t", Executors: []Executor{{Type: ExecutorControlTask, TaskID: 1, TaskAction: TaskActionRun}}}
	if msg := checkScheduleCycles([]Schedule{a, b}); msg == "" {
		t.Error("cycle through success/failure task should be detected")
	}
	// FailureTaskID 也参与
	a2 := Schedule{ID: 1, Name: "t", Executors: []Executor{{Type: ExecutorExecScript, ScriptCode: "echo", FailureTaskID: 2}}}
	if msg := checkScheduleCycles([]Schedule{a2, b}); msg == "" {
		t.Error("cycle through failure task should be detected")
	}
	// 目标不存在不误报环（存在性由 validateScheduleTargets 处理）
	if msg := checkScheduleCycles([]Schedule{mk(1, 99)}); msg != "" {
		t.Errorf("missing target is not a cycle, got: %s", msg)
	}
}

func TestNormalizeScheduleMonitorInlineOK(t *testing.T) {
	// 内联监控脚本代码（不创建资源）：允许保存；脚本类型规范化并保留
	sc := Schedule{
		Name:      "x",
		Triggers:  []Trigger{{Type: TriggerTypeMonitor, MonitorCode: "print(1)", MonitorScriptType: "python", MonitorIntervalSec: 30}},
		Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}},
	}
	if msg := normalizeSchedule(&sc); msg != "" {
		t.Fatalf("内联监控脚本应可保存，got %q", msg)
	}
	if sc.Triggers[0].MonitorScriptType != "python" {
		t.Fatalf("脚本类型应保留为 python，got %q", sc.Triggers[0].MonitorScriptType)
	}

	// 大小写/别名规范化（Sh → shell）
	sc3 := Schedule{
		Name:      "x",
		Triggers:  []Trigger{{Type: TriggerTypeMonitor, MonitorCode: "echo 1", MonitorScriptType: "Sh", MonitorIntervalSec: 30}},
		Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}},
	}
	if msg := normalizeSchedule(&sc3); msg != "" {
		t.Fatalf("内联监控脚本应可保存，got %q", msg)
	}
	if sc3.Triggers[0].MonitorScriptType != "shell" {
		t.Fatalf("Sh 应规范化为 shell，got %q", sc3.Triggers[0].MonitorScriptType)
	}

	// 预制/引用资源时类型字段不生效（执行按资源/预制自身类型）
	sc2 := Schedule{
		Name:      "x",
		Triggers:  []Trigger{{Type: TriggerTypeMonitor, MonitorPrebuilt: PrebuiltMonitorIdle, MonitorScriptType: "python", MonitorIntervalSec: 30}},
		Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}},
	}
	if msg := normalizeSchedule(&sc2); msg != "" {
		t.Fatalf("预制监控应可保存，got %q", msg)
	}
	if sc2.Triggers[0].MonitorScriptType != "" {
		t.Fatalf("预制触发器的类型字段应清空，got %q", sc2.Triggers[0].MonitorScriptType)
	}
}

func TestNormalizeScheduleErrors(t *testing.T) {
	cases := []struct {
		name string
		sc   Schedule
		want string // 期望错误消息片段
	}{
		{"empty name", Schedule{ScheduleType: ScheduleTypeDaily, Time: "10:00", Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "任务名称不能为空"},
		{"bad schedule type", Schedule{Name: "x", ScheduleType: "hourly", Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "触发方式无效"},
		{"time no period", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeTime}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "定时事件周期无效"},
		{"time bad period", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeTime, Period: "hourly"}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "定时事件周期无效"},
		{"one_time no runAt", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodOnce}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "执行日期时间"},
		{"daily bad time", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodDaily, Time: "25:99"}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "执行时间格式"},
		{"weekly no weekdays", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodWeekly, Time: "10:00"}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "星期"},
		{"monthly bad day", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeTime, Period: TriggerPeriodMonthly, Time: "10:00", MonthDays: []int{0, 32}}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "日期"},
		{"log no ref", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeLog}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "日志事件触发器"},
		{"monitor no ref", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeMonitor, MonitorIntervalSec: 30}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "监控事件触发器"},
		{"monitor interval range", Schedule{Name: "x", Triggers: []Trigger{{Type: TriggerTypeMonitor, MonitorPrebuilt: PrebuiltMonitorIdle, MonitorIntervalSec: 999}}, Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveDaily}}}, "监控执行间隔"},
		{"fan no channels", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00", FanMode: FanCurveDaily}, "至少需要一个执行器"},
		{"fan bad mode", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00", FanChannels: []int{1}, FanMode: "turbo"}, "风扇模式无效"},
		{"disk bad action", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00", DiskGroups: []int{1}, DiskAction: "reboot"}, "硬盘动作无效"},
		{"executor delay range", Schedule{Name: "x", ScheduleType: ScheduleTypeDaily, Time: "10:00", Executors: []Executor{{Type: ExecutorControlTask, TaskID: 1, TaskAction: TaskActionRun, DelaySec: 99999}}}, "延迟范围"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := normalizeSchedule(&tc.sc); msg == "" || !contains(msg, tc.want) {
				t.Errorf("expected error containing %q, got %q", tc.want, msg)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}

func TestValidTimeHelpers(t *testing.T) {
	if !validTimeHHMM("00:00") || !validTimeHHMM("23:59") || !validTimeHHMM("09:05") {
		t.Error("valid times rejected")
	}
	for _, bad := range []string{"24:00", "9:00", "09:5", "0900", "12:60", "abc", ""} {
		if validTimeHHMM(bad) {
			t.Errorf("invalid time %q accepted", bad)
		}
	}
	if !validTimeRange("08:00-20:00") {
		t.Error("valid range rejected")
	}
	if validTimeRange("20:00-08:00") || validTimeRange("08:00-08:00") || validTimeRange("8-20") {
		t.Error("invalid range accepted")
	}
	if !validIDs([]int{1, 2, 7}, 1, 7) {
		t.Error("valid ids rejected")
	}
	if validIDs([]int{1, 1}, 1, 7) || validIDs([]int{0}, 1, 7) || validIDs([]int{8}, 1, 7) {
		t.Error("invalid ids accepted")
	}
	if !validIDs(nil, 1, 7) {
		t.Error("empty ids should be allowed (not set / no limit)")
	}
}

// ===== Store 任务 CRUD =====

func TestStoreScheduleCRUD(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	sc := Schedule{
		Category:     ScheduleCategoryFan,
		Name:         "CRUD 测试",
		Enabled:      true,
		ScheduleType: ScheduleTypeDaily,
		FanChannels:  []int{1},
		FanMode:      FanCurveDaily,
		Time:         "08:00",
	}
	id := s.AddSchedule(sc)
	if id != 1 {
		t.Fatalf("first schedule id should be 1, got %d", id)
	}
	if got := s.GetSchedules(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("schedule not persisted in memory")
	}

	// 更新
	sc2 := sc
	sc2.Name = "改过的任务"
	sc2.FanMode = FanCurveQuiet
	if !s.UpdateSchedule(id, sc2) {
		t.Fatal("update failed")
	}
	got := s.GetSchedules()[0]
	if got.Name != "改过的任务" || got.FanMode != FanCurveQuiet {
		t.Errorf("update not applied: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("timestamps wrong: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}

	// 更新不存在的 ID
	if s.UpdateSchedule(999, sc2) {
		t.Error("update of missing id should fail")
	}

	// 删除
	if !s.DeleteSchedule(id) {
		t.Fatal("delete failed")
	}
	if s.DeleteSchedule(id) {
		t.Error("delete of missing id should fail")
	}
	if len(s.GetSchedules()) != 0 {
		t.Error("schedule not removed")
	}
}

// ===== ScheduleLogStore 存储 / 查询 / 保留清理 =====

func TestScheduleLogStoreQueryAndCleanup(t *testing.T) {
	dir := t.TempDir()
	st := InitScheduleLogStore(dir)
	if st == nil {
		t.Fatal("init failed")
	}
	defer func() {
		// 还原单例，避免影响其他测试
		scheduleLogMu.Lock()
		scheduleLogStore = nil
		scheduleLogMu.Unlock()
	}()

	now := time.Now()
	// 写入三条事件：今天 success、今天 failed、昨天 success
	st.Add(ScheduleLog{ScheduleID: 1, TaskName: "风扇A", Category: ScheduleCategoryFan, ScheduleType: ScheduleTypeDaily, Channels: "FAN1", Result: ScheduleResultSuccess, Detail: "已切换为高效", Time: now.Add(-1 * time.Hour)})
	st.Add(ScheduleLog{ScheduleID: 2, TaskName: "硬盘B", Category: ScheduleCategoryDisk, ScheduleType: ScheduleTypeTrigger, Channels: "组2", Result: ScheduleResultFailed, Detail: "设备离线，任务跳过", Time: now.Add(-30 * time.Minute)})
	yesterday := now.AddDate(0, 0, -1)
	st.Add(ScheduleLog{ScheduleID: 1, TaskName: "风扇A", Category: ScheduleCategoryFan, ScheduleType: ScheduleTypeDaily, Channels: "FAN1", Result: ScheduleResultSuccess, Detail: "已切换为静音", Time: yesterday})
	// 前天事件（用于验证保留天数清理边界：保留 1 天 = 昨天保留、前天删除）
	dayBefore := now.AddDate(0, 0, -2)
	st.Add(ScheduleLog{ScheduleID: 3, TaskName: "硬盘C", Category: ScheduleCategoryDisk, ScheduleType: ScheduleTypeMonthly, Channels: "组3", Result: ScheduleResultFailed, Detail: "设备离线", Time: dayBefore})

	// 全量查询（倒序：最新在前）
	res := st.Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if res.Total != 4 {
		t.Fatalf("total should be 4, got %d", res.Total)
	}
	// 校验全局倒序（不依赖具体任务名：插入顺序为 今天-1h、今天-30m、昨天、前天）
	for i := 1; i < len(res.Items); i++ {
		if !res.Items[i-1].Time.After(res.Items[i].Time) {
			t.Errorf("desc order broken at %d: %+v", i, res.Items)
			break
		}
	}

	// 按名称筛选
	res = st.Query(ScheduleLogQueryParams{TaskName: "硬盘", Page: 1, PageSize: 20})
	if res.Total != 2 || res.Items[0].Result != ScheduleResultFailed {
		t.Errorf("name filter wrong: %+v", res.Items)
	}

	// 按类别 + 结果筛选
	res = st.Query(ScheduleLogQueryParams{Category: ScheduleCategoryFan, Result: ScheduleResultSuccess, Page: 1, PageSize: 20})
	if res.Total != 2 {
		t.Errorf("category+result filter wrong: %d", res.Total)
	}

	// 按时间范围（最近 2 小时内的两条事件；用相对时间避免凌晨跨天导致“今天”边界失效）
	res = st.Query(ScheduleLogQueryParams{From: now.Add(-2 * time.Hour), Page: 1, PageSize: 20})
	if res.Total != 2 {
		t.Errorf("from filter wrong: %d", res.Total)
	}

	// 分页
	res = st.Query(ScheduleLogQueryParams{Page: 1, PageSize: 2})
	if res.Total != 4 || len(res.Items) != 2 {
		t.Errorf("paging wrong: total=%d len=%d", res.Total, len(res.Items))
	}
	res = st.Query(ScheduleLogQueryParams{Page: 2, PageSize: 2})
	if len(res.Items) != 2 {
		t.Errorf("page2 wrong: %d", len(res.Items))
	}

	// 文件持久化：新实例加载应读到同样事件
	st2 := InitScheduleLogStore(dir)
	reloaded := st2.Query(ScheduleLogQueryParams{Page: 1, PageSize: 200})
	if reloaded.Total != 4 {
		t.Errorf("reload should get 4 events, got %d", reloaded.Total)
	}

	// 保留天数：设置 1 天 → 前天文件删除、昨天文件保留
	st2.SetRetainDays(ScheduleRetainDays1)
	st2.Cleanup()
	entries, _ := os.ReadDir(filepath.Join(dir, scheduleLogDirName))
	for _, e := range entries {
		if e.Name() == dayBefore.Format("2006-01-02")+".jsonl" {
			t.Errorf("day-before file should be cleaned, found %s", e.Name())
		}
	}
	foundYesterday := false
	for _, e := range entries {
		if e.Name() == yesterday.Format("2006-01-02")+".jsonl" {
			foundYesterday = true
		}
	}
	if !foundYesterday {
		t.Error("yesterday file should be kept with retain=1")
	}
}

// ===== settings 默认值 =====

func TestSettingsScheduleDefaults(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	st := s.GetSettings()
	if st.ScheduleLogRetainDays != DefaultScheduleRetainDays {
		t.Errorf("default retain days should be %d, got %d", DefaultScheduleRetainDays, st.ScheduleLogRetainDays)
	}
	// 资源（schedules/monitorScripts/execScripts）独立文件存储，不在 Settings 里
	if len(s.GetSchedules()) != 0 {
		t.Error("fresh store should have no schedules")
	}
	// 非法保留天数回退默认
	s.SaveSettings(st) // 先保存一次让 settings 非空
	s.settings.ScheduleLogRetainDays = 99
	s.applyDefaultsLocked()
	if s.settings.ScheduleLogRetainDays != DefaultScheduleRetainDays {
		t.Errorf("invalid retain days should fallback, got %d", s.settings.ScheduleLogRetainDays)
	}
}

// TestNormalizeSchedule_MultiTrigger M8 多触发器归一化：字段清空、category 强制、
// 便捷字段回填、存量迁移、空清单拒绝。
func TestNormalizeSchedule_MultiTrigger(t *testing.T) {
	// 两张 daily：无关字段清空 + 便捷字段取首张回填
	sc := Schedule{Name: "多触发器", Category: ScheduleCategoryFan,
		Triggers: []Trigger{
			{Type: ScheduleTypeDaily, Time: "09:00", Weekdays: []int{1}}, // Weekdays 属 weekly，应清空
			{Type: ScheduleTypeDaily, Time: "18:00"},
		},
		Actions: []ScheduleAction{{Type: ActionFanCurve, FanChannels: []int{1}, FanMode: FanCurveQuiet}},
	}
	if msg := normalizeSchedule(&sc); msg != "" {
		t.Fatalf("multi daily should pass: %s", msg)
	}
	if len(sc.Triggers) != 2 || sc.Triggers[0].Weekdays != nil {
		t.Fatalf("unrelated trigger fields should be cleared: %+v", sc.Triggers[0])
	}
	if sc.ScheduleType != ScheduleTypeDaily || sc.Time != "09:00" {
		t.Fatalf("backfill should use first trigger, got %s/%s", sc.ScheduleType, sc.Time)
	}

	// 时间型 + 事件型混合（事件型为新格式 log 内联；类别置空）
	mixed := Schedule{Name: "混合", Category: ScheduleCategoryFan,
		Triggers: []Trigger{
			{Type: ScheduleTypeDaily, Time: "23:00"},
			{Type: TriggerTypeLog, LogEventPath: "/var/log/syslog", LogEventRegex: "DiskSpindown", DiskGroups: []int{2}, AllDay: true, ThresholdMin: 5},
		},
		Actions: []ScheduleAction{{Type: ActionFanCurve, FanChannels: []int{1}, FanMode: FanCurveQuiet}},
	}
	if msg := normalizeSchedule(&mixed); msg != "" {
		t.Fatalf("mixed should pass: %s", msg)
	}
	if mixed.Category != "" {
		t.Fatalf("category should be cleared, got %q", mixed.Category)
	}
	if mixed.ScheduleType != ScheduleTypeDaily {
		t.Fatalf("backfill should use first trigger (daily), got %s", mixed.ScheduleType)
	}
	if len(mixed.Triggers) != 2 || mixed.Triggers[0].Type != TriggerTypeTime ||
		mixed.Triggers[1].Type != TriggerTypeLog || mixed.Triggers[1].LogEventPath != "/var/log/syslog" {
		t.Fatalf("triggers should stay new-format, got %+v", mixed.Triggers)
	}
	if len(mixed.Executors) != 1 || mixed.Executors[0].Type != ExecutorFanControl {
		t.Fatalf("legacy actions should migrate to executors, got %+v", mixed.Executors)
	}

	// 事件型首张 → 回填 trigger 便捷字段（ScheduleType=trigger + TriggerRule + 顶层 DiskGroups）
	ev := Schedule{Name: "事件", Triggers: []Trigger{
		{Type: TriggerTypeMonitor, MonitorPrebuilt: PrebuiltMonitorIdle, MonitorIntervalSec: 30,
			DiskGroups: []int{1}, AllDay: false, TimeRange: "08:00-20:00", ThresholdMin: 10},
		{Type: ScheduleTypeDaily, Time: "09:00"},
	}, Actions: []ScheduleAction{{Type: ActionFanCurve, FanChannels: []int{1}, FanMode: FanCurveQuiet}}}
	if msg := normalizeSchedule(&ev); msg != "" {
		t.Fatalf("event-first should pass: %s", msg)
	}
	if ev.ScheduleType != ScheduleTypeTrigger ||
		ev.TriggerRule == nil || ev.TriggerRule.TimeRange != "08:00-20:00" {
		t.Fatalf("event backfill wrong: %+v", ev)
	}
	if ev.Triggers[0].Type != TriggerTypeMonitor || ev.Triggers[0].MonitorPrebuilt != PrebuiltMonitorIdle ||
		ev.Triggers[0].MonitorDurationMin != 0 {
		t.Fatalf("monitor trigger should stay as-is: %+v", ev.Triggers[0])
	}

	// 空清单允许（仅手动触发）
	empty := Schedule{Name: "空", Executors: []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: false, FanCurve: FanCurveQuiet}}}
	if msg := normalizeSchedule(&empty); msg != "" {
		t.Fatalf("empty triggers should be allowed, got %q", msg)
	}
	if len(empty.Triggers) != 0 {
		t.Fatalf("triggers should stay empty, got %+v", empty.Triggers)
	}

	// 存量迁移：旧便捷字段 → 新格式触发器清单 + 执行器
	legacy := Schedule{Name: "旧任务", Category: ScheduleCategoryFan, ScheduleType: ScheduleTypeWeekly,
		Time: "10:00", Weekdays: []int{1, 5},
		FanChannels: []int{1}, FanMode: FanCurveQuiet}
	if msg := normalizeSchedule(&legacy); msg != "" {
		t.Fatalf("legacy weekly should pass: %s", msg)
	}
	if len(legacy.Triggers) != 1 || legacy.Triggers[0].Type != TriggerTypeTime ||
		legacy.Triggers[0].Period != TriggerPeriodWeekly || len(legacy.Triggers[0].Weekdays) != 2 {
		t.Fatalf("legacy migration wrong: %+v", legacy.Triggers)
	}
	if len(legacy.Executors) != 1 || legacy.Executors[0].Type != ExecutorFanControl {
		t.Fatalf("legacy fan fields should migrate to executor: %+v", legacy.Executors)
	}
}
