package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===== 调度引擎单元测试（M2） =====

// fakeScheduleSession 模拟 BLE 会话（设备在线 + 设置同步记录）。
type fakeScheduleSession struct {
	online       bool
	lastSettings Settings
}

func (f *fakeScheduleSession) IsEncryptedSession() bool { return f.online }

func (f *fakeScheduleSession) SetSettings(s Settings) { f.lastSettings = s }

func (f *fakeScheduleSession) SetFanSpeed(_, _ int) (CommandResult, error) {
	return CommandResult{OK: true}, nil
}

// fakeDiskExecutor 模拟硬盘执行器，记录调用序列。
type fakeDiskExecutor struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]bool
}

func (f *fakeDiskExecutor) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeDiskExecutor) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.calls...)
	return out
}

func (f *fakeDiskExecutor) PowerOn(g int) (CommandResult, error) {
	f.record(fmt.Sprintf("PowerOn:%d", g))
	if f.fail[fmt.Sprintf("PowerOn:%d", g)] {
		return CommandResult{OK: false, Message: "mock fail"}, fmt.Errorf("mock fail")
	}
	return CommandResult{OK: true, Message: "ok"}, nil
}

func (f *fakeDiskExecutor) PowerOff(g int, action string) (CommandResult, error) {
	f.record(fmt.Sprintf("PowerOff:%d:%s", g, action))
	if f.fail[fmt.Sprintf("PowerOff:%d:%s", g, action)] {
		return CommandResult{OK: false, Message: "mock fail"}, fmt.Errorf("mock fail")
	}
	return CommandResult{OK: true}, nil
}

func (f *fakeDiskExecutor) ForcePowerOff(g int) (CommandResult, error) {
	f.record(fmt.Sprintf("ForcePowerOff:%d", g))
	return CommandResult{OK: true}, nil
}

// newTestScheduler 构造调度器测试环境（真实 Store + 独立日志单例）。
func newTestScheduler(t *testing.T, online bool) (*Store, *Scheduler, *fakeScheduleSession, *fakeDiskExecutor) {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := store.Load(); err != nil {
		t.Fatalf("store load: %v", err)
	}
	InitScheduleLogStore(t.TempDir())
	sess := &fakeScheduleSession{online: online}
	disk := &fakeDiskExecutor{fail: map[string]bool{}}
	return store, NewScheduler(store, sess, disk), sess, disk
}

// makeSchedule 构造测试任务（fan 类别；按触发方式补齐字段）。
func makeSchedule(id int, stype string, now time.Time) Schedule {
	sc := Schedule{
		ID:           id,
		Category:     ScheduleCategoryFan,
		Name:         fmt.Sprintf("任务%d", id),
		Enabled:      true,
		ScheduleType: stype,
		FanChannels:  []int{1},
		FanMode:      FanCurveQuiet,
	}
	switch stype {
	case ScheduleTypeOneTime:
		r := now.Add(-30 * time.Second) // 窗口内
		sc.RunAt = &r
	case ScheduleTypeDaily, ScheduleTypeWeekly, ScheduleTypeMonthly:
		sc.Time = now.Format("15:04")
	}
	if stype == ScheduleTypeWeekly {
		sc.Weekdays = []int{goWeekdayToISO(now)}
	}
	if stype == ScheduleTypeMonthly {
		sc.MonthDays = []int{now.Day()}
	}
	return sc
}

// TriggerManualAsync：任务存在 → 投递调度循环并异步执行（loop 的 manualCh case 即时消费，不依赖 tick）
func TestTriggerManualAsync(t *testing.T) {
	store, s, _, disk := newTestScheduler(t, true)
	store.AddSchedule(Schedule{ID: 1, Name: "异步任务", Enabled: true,
		Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 1, DiskAction: DiskActionOnline}},
	})
	s.Start()
	defer s.Stop()

	msg, ok := s.TriggerManualAsync(1)
	if !ok || msg != "" {
		t.Fatalf("async trigger should ok, got %q %v", msg, ok)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range disk.Calls() {
			if c == "PowerOn:1" {
				return // 异步执行完成
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("async task not executed within timeout, calls=%v", disk.Calls())
}

// TriggerManualAsync：任务不存在 → 失败（不投递）
func TestTriggerManualAsync_Missing(t *testing.T) {
	_, s, _, _ := newTestScheduler(t, true)
	s.Start()
	defer s.Stop()
	if _, ok := s.TriggerManualAsync(99); ok {
		t.Fatal("missing task should fail")
	}
}

func TestScheduleMatchesNow_OneTime(t *testing.T) {
	_, _, _, _ = newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 窗口内 → 匹配
	sc := makeSchedule(1, ScheduleTypeOneTime, now)
	if !scheduleMatchesNow(sc, now) {
		t.Fatal("one_time 在窗口内应匹配")
	}
	// 未来 → 不匹配
	future := now.Add(1 * time.Minute)
	sc.RunAt = &future
	if scheduleMatchesNow(sc, now) {
		t.Fatal("one_time RunAt 在未来不应匹配")
	}
	// 超窗口（错过）→ 不匹配
	past := now.Add(-10 * time.Minute)
	sc.RunAt = &past
	if scheduleMatchesNow(sc, now) {
		t.Fatal("one_time 超出宽容窗口（错过）不应执行")
	}
	// 已执行（RunAt 后有日志，per-trigger key）→ 不匹配
	sc.RunAt = &now
	GetScheduleLogStore().Add(ScheduleLog{ScheduleID: 1, TaskName: "t", Time: now.Add(5 * time.Second), Result: ScheduleResultSuccess, TriggerKey: "one_time:" + now.Format(time.RFC3339)})
	if scheduleMatchesNow(sc, now) {
		t.Fatal("one_time 已执行后不应再次触发")
	}
	// RunAt 为空 → 不匹配
	sc.RunAt = nil
	if scheduleMatchesNow(sc, now) {
		t.Fatal("one_time RunAt 为空不应匹配")
	}
}

func TestScheduleMatchesNow_Daily(t *testing.T) {
	_, _, _, _ = newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 5, 0, time.Local) // 10:00:05

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	if !scheduleMatchesNow(sc, now) {
		t.Fatal("daily 时间匹配应触发")
	}
	// 时间不匹配
	sc.Time = "11:00"
	if scheduleMatchesNow(sc, now) {
		t.Fatal("daily 时间不匹配不应触发")
	}
	// 当天已执行（任意结果日志，per-trigger key）→ 不重复
	sc.Time = "10:00"
	GetScheduleLogStore().Add(ScheduleLog{ScheduleID: 1, TaskName: "t", Time: now, Result: ScheduleResultFailed, TriggerKey: "daily:10:00"})
	if scheduleMatchesNow(sc, now) {
		t.Fatal("daily 当天已执行（含失败）不应重复触发")
	}
}

func TestScheduleMatchesNow_Weekly(t *testing.T) {
	_, _, _, _ = newTestScheduler(t, true)
	// 2026-09-18 为周五 → ISO 星期 5
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	if got := goWeekdayToISO(now); got != 5 {
		t.Fatalf("2026-09-18 应为周五(5)，got %d", got)
	}

	sc := makeSchedule(1, ScheduleTypeWeekly, now)
	if !scheduleMatchesNow(sc, now) {
		t.Fatal("weekly 星期匹配应触发")
	}
	sc.Weekdays = []int{1}
	if scheduleMatchesNow(sc, now) {
		t.Fatal("weekly 星期不匹配不应触发")
	}
	// 周日边界：ISO 7
	sun := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local) // 周日
	if got := goWeekdayToISO(sun); got != 7 {
		t.Fatalf("周日应映射为 7，got %d", got)
	}
	sc2 := makeSchedule(2, ScheduleTypeWeekly, sun)
	sc2.Weekdays = []int{7}
	if !scheduleMatchesNow(sc2, sun) {
		t.Fatal("weekly 周日(7)应匹配")
	}
}

func TestScheduleMatchesNow_Monthly(t *testing.T) {
	_, _, _, _ = newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeMonthly, now)
	if !scheduleMatchesNow(sc, now) {
		t.Fatal("monthly 日期匹配应触发")
	}
	sc.MonthDays = []int{17}
	if scheduleMatchesNow(sc, now) {
		t.Fatal("monthly 日期不匹配不应触发")
	}
}

func TestScheduleMatchesNow_TriggerAndDisabled(t *testing.T) {
	_, _, _, _ = newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 触发任务：时间轮询永不匹配（M3 事件驱动）
	sc := makeSchedule(1, ScheduleTypeTrigger, now)
	if scheduleMatchesNow(sc, now) {
		t.Fatal("trigger 任务不应由时间轮询触发")
	}
	// 禁用任务不触发
	sc2 := makeSchedule(2, ScheduleTypeDaily, now)
	sc2.Enabled = false
	if scheduleMatchesNow(sc2, now) {
		t.Fatal("禁用任务不应触发")
	}
}

func TestSchedulerExecuteFan(t *testing.T) {
	store, sched, sess, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	sched.execute(sc)

	// 曲线已切换并双写
	set := store.GetSettings()
	if set.Fans[0].ActiveCurve != FanCurveQuiet {
		t.Fatalf("ActiveCurve 应切换为 %s，got %s", FanCurveQuiet, set.Fans[0].ActiveCurve)
	}
	if sess.lastSettings.Fans[0].ActiveCurve != FanCurveQuiet {
		t.Fatal("ble 设置副本未同步 ActiveCurve")
	}
	// 日志写入
	st := GetScheduleLogStore()
	if !st.HasLogSince(1, now.Add(-time.Minute)) {
		t.Fatal("执行日志未写入")
	}
	logs := st.Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 || logs.Items[0].Result != ScheduleResultSuccess {
		t.Fatalf("日志应 1 条 success，got %+v", logs.Items)
	}
	if logs.Items[0].Channels != "FAN1" {
		t.Fatalf("通道摘要应为 FAN1，got %q", logs.Items[0].Channels)
	}
}

func TestSchedulerExecuteFan_UnknownChannel(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	sc.FanChannels = []int{1, 99} // 99 不存在
	sched.execute(sc)

	// 部分成功：FAN1 切换保存，任务结果 failed（99 不存在）
	set := store.GetSettings()
	if set.Fans[0].ActiveCurve != FanCurveQuiet {
		t.Fatal("存在的通道仍应切换")
	}
	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 || logs.Items[0].Result != ScheduleResultFailed {
		t.Fatalf("应记 failed（含不存在的通道），got %+v", logs.Items)
	}
	if logs.Items[0].Detail == "" {
		t.Fatal("失败详情不应为空")
	}
}

func TestSchedulerExecuteDisk_Online(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	sc.Category = ScheduleCategoryDisk
	sc.DiskGroups = []int{1, 2}
	sc.DiskAction = DiskActionOnline
	sched.execute(sc)

	calls := disk.Calls()
	if len(calls) != 2 || calls[0] != "PowerOn:1" || calls[1] != "PowerOn:2" {
		t.Fatalf("应串行调用 PowerOn 1/2，got %v", calls)
	}
	_ = store
	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 || logs.Items[0].Result != ScheduleResultSuccess {
		t.Fatalf("应记 success，got %+v", logs.Items)
	}
	if logs.Items[0].Channels != "组1,组2" {
		t.Fatalf("通道摘要应为 组1,组2，got %q", logs.Items[0].Channels)
	}
}

func TestSchedulerExecuteDisk_OfflinePaths(t *testing.T) {
	_, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 非强制下线 → PowerOff(action=force)
	sc := makeSchedule(1, ScheduleTypeDaily, now)
	sc.Category = ScheduleCategoryDisk
	sc.DiskGroups = []int{2}
	sc.DiskAction = DiskActionOffline
	sc.ForceOff = false
	sched.execute(sc)

	// 强制下线（三级：正常下线失败 → 强制断电）
	sc2 := makeSchedule(2, ScheduleTypeDaily, now)
	sc2.Category = ScheduleCategoryDisk
	sc2.DiskGroups = []int{3}
	sc2.DiskAction = DiskActionOffline
	sc2.ForceOff = true
	disk.fail["PowerOff:3:force"] = true // 第一级失败 → 第三级强制断电
	sched.execute(sc2)

	calls := disk.Calls()
	// 非强制：PowerOff:2:force；强制三级：正常失败 → ForcePowerOff:3
	if len(calls) != 3 || calls[0] != "PowerOff:2:force" || calls[1] != "PowerOff:3:force" || calls[2] != "ForcePowerOff:3" {
		t.Fatalf("下线路径错误，got %v", calls)
	}
}

func TestSchedulerExecuteDisk_DeviceOffline(t *testing.T) {
	_, sched, _, disk := newTestScheduler(t, false) // 设备离线
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	sc.Category = ScheduleCategoryDisk
	sc.DiskGroups = []int{1}
	sc.DiskAction = DiskActionOnline
	sched.execute(sc)

	if calls := disk.Calls(); len(calls) != 0 {
		t.Fatalf("设备离线不应调用执行器，got %v", calls)
	}
	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 || logs.Items[0].Result != ScheduleResultFailed {
		t.Fatalf("设备离线应记 failed，got %+v", logs.Items)
	}
	if logs.Items[0].Detail == "" {
		t.Fatal("失败详情应包含设备离线原因")
	}
}

func TestSchedulerExecuteDisk_Failure(t *testing.T) {
	_, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	sc.Category = ScheduleCategoryDisk
	sc.DiskGroups = []int{1, 2}
	sc.DiskAction = DiskActionOnline
	disk.fail["PowerOn:2"] = true
	sched.execute(sc)

	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 || logs.Items[0].Result != ScheduleResultFailed {
		t.Fatalf("部分失败应记 failed，got %+v", logs.Items)
	}
	if !strings.Contains(logs.Items[0].Detail, "组2") {
		t.Fatalf("失败详情应提及组2，got %q", logs.Items[0].Detail)
	}
}

func TestSchedulerOneTime_AutoDisable(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeOneTime, now)
	mustAddSchedule(t, store, sc)

	// 模拟 checkAndRun：任务入库后执行
	sched.checkAndRun(now)

	// 断言：一次性任务执行后应自动禁用
	scheds := store.GetSchedules()
	if len(scheds) != 1 || scheds[0].Enabled {
		t.Fatal("一次性任务执行后应自动禁用")
	}
	// 再次 checkAndRun 不产生新日志
	sched.checkAndRun(now.Add(10 * time.Second))
	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 {
		t.Fatalf("一次性任务只应执行一次，got %d 条日志", len(logs.Items))
	}
}

func TestSchedulerCheckAndRun_NoRepeat(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	sc := makeSchedule(1, ScheduleTypeDaily, now)
	mustAddSchedule(t, store, sc)

	sched.checkAndRun(now)                       // 命中执行
	sched.checkAndRun(now.Add(10 * time.Second)) // 同分钟再次命中 → 被日志挡住

	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 1 {
		t.Fatalf("daily 同一天只应执行一次，got %d 条日志", len(logs.Items))
	}
	set := store.GetSettings()
	if set.Fans[0].ActiveCurve != FanCurveQuiet {
		t.Fatal("风扇曲线应已切换")
	}
}

func TestSchedulerCheckAndRun_SerialMultiTask(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 两个同刻命中任务：风扇 + 硬盘（顺序执行，互不阻塞）
	fan := makeSchedule(1, ScheduleTypeDaily, now)
	diskTask := makeSchedule(2, ScheduleTypeDaily, now)
	diskTask.Category = ScheduleCategoryDisk
	diskTask.DiskGroups = []int{1}
	diskTask.DiskAction = DiskActionOnline
	store.SaveSchedules([]Schedule{fan, diskTask})

	sched.checkAndRun(now)

	calls := disk.Calls()
	if len(calls) != 1 || calls[0] != "PowerOn:1" {
		t.Fatalf("硬盘任务应执行一次 PowerOn:1，got %v", calls)
	}
	logs := GetScheduleLogStore().Query(ScheduleLogQueryParams{Page: 1, PageSize: 20})
	if len(logs.Items) != 2 {
		t.Fatalf("两个任务应各记一条日志，got %d", len(logs.Items))
	}
}

// mustSettingsWithSchedule 在 store 设置中追加任务并保存（返回原 Settings 供断言复用）。
func mustAddSchedule(t *testing.T, store *Store, sc Schedule) {
	t.Helper()
	store.AddSchedule(sc)
}

// TestTriggerRuleAllows 触发任务生效规则校验（triggerRuleAllowsTr 纯逻辑，供 log/monitor 事件型使用）。
func TestTriggerRuleAllows(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local) // 周五
	cases := []struct {
		name string
		rule *TriggerRule
		want bool
	}{
		{"nil", nil, false},
		{"allDay 忽略时间范围", &TriggerRule{AllDay: true, TimeRange: "08:00-20:00"}, true},
		{"时间范围内", &TriggerRule{TimeRange: "08:00-20:00"}, true},
		{"时间范围下边界", &TriggerRule{TimeRange: "10:00-20:00"}, true},
		{"时间范围上边界", &TriggerRule{TimeRange: "08:00-10:00"}, true},
		{"早于范围", &TriggerRule{TimeRange: "11:00-20:00"}, false},
		{"晚于范围", &TriggerRule{TimeRange: "08:00-09:00"}, false},
		{"周限制命中", &TriggerRule{AllDay: true, WeekLimit: []int{5}}, true},
		{"周限制未命中", &TriggerRule{AllDay: true, WeekLimit: []int{1, 2, 3}}, false},
		{"月日限制命中", &TriggerRule{AllDay: true, MonthDayLimit: []int{18}}, true},
		{"月日限制未命中", &TriggerRule{AllDay: true, MonthDayLimit: []int{1, 2}}, false},
		{"双限制均空不限", &TriggerRule{AllDay: true}, true},
	}
	for _, c := range cases {
		if got := triggerRuleAllows(c.rule, now); got != c.want {
			t.Errorf("%s: triggerRuleAllows = %v, want %v", c.name, got, c.want)
		}
	}
}

// ===== M6 任务串联：待执行队列 / 分支 / 遵循开关 / 防护 =====

// addTestTasks 批量写入测试任务（返回各 ID）。
func addTestTasks(t *testing.T, store *Store, tasks ...Schedule) []int {
	t.Helper()
	ids := make([]int, 0, len(tasks))
	for _, sc := range tasks {
		ids = append(ids, store.AddSchedule(sc))
	}
	return ids
}

func TestRunTaskAction_EnqueueAndExecute(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// A：daily 时间任务，动作=执行任务 B（立即投递）
	a := makeSchedule(1, ScheduleTypeDaily, now)
	a.Actions = []ScheduleAction{{Type: ActionRunTask, TaskID: 2}}
	// B：daily 时间任务（23:59 避免常规触发），动作=硬盘下线（由 A 投递执行）
	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Time = "23:59"
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	sched.now = func() time.Time { return now }
	ids := addTestTasks(t, store, a, b)

	sched.checkAndRun(now) // 第一轮：A 命中执行 → 投递 B 入队
	if len(sched.pending) != 1 || sched.pending[0].ScheduleID != ids[1] {
		t.Fatalf("expect B queued after A runs, pending=%+v", sched.pending)
	}
	if sched.pending[0].SourceID != ids[0] || sched.pending[0].SourceName != "任务1" {
		t.Errorf("pending source not recorded: %+v", sched.pending[0])
	}
	if len(disk.Calls()) != 0 {
		t.Fatalf("B should not run in same tick, calls=%v", disk.Calls())
	}

	sched.checkAndRun(now) // 第二轮：队列到期 → B 执行
	calls := disk.Calls()
	if len(calls) != 1 || calls[0] != "PowerOff:1:force" {
		t.Fatalf("B should PowerOff group1, calls=%v", calls)
	}
	if len(sched.pending) != 0 {
		t.Errorf("pending should be drained, got %d", len(sched.pending))
	}
	// B 的日志带串联来源
	var bLog *ScheduleLog
	for _, e := range GetScheduleLogStore().Query(ScheduleLogQueryParams{}).Items {
		if e.ScheduleID == 2 {
			bLog = &e
		}
	}
	if bLog == nil {
		t.Fatal("B log missing")
	}
	if bLog.SourceScheduleID != ids[0] || bLog.SourceResult != ScheduleResultSuccess {
		t.Errorf("B log source wrong: %+v", bLog)
	}
	if !strings.Contains(bLog.Detail, "任务「任务1」") {
		t.Errorf("B log detail should mention source, got %q", bLog.Detail)
	}
}

func TestRunTaskAction_DelayRespected(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	a := makeSchedule(1, ScheduleTypeDaily, now)
	a.Actions = []ScheduleAction{{Type: ActionRunTask, TaskID: 2, DelaySec: 60}}
	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Time = "23:59"
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	sched.now = func() time.Time { return now }
	addTestTasks(t, store, a, b)

	sched.checkAndRun(now)
	// M9 新语义：run_task 迁移为 control_task 执行器，DelaySec 变为执行器独立延迟
	if len(sched.delayedExecutors) != 1 || sched.delayedExecutors[0].Executor.DelaySec != 60 {
		t.Fatalf("delayed executor should be queued, got %+v", sched.delayedExecutors)
	}
	if sched.delayedExecutors[0].RunAt.Sub(now) < 55*time.Second {
		t.Errorf("executor RunAt should be now+60s, got %v", sched.delayedExecutors[0].RunAt.Sub(now))
	}
	// 到期前再 tick：不投递不执行
	sched.checkAndRun(now.Add(30 * time.Second))
	if len(disk.Calls()) != 0 {
		t.Fatalf("B should not run before delay elapses, calls=%v", disk.Calls())
	}
	// 到期后：延迟执行器到期 → 投递 B
	sched.checkAndRun(now.Add(61 * time.Second))
	if len(sched.pending) != 1 || sched.pending[0].ScheduleID != 2 {
		t.Fatalf("B should be queued after executor delay, pending=%+v", sched.pending)
	}
	// 下一 tick：B 执行
	sched.checkAndRun(now.Add(71 * time.Second))
	if len(disk.Calls()) != 1 {
		t.Fatalf("B should run after delay, calls=%v", disk.Calls())
	}
}

func TestRunTaskAction_Dedup(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// A 的动作清单里两个 run_task 都指向 B → 只入队一个实例
	a := makeSchedule(1, ScheduleTypeDaily, now)
	a.Actions = []ScheduleAction{
		{Type: ActionRunTask, TaskID: 2},
		{Type: ActionRunTask, TaskID: 2},
	}
	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Time = "23:59"
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	sched.now = func() time.Time { return now }
	addTestTasks(t, store, a, b)

	sched.checkAndRun(now)
	if len(sched.pending) != 1 {
		t.Fatalf("duplicate target should be deduped, pending=%+v", sched.pending)
	}
	sched.checkAndRun(now)
	if len(disk.Calls()) != 1 {
		t.Fatalf("B should run exactly once, calls=%v", disk.Calls())
	}
}

func TestPendingRun_RespectEnabled(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// B 默认启用，先跑一个场景：respectEnabled=false 时禁用也执行
	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	ids := addTestTasks(t, store, b)

	// 禁用 B
	scheds := store.GetSchedules()
	for i := range scheds {
		if scheds[i].ID == ids[0] {
			scheds[i].Enabled = false
		}
	}
	store.SaveSchedules(scheds)

	// respectEnabled=false → 即使 B 禁用也执行
	sched.pending = append(sched.pending, pendingRun{ScheduleID: ids[0], RunAt: now, SourceID: 1, SourceName: "A"})
	sched.checkAndRun(now)
	if len(disk.Calls()) != 1 {
		t.Fatalf("respectEnabled=false should run disabled B, calls=%v", disk.Calls())
	}
	// 复位调用记录
	disk.calls = nil

	// respectEnabled=true → 跳过并记 failed 日志
	sched.pending = append(sched.pending, pendingRun{ScheduleID: ids[0], RunAt: now, SourceID: 1, SourceName: "A", RespectEnabled: true})
	sched.checkAndRun(now)
	if len(disk.Calls()) != 0 {
		t.Fatalf("respectEnabled=true should skip disabled B, calls=%v", disk.Calls())
	}
	items := GetScheduleLogStore().Query(ScheduleLogQueryParams{}).Items
	foundSkip := false
	for _, e := range items {
		if e.ScheduleID == ids[0] && e.Result == ScheduleResultFailed && strings.Contains(e.Detail, "已停用") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("skip should be logged as failed with reason, got %+v", items)
	}
}

func TestPendingRun_RespectTimeRule(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local) // B 的 daily 时间=10:00

	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	b.Enabled = false // 停用：隔离常规触发（per-trigger 语义下投递与常规独立），投递执行仍照跑
	ids := addTestTasks(t, store, b)

	// respectTimeRule=true，投递时刻 14:00 ≠ 10:00 → 跳过
	sched.pending = append(sched.pending, pendingRun{ScheduleID: ids[0], RunAt: now, SourceID: 1, SourceName: "A", RespectTimeRule: true})
	sched.checkAndRun(now.Add(4 * time.Hour))
	if len(disk.Calls()) != 0 {
		t.Fatalf("respectTimeRule should skip when time not matched, calls=%v", disk.Calls())
	}
	items := GetScheduleLogStore().Query(ScheduleLogQueryParams{}).Items
	if len(items) == 0 || !strings.Contains(items[0].Detail, "时间规则") {
		t.Fatalf("time-rule skip should be logged, got %+v", items[0])
	}

	// respectTimeRule=true 且投递时刻匹配 → 执行
	sched.pending = append(sched.pending, pendingRun{ScheduleID: ids[0], RunAt: now, SourceID: 1, SourceName: "A", RespectTimeRule: true})
	sched.checkAndRun(now)
	if len(disk.Calls()) != 1 {
		t.Fatalf("respectTimeRule should run when time matched, calls=%v", disk.Calls())
	}
}

func TestPendingRun_RespectTimeRuleTrigger(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 目标为 trigger 任务（新格式 log 事件触发器 + 生效规则 AllDay=true → 恒允许）
	tr := Schedule{
		Category:     ScheduleCategoryDisk,
		Name:         "触发B",
		Enabled:      true,
		ScheduleType: ScheduleTypeTrigger,
		DiskGroups:   []int{1},
		DiskAction:   DiskActionOffline,
		Triggers:     []Trigger{{Type: TriggerTypeLog, LogEventPath: "/var/log/syslog", LogEventRegex: "DiskSpindown", AllDay: true, ThresholdMin: 30}},
	}
	ids := addTestTasks(t, store, tr)

	sched.pending = append(sched.pending, pendingRun{ScheduleID: ids[0], RunAt: now, SourceID: 1, SourceName: "A", RespectTimeRule: true})
	sched.checkAndRun(now)
	if len(disk.Calls()) != 1 {
		t.Fatalf("trigger target with AllDay rule should run, calls=%v", disk.Calls())
	}

	// 触发器时间范围不命中 → 跳过
	scheds := store.GetSchedules()
	for i := range scheds {
		if scheds[i].ID == ids[0] {
			scheds[i].Triggers[0].AllDay = false
			scheds[i].Triggers[0].TimeRange = "20:00-22:00"
		}
	}
	store.SaveSchedules(scheds)
	disk.calls = nil
	sched.pending = append(sched.pending, pendingRun{ScheduleID: ids[0], RunAt: now, SourceID: 1, SourceName: "A", RespectTimeRule: true})
	sched.checkAndRun(now)
	if len(disk.Calls()) != 0 {
		t.Fatalf("trigger target out of time range should skip, calls=%v", disk.Calls())
	}
}

func TestExecute_FailureBranchDispatches(t *testing.T) {
	// 设备离线 → A 动作全部失败 → 失败分支 run_task C 投递（来源结果=failed）
	store, sched, _, _ := newTestScheduler(t, false)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	a := makeSchedule(1, ScheduleTypeDaily, now)
	a.Category = ScheduleCategoryDisk
	a.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	a.FailureActions = []ScheduleAction{{Type: ActionRunTask, TaskID: 0}} // 在 addTestTasks 后补实际 ID
	c := makeSchedule(3, ScheduleTypeDaily, now)
	c.Time = "23:59"
	c.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	ids := addTestTasks(t, store, a, c)
	a.FailureActions[0].TaskID = ids[1] // A 失败 → 投递 C（实际 ID）

	sched.checkAndRun(now)
	if len(sched.pending) != 1 || sched.pending[0].ScheduleID != ids[1] {
		t.Fatalf("failure branch should enqueue C, pending=%+v", sched.pending)
	}
	if sched.pending[0].SourceResult != ScheduleResultFailed {
		t.Errorf("C pending source result should be failed, got %q", sched.pending[0].SourceResult)
	}
}

func TestExecute_SuccessBranchDispatches(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	a := makeSchedule(1, ScheduleTypeDaily, now)
	a.Actions = []ScheduleAction{{Type: ActionRunTask, TaskID: 0}} // 在 addTestTasks 后补实际 ID
	c := makeSchedule(3, ScheduleTypeDaily, now)
	c.Time = "23:59"
	c.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	ids := addTestTasks(t, store, a, c)
	a.Actions[0].TaskID = ids[1]

	sched.checkAndRun(now)
	if len(sched.pending) != 1 || sched.pending[0].ScheduleID != ids[1] {
		t.Fatalf("success branch should enqueue C, pending=%+v", sched.pending)
	}
	if sched.pending[0].SourceResult != ScheduleResultSuccess {
		t.Errorf("C pending source result should be success, got %q", sched.pending[0].SourceResult)
	}
}

func TestEnableDisableTaskAction(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	a := makeSchedule(1, ScheduleTypeDaily, now)
	a.Actions = []ScheduleAction{{Type: ActionDisableTask, TaskID: 2}}
	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	ids := addTestTasks(t, store, a, b)

	// A 执行 → 停用 B
	sched.checkAndRun(now)
	var bEnabled bool
	for _, e := range store.GetSchedules() {
		if e.ID == ids[1] {
			bEnabled = e.Enabled
		}
	}
	if bEnabled {
		t.Error("disable_task should set B disabled")
	}

	// 幂等：B 已停用时再执行 disable 动作不报失败（直接构造执行验证）
	if d, f := sched.execSetTaskEnabled(ScheduleAction{Type: ActionDisableTask, TaskID: ids[1]}, false); len(f) != 0 || d == "" {
		t.Errorf("disable on already-disabled should be idempotent, detail=%q fails=%v", d, f)
	}

	// enable 动作
	a2 := makeSchedule(4, ScheduleTypeDaily, now)
	a2.Actions = []ScheduleAction{{Type: ActionEnableTask, TaskID: ids[1]}}
	store.AddSchedule(a2)
	sched.checkAndRun(now.Add(24 * time.Hour))
	bEnabled = false
	for _, e := range store.GetSchedules() {
		if e.ID == 2 {
			bEnabled = e.Enabled
		}
	}
	if !bEnabled {
		t.Error("enable_task should set B enabled")
	}
}

func TestRunTaskAction_ChainDepthLimit(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 链深度已达上限的任务（Depth=maxScheduleChainDepth），其 run_task 拒绝投递
	b := makeSchedule(2, ScheduleTypeDaily, now)
	b.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	b.Enabled = false // 停用：隔离 B 常规触发（断言聚焦 deep 的深度拒绝日志）
	ids := addTestTasks(t, store, b)

	deep := makeSchedule(5, ScheduleTypeDaily, now)
	deep.Actions = []ScheduleAction{{Type: ActionRunTask, TaskID: ids[0]}}
	deep.Enabled = false // 停用：仅验证"被投递的超深度任务拒绝再投递"路径，隔离常规重复触发
	did := store.AddSchedule(deep)

	// 模拟：深度已达上限的任务被投递执行
	sched.pending = append(sched.pending, pendingRun{ScheduleID: did, RunAt: now, SourceID: 0, SourceName: "", Depth: maxScheduleChainDepth})
	sched.checkAndRun(now)
	if len(sched.pending) != 0 {
		t.Fatalf("over-depth run_task should be rejected, pending=%+v", sched.pending)
	}
	items := GetScheduleLogStore().Query(ScheduleLogQueryParams{}).Items
	if len(items) == 0 || items[0].Result != ScheduleResultFailed || !strings.Contains(items[0].Detail, "深度") {
		t.Fatalf("depth rejection should be logged as failed, got %+v", items[0])
	}
}

func TestPendingRun_MissingTargetSkipped(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	_ = store

	// 队列条目指向已删除任务（99）→ 静默跳过
	sched.pending = append(sched.pending, pendingRun{ScheduleID: 99, RunAt: now, SourceID: 1, SourceName: "A"})
	sched.checkAndRun(now)
	if len(sched.pending) != 0 {
		t.Errorf("missing target should be dropped from queue")
	}
	if len(disk.Calls()) != 0 {
		t.Errorf("no execution expected")
	}
}

func TestRunPending_OrderPreserved(t *testing.T) {
	store, sched, _, disk := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 三个 target 都到期，投递顺序保持（按入队序）。
	// 目标停用隔离常规匹配：per-trigger 语义下投递执行与常规触发独立，
	// 本用例聚焦投递队列顺序（respectEnabled 默认 false，停用目标照常执行）。
	var ids []int
	for i := 2; i <= 4; i++ {
		sc := makeSchedule(i, ScheduleTypeDaily, now)
		sc.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
		sc.Enabled = false
		ids = append(ids, store.AddSchedule(sc))
	}
	for _, id := range ids {
		sched.pending = append(sched.pending, pendingRun{ScheduleID: id, RunAt: now, SourceID: 1, SourceName: "A"})
	}
	sched.checkAndRun(now)
	calls := disk.Calls()
	if len(calls) != 3 || calls[0] != "PowerOff:1:force" || calls[1] != "PowerOff:1:force" || calls[2] != "PowerOff:1:force" {
		t.Fatalf("pending order not preserved, calls=%v", calls)
	}
}

func TestOneTimePending_AutoDisabled(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	// 被投递的一次性任务执行后同样自动禁用
	ot := makeSchedule(2, ScheduleTypeOneTime, now)
	ot.Actions = []ScheduleAction{{Type: ActionDiskPower, DiskGroups: []int{1}, DiskAction: DiskActionOffline}}
	id := store.AddSchedule(ot)

	sched.pending = append(sched.pending, pendingRun{ScheduleID: id, RunAt: now, SourceID: 1, SourceName: "A"})
	sched.checkAndRun(now)
	for _, e := range store.GetSchedules() {
		if e.ID == id && e.Enabled {
			t.Error("one-time task should auto-disable after pending run")
		}
	}
}

// ===== M8 多触发器专项测试 =====

// TestMatchTimeTrigger_MultiDailyPerTriggerDedup 多张 daily 触发器独立消费
// （per-trigger 防重复）：09:00 已执行不影响 18:00 触发。
func TestMatchTimeTrigger_MultiDailyPerTriggerDedup(t *testing.T) {
	InitScheduleLogStore(t.TempDir())
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.Local)
	sc := Schedule{ID: 1, Enabled: true, Triggers: []Trigger{
		{Type: ScheduleTypeDaily, Time: "09:00"},
		{Type: ScheduleTypeDaily, Time: "18:00"},
	}}
	if key, _, ok := matchTimeTrigger(sc, now); !ok || key != "daily:09:00" {
		t.Fatalf("09:00 should match daily:09:00, got %q ok=%v", key, ok)
	}
	if !scheduleMatchesNow(sc, now) {
		t.Fatal("09:00 should fire before any log")
	}
	// 09:00 已消费 → 不再触发
	GetScheduleLogStore().Add(ScheduleLog{ScheduleID: 1, Time: now, Result: ScheduleResultSuccess, TriggerKey: "daily:09:00"})
	if scheduleMatchesNow(sc, now) {
		t.Fatal("09:00 consumed should not refire")
	}
	// 18:00 独立消费 → 仍触发
	evening := time.Date(2026, 9, 18, 18, 0, 0, 0, time.Local)
	if key, _, ok := matchTimeTrigger(sc, evening); !ok || key != "daily:18:00" {
		t.Fatalf("18:00 should match daily:18:00, got %q ok=%v", key, ok)
	}
	if !scheduleMatchesNow(sc, evening) {
		t.Fatal("18:00 should fire independently (per-trigger dedup)")
	}
}

// TestScheduleMatchesNow_MultiTriggerOR 混合触发器任一命中即触发，
// 且各触发器独立消费互不遮挡。
func TestScheduleMatchesNow_MultiTriggerOR(t *testing.T) {
	InitScheduleLogStore(t.TempDir())
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	r := now.Add(-time.Minute)
	sc := Schedule{ID: 1, Enabled: true, Triggers: []Trigger{
		{Type: ScheduleTypeDaily, Time: "23:59"},
		{Type: ScheduleTypeOneTime, RunAt: &r},
	}}
	if !scheduleMatchesNow(sc, now) {
		t.Fatal("one_time in window should fire")
	}
	// 消费 one_time 后，daily 23:59 仍独立可用
	GetScheduleLogStore().Add(ScheduleLog{ScheduleID: 1, Time: now, Result: ScheduleResultSuccess, TriggerKey: "one_time:" + r.Format(time.RFC3339)})
	if scheduleMatchesNow(sc, now) {
		t.Fatal("one_time consumed should not refire")
	}
	midnight := time.Date(2026, 9, 18, 23, 59, 0, 0, time.Local)
	if !scheduleMatchesNow(sc, midnight) {
		t.Fatal("daily 23:59 should still fire independently")
	}
}
// TestTriggersFor_Legacy 上层便捷字段 → 触发器清单（时间型迁移；Triggers 显式
// 原样返回；旧 sleep/idle 事件型不再构造——直接抛弃）。
func TestTriggersFor_Legacy(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	// 时间型
	sc := makeSchedule(1, ScheduleTypeDaily, now)
	trs := triggersFor(sc)
	if len(trs) != 1 || trs[0].Type != ScheduleTypeDaily || trs[0].Time != "10:00" {
		t.Fatalf("daily migration wrong: %+v", trs)
	}
	// 旧 sleep/idle 事件型（scheduleType=trigger）→ 不再构造，返回空
	tr := Schedule{ID: 2, Category: ScheduleCategoryDisk, ScheduleType: ScheduleTypeTrigger,
		DiskGroups: []int{1, 3}, TriggerRule: &TriggerRule{AllDay: true, ThresholdMin: 10}}
	if trs = triggersFor(tr); len(trs) != 0 {
		t.Fatalf("legacy trigger config should be discarded, got %+v", trs)
	}
	// Triggers 显式 → 原样返回
	multi := Schedule{ID: 3, Triggers: []Trigger{
		{Type: ScheduleTypeDaily, Time: "09:00"},
		{Type: TriggerTypeLog, LogEventPath: "/var/log/syslog", LogEventRegex: "DiskSpindown"},
	}}
	if trs = triggersFor(multi); len(trs) != 2 || trs[1].Type != TriggerTypeLog {
		t.Fatalf("explicit triggers should be returned as-is: %+v", trs)
	}
}

// TestScheduleLogTriggerType 触发类型推导（修订版「触发类型」筛选）。
func TestScheduleLogTriggerType(t *testing.T) {
	cases := []struct {
		key, schedType string
		srcID          int
		want           string
	}{
		{"manual", "", 0, ScheduleLogTriggerManual},
		{"log:logevent:1", "", 0, ScheduleLogTriggerLog},
		{"log:inline:/var/log/syslog|hdparm.*sleeping", "", 0, ScheduleLogTriggerLog}, // 内联日志事件（第⑧阶段）
		{"sleep", "trigger", 0, ScheduleLogTriggerLog},
		{"monitor:monitor:1", "", 0, ScheduleLogTriggerMonitor},
		{"monitor:prebuilt:idle", "", 0, ScheduleLogTriggerMonitor}, // 预制监控事件
		{"idle", "trigger", 0, ScheduleLogTriggerMonitor},
		{"daily:18:00", "daily", 0, ScheduleLogTriggerTime},
		{"one_time:2026-09-19T10:00:00+08:00", "one_time", 0, ScheduleLogTriggerTime},
		{"weekly:2", "weekly", 0, ScheduleLogTriggerTime},
		{"monthly:15", "monthly", 0, ScheduleLogTriggerTime},
		{"", "one_time", 0, ScheduleLogTriggerTime}, // 旧日志兜底：按 scheduleType
		{"", "daily", 0, ScheduleLogTriggerTime},
		{"", "trigger", 0, ""}, // 旧事件型存量无法区分
		{"", "", 3, ""},        // 串联投递不属于任何触发类型
	}
	for _, c := range cases {
		got := scheduleLogTriggerType(c.key, c.schedType, c.srcID)
		if got != c.want {
			t.Errorf("scheduleLogTriggerType(%q,%q,%d) = %q, want %q", c.key, c.schedType, c.srcID, got, c.want)
		}
	}
}

// ===== 架构已改：资源独立存储，不再在 Settings 里 =====
// 之前的 TestSaveSettings_PartialPreservesSchedules（验证 Schedules 不会被
// Settings 整对象替换清掉）已因资源独立存储而无需存在——资源持久化路径与
// Settings 完全解耦，saveSettings handler 不可能影响到它们。

// TestManualRunOneTime_TaskNotDeleted 一次性任务手动执行后只是被停用，不会被删除。
func TestManualRunOneTime_TaskNotDeleted(t *testing.T) {
	store, sched, _, _ := newTestScheduler(t, true)
	ot := Schedule{
		Name:         "一次性联调",
		ScheduleType: ScheduleTypeOneTime,
		RunAt:        ptr(time.Now().Add(-time.Minute)),
		Enabled:      true,
		Executors:    []Executor{{Type: ExecutorFanControl, FanID: 1, FanManual: true, FanPercent: 60}},
	}
	store.AddSchedule(ot)

	// 手动触发
	msg, ok := sched.TriggerManual(1)
	if !ok {
		t.Fatalf("manual trigger failed: %s", msg)
	}

	// 断言：任务仍然存在于 store 中，只是 enabled=false
	after := store.GetSchedules()
	if len(after) != 1 {
		t.Fatalf("一次性任务手动执行后被删除！got %d, want 1", len(after))
	}
	if after[0].Enabled {
		t.Fatalf("一次性任务手动执行后应被停用")
	}
}

func ptr[T any](v T) *T { return &v }
