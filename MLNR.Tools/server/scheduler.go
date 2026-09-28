package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"mlnr/logger"
)

// ============================================================
// 定时计划调度引擎（M2 时间任务 + M6 动作清单/串联 + M8 多触发器）
//
// 职责：
//   - 周期轮询匹配到期任务（one_time / daily / weekly / monthly）
//   - 事件型触发器（log 日志事件 / monitor 监控事件，M10/M11）：按生效规则
//     （全天/时间范围/周/月日）允许 + 事件引擎命中即执行；
//     旧格式 sleep/idle 硬盘状态触发器（M3）已由 log/monitor 预制事件替代，
//     旧配置直接抛弃（不再迁移/触发）
//   - 多触发器（M8）：Triggers 清单可多张，时间型任一命中 + 事件型任一触发即执行
//   - 待执行队列（M6 任务串联）：control_task(任务动作 run) 把目标任务投递到队列
//     （可带遵循开关），到期串行执行；队列防重入 + 链深度上限防级联风暴
//   - 执行器清单（M9 修订：原动作改名执行器）：任务执行一组执行器（Executors）；
//     类型：风扇控制 / 硬盘组控制 / 控制任务 / 执行脚本；每个执行器独立延迟
//     （DelaySec）与失败重试（RetryEnabled），成功后/失败后执行任务（执行器级串联）；
//     空触发器任务仅支持手动触发（TriggerManual）或被其他任务调用执行
//   - 串行执行命中任务（同一轮多个任务顺序执行，避免并发操作冲突）
//
// 防重复触发（M8 per-trigger）：时间型触发器以「任务+触发器摘要」的执行日志为
// 唯一事实源——该触发器自 since（one_time=RunAt，其余=当天 00:00）起已有日志
// （成功/失败均计入）即视为已消费，重启后不会重复执行；多张同类型触发器各自
// 独立消费互不遮挡。事件型触发器的防重复由事件引擎（M10/M11）自身状态管理。
// 一次性任务执行后自动禁用。
// ============================================================

// scheduleTickInterval 调度检查周期（对齐 thermal 自动调速周期，足够精细）
const scheduleTickInterval = 10 * time.Second

// oneTimeGraceWindow 一次性任务宽容窗口：RunAt 后超过该窗口视为错过，不补执行
const oneTimeGraceWindow = 2 * time.Minute

// loopNextRun 循环触发器下次执行时刻（key=loop:<schedule_id>:<trigger_idx>）。
// 调度循环是单 goroutine 串行执行，无需加锁。
var loopNextRun = make(map[string]time.Time)

// scheduleStore 调度器对设置存储的最小依赖（*Store 天然满足）
type scheduleStore interface {
	GetSettings() Settings
	SaveSettings(v Settings)
	// 资源（不按 MAC 隔离，独立持久化）
	GetSchedules() []Schedule
	SaveSchedules([]Schedule)
	GetMonitorScripts() []MonitorScript
	GetMonitorScriptCode(id int) (string, error)
	GetExecScripts() []ExecScript
	GetExecScriptCode(id int) (string, error)
	GetLogEvents() []LogEvent
}

// scheduleSession 调度器对会话的最小依赖（*BLEManager 天然满足）
type scheduleSession interface {
	// IsEncryptedSession 设备是否处于加密会话（WORK_RUN）——硬盘任务前置检查
	IsEncryptedSession() bool
	// SetSettings 同步 BLE 内存设置（thermal 自动调速读取该副本）
	SetSettings(s Settings)
	// SetFanSpeed 直接下发风扇转速到固件（手动模式 scheduler 主动调用，自动模式 thermal 调用）
	SetFanSpeed(fanID, speed int) (CommandResult, error)
}

// scheduleDiskExecutor 硬盘执行器接口（*DiskManager 天然满足）
type scheduleDiskExecutor interface {
	PowerOn(groupID int) (CommandResult, error)
	PowerOff(groupID int, action string) (CommandResult, error)
	ForcePowerOff(groupID int) (CommandResult, error)
}

// pendingRun 待执行队列条目（M6 任务串联：由 run_task 动作投递）。
type pendingRun struct {
	ScheduleID      int
	RunAt           time.Time // 计划执行时间（投递时刻 + DelaySec）
	SourceID        int       // 来源任务 ID（0 = 非串联）
	SourceName      string    // 来源任务名快照
	SourceResult    string    // 来源任务执行结果（success/failed，投递时确定）
	RespectEnabled  bool      // 遵循目标任务启用状态（被禁用则不执行）
	RespectTimeRule bool      // 遵循目标任务时间规则（时间窗/周期命中才执行）
	Depth           int       // 串联链深度（常规触发=0；每次投递+1）
}

// pendingExecutorRun 延迟执行器条目（M9：执行器独立延迟 + 失败重试调度）。
type pendingExecutorRun struct {
	ScheduleID int
	Executor   Executor
	RunAt      time.Time // 计划执行时刻（投递时刻 + DelaySec / 重试间隔）
	SourceID   int       // 来源任务 ID（0 = 常规触发）
	SourceName string
	Depth      int
	Attempt    int // 已尝试次数（首次=1；重试=2..）；0=延迟主体尚未执行
}

// TaskRunner 硬盘组上电后/下电前任务触发接口（由 DiskManager 依赖注入）。
// 由 *Scheduler 实现；DiskManager 仅依赖本接口，便于测试注入 fake。
type TaskRunner interface {
	// TriggerManual 同步触发任务：等待任务主执行器全部执行完成并返回（消息, 是否成功）。
	TriggerManual(id int) (string, bool)
	// TriggerManualAsync 异步触发任务：投递调度循环后立即返回，不等待执行结果。
	TriggerManualAsync(id int) (string, bool)
}

// Scheduler 定时计划调度引擎。
type Scheduler struct {
	store scheduleStore
	ble   scheduleSession
	disk  scheduleDiskExecutor

	stopCh  chan struct{}
	running bool // 执行中标志（单 goroutine 内防 tick 重叠重入）
	// manualCh 异步手动触发投递通道（上电后任务等：TriggerManualAsync 投递，loop 消费执行）
	manualCh chan int
	// pending 待执行队列（仅 loop goroutine 内访问，无需锁）
	pending []pendingRun
	// delayedExecutors 延迟/重试执行器队列（M9，仅 loop goroutine 内访问）
	delayedExecutors []pendingExecutorRun
	// now 时钟注入（测试用；生产为 time.Now）。仅 run_task 投递延迟计算使用。
	now func() time.Time

	// 事件引擎回调（M10 日志事件 / M11 监控事件；nil 表示引擎未接入，事件不触发）
	logEventFired     func(sc Schedule, tr Trigger, now time.Time) bool
	monitorEventFired func(sc Schedule, tr Trigger, now time.Time) bool
	// 事件引擎状态（M10/M11，仅 loop goroutine 访问）：
	// logTails 日志尾随状态（key=log:<事件key>）；monitorStates 监控采样状态（key=monitor:<事件key>）
	logTails      map[string]*logTailState
	monitorStates map[string]*monitorState
	// runScript 监控脚本执行器（默认 runScriptCode；测试注入假执行器）
	runScript func(code string, typ string, args string, timeout time.Duration) (string, error)
}

// NewScheduler 创建调度引擎。
func NewScheduler(store *Store, ble scheduleSession, disk scheduleDiskExecutor) *Scheduler {
	s := &Scheduler{
		store:         store,
		ble:           ble,
		disk:          disk,
		stopCh:        make(chan struct{}),
		manualCh:      make(chan int, 8),
		now:           time.Now,
		logTails:      make(map[string]*logTailState),
		monitorStates: make(map[string]*monitorState),
	}
	// 挂接事件引擎（M10 日志事件 / M11 监控事件）
	s.logEventFired = s.onLogEventFired
	s.monitorEventFired = s.onMonitorEventFired
	s.runScript = runScriptCode
	return s
}

// Start 启动调度循环（后台 goroutine）。
func (s *Scheduler) Start() {
	go s.loop()
	logger.Info("schedule", "scheduler started (tick %v)", scheduleTickInterval)
}

// Stop 停止调度循环。不阻塞等待正在执行的任务完成（进程关闭场景）。
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

func (s *Scheduler) loop() {
	ticker := time.NewTicker(scheduleTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case now := <-ticker.C:
			s.checkAndRun(now)
		case id := <-s.manualCh:
			s.executeManualAsync(id)
		}
	}
}

// checkAndRun 检查到期任务并串行执行（同轮多任务按列表顺序逐个执行）。
// 顺序：先消费到期的待执行队列（串联投递），再匹配常规触发（时间型任一命中
// 且未消费 → 执行；事件型逐个检查触发）。
// 执行期间 running 置位，下一 tick 直接跳过，避免与长任务（如硬盘上下线）重叠。
func (s *Scheduler) checkAndRun(now time.Time) {
	if s.running {
		return
	}
	s.running = true
	defer func() { s.running = false }()

	// 1. 待执行队列（任务串联）+ 延迟/重试执行器
	s.runPending(now)
	s.runDelayedExecutors(now)

	// 2. 常规匹配（时间型 + 事件型触发器）
	schedules := s.store.GetSchedules()

	// 2.0 事件引擎状态清理：仅保留启用任务的事件触发器状态
	// （任务禁用/删除/触发器移除后丢弃其尾随与采样状态；重新启用按规则
	//  以启用时刻为起点重新初始化——日志跳末尾、监控重新开始持续计时）
	s.pruneEventStates(schedules)

	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		if scheduleMatchesNow(sc, now) {
			// 命中时间型触发器（per-trigger 防重复已消费判定通过）
			key, _, _ := matchTimeTrigger(sc, now)
			s.executeTriggered(sc, key)
			continue
		}
		if hasEventTrigger(sc) {
			s.checkTrigger(sc, now)
		}
	}
}

// ===== 触发器（M8 多触发器清单） =====

// triggersFor 返回任务触发器清单；Triggers 为空时从上层便捷字段构造
// （存量任务 / 直接构造的 Schedule / 旧前端兼容；仅时间型）。
// 旧 sleep/idle 事件型（scheduleType=trigger）不再构造——旧配置直接抛弃。
func triggersFor(sc Schedule) []Trigger {
	if len(sc.Triggers) > 0 {
		return sc.Triggers
	}
	switch sc.ScheduleType {
	case ScheduleTypeOneTime:
		return []Trigger{{Type: ScheduleTypeOneTime, RunAt: sc.RunAt}}
	case ScheduleTypeDaily:
		return []Trigger{{Type: ScheduleTypeDaily, Time: sc.Time}}
	case ScheduleTypeWeekly:
		return []Trigger{{Type: ScheduleTypeWeekly, Time: sc.Time, Weekdays: sc.Weekdays}}
	case ScheduleTypeMonthly:
		return []Trigger{{Type: ScheduleTypeMonthly, Time: sc.Time, MonthDays: sc.MonthDays}}
	}
	return nil
}

// matchTimeTrigger 返回首个命中的时间型触发器（key=触发器摘要用于 per-trigger
// 防重复与日志，since=防重复基准：one_time 用 RunAt，其余用当天 00:00）。
// 兼容新旧格式：旧触发器（one_time/daily/weekly/monthly）先迁移（幂等）。
func matchTimeTrigger(sc Schedule, now time.Time) (key string, since time.Time, ok bool) {
	for i, tr := range triggersFor(sc) {
		migrateTrigger(&tr)
		if tr.Type != TriggerTypeTime {
			continue
		}
		switch tr.Period {
		case TriggerPeriodOnce:
			if tr.RunAt != nil && !now.Before(*tr.RunAt) && now.Sub(*tr.RunAt) <= oneTimeGraceWindow {
				return "one_time:" + tr.RunAt.Format(time.RFC3339), *tr.RunAt, true
			}
		case TriggerPeriodDaily:
			if validTimeHHMM(tr.Time) && now.Format("15:04") == tr.Time {
				return "daily:" + tr.Time, startOfDay(now), true
			}
		case TriggerPeriodWeekly:
			if validTimeHHMM(tr.Time) && now.Format("15:04") == tr.Time &&
				containsInt(tr.Weekdays, goWeekdayToISO(now)) {
				return "weekly:" + tr.Time, startOfDay(now), true
			}
		case TriggerPeriodMonthly:
			if validTimeHHMM(tr.Time) && now.Format("15:04") == tr.Time &&
				containsInt(tr.MonthDays, now.Day()) {
				return "monthly:" + tr.Time, startOfDay(now), true
			}
		case TriggerPeriodLoop:
			if !validHHMMSS(tr.LoopInterval) {
				continue
			}
			interval, _ := parseHHMMSS(tr.LoopInterval)
			if interval <= 0 {
				continue
			}
			key := fmt.Sprintf("loop:%d:%d", sc.ID, i)
			next, exists := loopNextRun[key]
			if !exists {
				// 首次初始化：从 now 起算一个间隔（符合用户示例语义：10:05 启动 → 10:15 首次执行）
				next = now.Add(interval)
				loopNextRun[key] = next
			}
			if !now.Before(next) {
				// 命中！推进到下次（跳过已错过的多轮）
				for !now.Before(loopNextRun[key]) {
					loopNextRun[key] = loopNextRun[key].Add(interval)
				}
				return key, next, true
			}
		}
	}
	return "", time.Time{}, false
}

// hasEventTrigger 任务是否含事件型触发器（日志事件 / 监控事件）。
func hasEventTrigger(sc Schedule) bool {
	for _, tr := range triggersFor(sc) {
		migrateTrigger(&tr)
		if tr.Type == TriggerTypeLog || tr.Type == TriggerTypeMonitor {
			return true
		}
	}
	return false
}

// scheduleAnyTriggerMatches 任一触发器在当前时刻命中（时间型命中或事件型规则允许）。
// 供串联投递的"遵循时间规则"（respectTimeRule）判定。
// 日志/监控事件触发器无日期规则（等价"不受限制"），仅校验保留的旧规则字段。
func scheduleAnyTriggerMatches(sc Schedule, now time.Time) bool {
	if _, _, ok := matchTimeTrigger(sc, now); ok {
		return true
	}
	for _, tr := range triggersFor(sc) {
		migrateTrigger(&tr)
		if (tr.Type == TriggerTypeLog || tr.Type == TriggerTypeMonitor) && triggerRuleAllowsTr(tr, now) {
			return true
		}
	}
	return false
}

// triggerRuleAllows 触发任务生效规则校验（兼容旧签名，内部转 Trigger）。
func triggerRuleAllows(rule *TriggerRule, now time.Time) bool {
	if rule == nil {
		return false
	}
	return triggerRuleAllowsTr(Trigger{
		AllDay:        rule.AllDay,
		TimeRange:     rule.TimeRange,
		WeekLimit:     rule.WeekLimit,
		MonthDayLimit: rule.MonthDayLimit,
	}, now)
}

// triggerRuleAllowsTr 事件型触发器生效规则校验（纯逻辑，可单测）：
//   - AllDay=true → 时间范围不限制；否则 now 的 HH:mm 须在 TimeRange 内（含边界）；
//   - WeekLimit 非空 → 仅限列表内星期（1=周一 ~ 7=周日）；
//   - MonthDayLimit 非空 → 仅限列表内日期（1~31）。
func triggerRuleAllowsTr(tr Trigger, now time.Time) bool {
	// 无任何生效规则字段 → 不受限制（新格式 log/monitor 触发器无日期规则）
	if tr.TimeRange == "" && len(tr.WeekLimit) == 0 && len(tr.MonthDayLimit) == 0 {
		return true
	}
	if !tr.AllDay {
		hm := now.Format("15:04")
		parts := strings.Split(tr.TimeRange, "-")
		if len(parts) != 2 || hm < parts[0] || hm > parts[1] {
			return false
		}
	}
	if len(tr.WeekLimit) > 0 && !containsInt(tr.WeekLimit, goWeekdayToISO(now)) {
		return false
	}
	if len(tr.MonthDayLimit) > 0 && !containsInt(tr.MonthDayLimit, now.Day()) {
		return false
	}
	return true
}

// checkTrigger 检查单个任务的全部事件型触发器（M8：遍历 Triggers 中的 log/monitor）。
// 旧格式事件触发器（sleep/idle，M3 硬盘状态触发）已由 log/monitor 预制事件替代；
// 旧配置直接抛弃：Type 非 log/monitor 的触发器在此被跳过，不再触发。
func (s *Scheduler) checkTrigger(sc Schedule, now time.Time) {
	for _, tr := range triggersFor(sc) {
		migrateTrigger(&tr)
		switch tr.Type {
		case TriggerTypeLog:
			if s.logEventFired == nil {
				continue // 日志事件引擎未接入（M10）
			}
			if !triggerRuleAllowsTr(tr, now) {
				continue
			}
			if s.logEventFired(sc, tr, now) {
				s.executeTriggered(sc, "log:"+triggerEventKey(tr))
			}
		case TriggerTypeMonitor:
			if s.monitorEventFired == nil {
				continue // 监控事件引擎未接入（M11）
			}
			if !triggerRuleAllowsTr(tr, now) {
				continue
			}
			if s.monitorEventFired(sc, tr, now) {
				s.executeTriggered(sc, "monitor:"+triggerEventKey(tr))
			}
		}
	}
}

// pruneEventStates 清理已停用/删除任务的事件引擎状态（M10/M11）。
func (s *Scheduler) pruneEventStates(schedules []Schedule) {
	activeLog := make(map[string]bool)
	activeMon := make(map[string]bool)
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		for _, tr := range triggersFor(sc) {
			migrateTrigger(&tr)
			switch tr.Type {
			case TriggerTypeLog:
				activeLog["log:"+triggerEventKey(tr)] = true
			case TriggerTypeMonitor:
				activeMon["monitor:"+triggerEventKey(tr)] = true
			}
		}
	}
	for k, st := range s.logTails {
		if !activeLog[k] {
			if st.file != nil {
				st.file.Close()
			}
			delete(s.logTails, k)
		}
	}
	for k := range s.monitorStates {
		if !activeMon[k] {
			delete(s.monitorStates, k)
		}
	}
}

// triggerEventKey 日志/监控事件触发器的去重与日志摘要。
// log 触发器：资源引用以 logevent:<id> 为唯一标识，内联以 path+regex 为唯一标识；
// monitor 以预制标识或资源 ID 为标识。
func triggerEventKey(tr Trigger) string {
	if tr.Type == TriggerTypeLog {
		if tr.LogEventID > 0 {
			return fmt.Sprintf("logevent:%d", tr.LogEventID)
		}
		return "inline:" + tr.LogEventPath + "|" + tr.LogEventRegex
	}
	if tr.MonitorPrebuilt != "" {
		return "prebuilt:" + tr.MonitorPrebuilt
	}
	return fmt.Sprintf("monitor:%d", tr.MonitorScriptID)
}

// ===== 时间匹配 =====

// scheduleTimeMatches 纯时间型触发器匹配（不含 Enabled 与防重复）。
// 供串联投递的"遵循时间规则"与测试使用；多触发器任一命中即 true。
func scheduleTimeMatches(sc Schedule, now time.Time) bool {
	_, _, ok := matchTimeTrigger(sc, now)
	return ok
}

// scheduleMatchesNow 判断任务在 now 时刻是否到期（纯逻辑，可单测）。
// 多触发器（M8）：任一时间型触发器命中 + per-trigger 防重复（该触发器自 since
// 起已有该任务日志 → 已消费不触发）。
func scheduleMatchesNow(sc Schedule, now time.Time) bool {
	if !sc.Enabled {
		return false
	}
	key, since, ok := matchTimeTrigger(sc, now)
	if !ok {
		return false
	}
	return !hasTriggerLogSince(sc.ID, key, since)
}

// ===== 待执行队列（M6 任务串联） =====

// runPending 取出全部到期条目并逐个执行（保持投递顺序）。
func (s *Scheduler) runPending(now time.Time) {
	if len(s.pending) == 0 {
		return
	}
	remain := s.pending[:0]
	var due []pendingRun
	for _, p := range s.pending {
		if !now.Before(p.RunAt) {
			due = append(due, p)
		} else {
			remain = append(remain, p)
		}
	}
	s.pending = remain
	for _, p := range due {
		s.executePending(p, now)
	}
}

// enqueue 投递条目到待执行队列（防重入：同任务已排队则跳过）。
// 返回是否入队成功；失败时 reason 说明（记入来源任务日志）。
func (s *Scheduler) enqueue(p pendingRun) (bool, string) {
	for _, e := range s.pending {
		if e.ScheduleID == p.ScheduleID {
			return false, fmt.Sprintf("任务 #%d 已在待执行队列，跳过重复投递", p.ScheduleID)
		}
	}
	s.pending = append(s.pending, p)
	return true, ""
}

// executePending 执行一条待执行队列条目（串联投递的目标任务）。
// 遵循开关：respectEnabled=目标已停用则跳过；respectTimeRule=时间规则不命中则跳过。
func (s *Scheduler) executePending(p pendingRun, now time.Time) {
	sc := s.findScheduleByID(p.ScheduleID)
	if sc == nil {
		logger.Warn("schedule", "pending run target #%d missing (source #%d)", p.ScheduleID, p.SourceID)
		return
	}
	srcDesc := "外部投递"
	if p.SourceName != "" {
		srcDesc = fmt.Sprintf("任务「%s」", p.SourceName)
	}
	if p.RespectEnabled && !sc.Enabled {
		s.addLog(*sc, ScheduleResultFailed, "被"+srcDesc+"投递但任务已停用，跳过", p.SourceID, p.SourceResult, "")
		logger.Info("schedule", "pending run #%d %q skipped: disabled (respectEnabled)", sc.ID, sc.Name)
		return
	}
	if p.RespectTimeRule && !scheduleAnyTriggerMatches(*sc, now) {
		s.addLog(*sc, ScheduleResultFailed, "被"+srcDesc+"投递但当前不满足目标任务时间规则，跳过", p.SourceID, p.SourceResult, "")
		logger.Info("schedule", "pending run #%d %q skipped: time rule not matched", sc.ID, sc.Name)
		return
	}
	// 正常执行（来源信息随执行日志记录；串联投递不消费任何触发器）
	s.executeWithSource(*sc, p.SourceID, p.SourceName, p.SourceResult, p.Depth, "")
}

// findScheduleByID 按 ID 查找任务副本（nil=不存在）。
func (s *Scheduler) findScheduleByID(id int) *Schedule {
	schedules := s.store.GetSchedules()
	for i := range schedules {
		if schedules[i].ID == id {
			return &schedules[i]
		}
	}
	return nil
}

// ===== 任务执行（M6 动作清单 + 分支 + 串联） =====

// execute 常规触发入口（无触发器消费记录；兼容测试/直接调用）。
func (s *Scheduler) execute(sc Schedule) {
	s.executeWithSource(sc, 0, "", "", 0, "")
}

// executeTriggered 触发器命中入口（记录触发器摘要 key 用于 per-trigger 防重复）。
func (s *Scheduler) executeTriggered(sc Schedule, triggerKey string) {
	s.executeWithSource(sc, 0, "", "", 0, triggerKey)
}

// executeWithSource 串行执行单个任务并写执行日志。
// srcID/srcName/srcResult 非零/非空表示由其他任务串联投递（记录到日志与详情）。
// depth 为当前任务的串联链深度（常规触发=0，投递执行≥1）；triggerKey 为命中的
// 触发器摘要（串联投递为空）。
func (s *Scheduler) executeWithSource(sc Schedule, srcID int, srcName, srcResult string, depth int, triggerKey string) {
	// 执行器清单（Executors 为空时回退旧动作/顶层便捷字段迁移）
	detail, fails := s.executeExecutors(sc, scheduleExecutorsFor(sc), srcID, srcName, depth)

	result := ScheduleResultSuccess
	if len(fails) > 0 {
		result = ScheduleResultFailed
		if detail != "" {
			detail += "；" + strings.Join(fails, "；")
		} else {
			detail = strings.Join(fails, "；")
		}
	}

	// 一次性任务执行后自动禁用（无论成败，视为已消费，避免重复触发）
	if sc.ScheduleType == ScheduleTypeOneTime {
		s.disableOneTime(sc.ID)
	}

	// 回填本任务投递条目的来源结果（control_task run 投递时结果未知，
	// 任务最终结果确定后回填；执行器级成功/失败后投递已携带来源结果，不覆盖）
	for i := range s.pending {
		if s.pending[i].SourceID == sc.ID && s.pending[i].SourceResult == "" {
			s.pending[i].SourceResult = result
		}
	}

	// 日志详情带来源
	logDetail := detail
	if srcID != 0 && srcName != "" {
		logDetail = fmt.Sprintf("由任务「%s」触发；%s", srcName, logDetail)
	}
	s.addLog(sc, result, logDetail, srcID, srcResult, triggerKey)
	logger.Info("schedule", "task #%d %q (%s) -> %s: %s", sc.ID, sc.Name, sc.ScheduleType, result, logDetail)
}

// scheduleExecutorsFor 返回任务要执行的执行器清单；
// Executors 为空时回退旧 Actions 迁移 → 顶层便捷字段（存量任务/直接 API 调用兼容）。
func scheduleExecutorsFor(sc Schedule) []Executor {
	if len(sc.Executors) > 0 {
		return sc.Executors
	}
	if len(sc.Actions) > 0 {
		out := make([]Executor, 0, len(sc.Actions))
		for _, a := range sc.Actions {
			out = append(out, actionToExecutor(a))
		}
		// 旧任务级失败分支：首个 run_task 迁移到首个执行器的 FailureTaskID
		for _, a := range sc.FailureActions {
			if a.Type == ActionRunTask && a.TaskID > 0 {
				out[0].FailureTaskID = a.TaskID
				break
			}
		}
		return out
	}
	if len(sc.DiskGroups) > 0 || len(sc.DiskAction) > 0 {
		out := make([]Executor, 0, len(sc.DiskGroups))
		for _, g := range sc.DiskGroups {
			out = append(out, Executor{Type: ExecutorDiskGroupControl, DiskGroupID: g, DiskAction: sc.DiskAction, ForceOff: sc.ForceOff})
		}
		return out
	}
	if len(sc.FanChannels) > 0 || len(sc.FanMode) > 0 {
		out := make([]Executor, 0, len(sc.FanChannels))
		for _, ch := range sc.FanChannels {
			out = append(out, Executor{Type: ExecutorFanControl, FanID: ch, FanManual: false, FanCurve: sc.FanMode})
		}
		return out
	}
	return nil
}

// executeExecutors 串行执行执行器清单：延迟执行器（DelaySec>0）进入延迟队列，
// 其余立即执行（含失败重试与成功/失败后执行任务投递）。
func (s *Scheduler) executeExecutors(sc Schedule, execs []Executor, srcID int, srcName string, depth int) (string, []string) {
	var (
		detailParts []string
		fails       []string
	)
	for _, e := range execs {
		if e.DelaySec > 0 {
			s.delayedExecutors = append(s.delayedExecutors, pendingExecutorRun{
				ScheduleID: sc.ID,
				Executor:   e,
				RunAt:      s.now().Add(time.Duration(e.DelaySec) * time.Second),
				SourceID:   srcID,
				SourceName: srcName,
				Depth:      depth,
				Attempt:    0,
			})
			detailParts = append(detailParts, fmt.Sprintf("%s延迟 %d 秒", executorTypeLabel(e.Type), e.DelaySec))
			continue
		}
		d, f := s.runExecutorAttempt(sc, e, srcID, srcName, depth, 1)
		detailParts = append(detailParts, d)
		fails = append(fails, f...)
	}
	return strings.Join(detailParts, "；"), fails
}

// runExecutorAttempt 执行单个执行器（attempt 从 1 开始；重试递增）。
// 成功 → 记录详情（含重试次数）+ 投递成功后执行任务；失败 → 入重试队列或
// 超过最大重试次数判定失败并投递失败后执行任务。
func (s *Scheduler) runExecutorAttempt(sc Schedule, e Executor, srcID int, srcName string, depth int, attempt int) (string, []string) {
	detail, fails := s.execOne(sc, e, srcID, srcName, depth)
	if len(fails) == 0 {
		if attempt > 1 {
			detail = fmt.Sprintf("%s（重复执行 %d 次后成功）", detail, attempt)
		}
		if e.SuccessTaskID > 0 {
			if msg := s.dispatchFollowTask(e.SuccessTaskID, sc, ScheduleResultSuccess, depth); msg != "" {
				fails = append(fails, msg)
			}
		}
		return detail, fails
	}
	maxAttempts := 1
	if e.RetryEnabled {
		maxAttempts = 1 + e.MaxRetries
	}
	if attempt < maxAttempts {
		interval := e.RetryIntervalSec
		if interval < 1 {
			interval = 10
		}
		s.delayedExecutors = append(s.delayedExecutors, pendingExecutorRun{
			ScheduleID: sc.ID,
			Executor:   e,
			RunAt:      s.now().Add(time.Duration(interval) * time.Second),
			SourceID:   srcID,
			SourceName: srcName,
			Depth:      depth,
			Attempt:    attempt + 1,
		})
		return "", []string{fmt.Sprintf("%s第 %d 次失败，%d 秒后重试", executorTypeLabel(e.Type), attempt, interval)}
	}
	if e.FailureTaskID > 0 {
		if msg := s.dispatchFollowTask(e.FailureTaskID, sc, ScheduleResultFailed, depth); msg != "" {
			fails = append(fails, msg)
		}
	}
	return "", fails
}

// runDelayedExecutors 消费到期延迟/重试执行器（串行，保持投递顺序）。
func (s *Scheduler) runDelayedExecutors(now time.Time) {
	if len(s.delayedExecutors) == 0 {
		return
	}
	remain := s.delayedExecutors[:0]
	var due []pendingExecutorRun
	for _, p := range s.delayedExecutors {
		if !p.RunAt.After(now) {
			due = append(due, p)
		} else {
			remain = append(remain, p)
		}
	}
	s.delayedExecutors = remain
	for _, p := range due {
		sc := s.findScheduleByID(p.ScheduleID)
		if sc == nil {
			logger.Warn("schedule", "delayed executor target #%d missing", p.ScheduleID)
			continue
		}
		d, f := s.runExecutorAttempt(*sc, p.Executor, p.SourceID, p.SourceName, p.Depth, p.Attempt)
		if len(f) > 0 {
			logger.Warn("schedule", "delayed executor #%d %q failed: %s", sc.ID, sc.Name, strings.Join(f, "；"))
		} else if d != "" {
			logger.Info("schedule", "delayed executor #%d %q done: %s", sc.ID, sc.Name, d)
		}
	}
}

// dispatchFollowTask 执行器成功后/失败后执行任务：投递目标任务到待执行队列
// （无遵循开关，直接执行；链深度校验）。
func (s *Scheduler) dispatchFollowTask(taskID int, sc Schedule, srcResult string, depth int) string {
	if depth >= maxScheduleChainDepth {
		return fmt.Sprintf("串联链深度超过 %d 级，拒绝投递", maxScheduleChainDepth)
	}
	target := s.findScheduleByID(taskID)
	if target == nil {
		return fmt.Sprintf("执行后任务 #%d 不存在", taskID)
	}
	p := &pendingRun{
		ScheduleID:   taskID,
		RunAt:        s.now(),
		SourceID:     sc.ID,
		SourceName:   sc.Name,
		SourceResult: srcResult,
		Depth:        depth + 1,
	}
	if ok, reason := s.enqueue(*p); !ok {
		return reason
	}
	return ""
}

// execFanControl 执行风扇控制执行器：
//
//	启用/禁用风扇（FanEnabled 非 nil 时）；自动/手动控制（FanManual）：
//	手动 → 设置转速 FanPercent（**立即下发**，不依赖 thermal）；
//	自动 → 切换曲线 FanCurve（空=不切换，thermal 下一周期生效）。
func (s *Scheduler) execFanControl(e Executor) (string, []string) {
	set := s.store.GetSettings()
	for i := range set.Fans {
		if set.Fans[i].ID != e.FanID {
			continue
		}
		needSave := false
		parts := []string{fmt.Sprintf("FAN%d", e.FanID)}
		if e.FanEnabled != nil && set.Fans[i].Enabled != *e.FanEnabled {
			set.Fans[i].Enabled = *e.FanEnabled
			needSave = true
			if *e.FanEnabled {
				parts = append(parts, "启用")
			} else {
				parts = append(parts, "禁用")
			}
		}
		if e.FanManual {
			if set.Fans[i].Mode != ModeManual || set.Fans[i].TargetSpd != e.FanPercent {
				set.Fans[i].Mode = ModeManual
				set.Fans[i].TargetSpd = e.FanPercent
				needSave = true
			}
			parts = append(parts, fmt.Sprintf("手动 %d%%", e.FanPercent))
			// 立即下发手动转速（thermal 仅在 Mode=Auto 时下发，手动模式没人兜底）
			if s.ble != nil {
				if _, err := s.ble.SetFanSpeed(e.FanID, e.FanPercent); err != nil {
					logger.Warn("schedule", "execFanControl setFanSpeed fan%d %d%% failed: %v", e.FanID, e.FanPercent, err)
				}
			}
		} else if e.FanCurve != "" {
			if set.Fans[i].Mode != ModeAuto || set.Fans[i].ActiveCurve != e.FanCurve {
				set.Fans[i].Mode = ModeAuto
				set.Fans[i].ActiveCurve = e.FanCurve
				needSave = true
			}
			parts = append(parts, "曲线 "+curveDisplayName(e.FanCurve))
		}
		if needSave {
			s.store.SaveSettings(set)
			if s.ble != nil {
				s.ble.SetSettings(set)
			}
		}
		return strings.Join(parts, " "), nil
	}
	return "", []string{fmt.Sprintf("FAN%d 通道不存在", e.FanID)}
}

// execDiskGroupControl 执行硬盘组控制执行器（上线/下线）。
// 下线三级（M9 修订）：正常下线 →（KillOccupied）kill-9 占用进程后重试 →（ForceOff）
// 强制断电。以最终硬盘组状态（SW 断电成功）为下线成功判据（现有 PowerOff 内联
// verifySwitchLevel）。勾选「自动终止占用/强制下线」且实际执行时日志输出警告。
func (s *Scheduler) execDiskGroupControl(e Executor) (string, []string) {
	if s.ble == nil || !s.ble.IsEncryptedSession() {
		return "", []string{fmt.Sprintf("组%d：设备离线，任务跳过", e.DiskGroupID)}
	}
	switch e.DiskAction {
	case DiskActionOnline:
		res, err := s.disk.PowerOn(e.DiskGroupID)
		if err != nil || !res.OK {
			return "", []string{fmt.Sprintf("组%d 上线失败：%s", e.DiskGroupID, resMessage(res, err))}
		}
		return fmt.Sprintf("组%d 上线", e.DiskGroupID), nil
	case DiskActionOffline:
		warnings := []string{}
		if e.KillOccupied {
			warnings = append(warnings, "勾选自动终止占用")
		}
		if e.ForceOff {
			warnings = append(warnings, "勾选强制下线")
		}
		// 第一级：正常下线（force 卸载）
		res, err := s.disk.PowerOff(e.DiskGroupID, "force")
		if err == nil && res.OK {
			return strings.Join(append([]string{fmt.Sprintf("组%d 下线", e.DiskGroupID)}, warnings...), "；"), nil
		}
		lastMsg := resMessage(res, err)
		// 第二级：kill-9 占用进程后重试
		if e.KillOccupied {
			if kerr := s.killDiskOccupiers(e.DiskGroupID); kerr != nil {
				lastMsg = "终止占用失败：" + kerr.Error()
			} else {
				res2, err2 := s.disk.PowerOff(e.DiskGroupID, "force")
				if err2 == nil && res2.OK {
					return strings.Join(append([]string{fmt.Sprintf("组%d 下线（已终止占用进程）", e.DiskGroupID)}, warnings...), "；"), nil
				}
				lastMsg = resMessage(res2, err2)
			}
		}
		// 第三级：强制断电
		if e.ForceOff {
			res3, err3 := s.disk.ForcePowerOff(e.DiskGroupID)
			if err3 == nil && res3.OK {
				return strings.Join(append([]string{fmt.Sprintf("组%d 强制断电下线", e.DiskGroupID)}, warnings...), "；"), nil
			}
			lastMsg = resMessage(res3, err3)
		}
		return "", []string{fmt.Sprintf("组%d 下线失败：%s", e.DiskGroupID, lastMsg)}
	}
	return "", []string{"硬盘组控制动作无效"}
}

// killDiskOccupiers kill-9 终止占用硬盘组挂载点的进程（Linux fuser；非 Linux 返回错误）。
func (s *Scheduler) killDiskOccupiers(groupID int) error {
	set := s.store.GetSettings()
	for _, g := range set.DiskGroups {
		if g.ID != groupID {
			continue
		}
		for _, disk := range g.Disks {
			for _, m := range disk.Mounts {
				if m.MountPoint == "" {
					continue
				}
				cmd := exec.Command("fuser", "-km", m.MountPoint)
				if out, err := cmd.CombinedOutput(); err != nil {
					logger.Warn("disk", "kill occupiers %s: %s (%v)", m.MountPoint, strings.TrimSpace(string(out)), err)
				}
			}
		}
		return nil
	}
	return fmt.Errorf("硬盘组 #%d 不存在", groupID)
}

// execControlTask 执行控制任务执行器：启用/禁用目标任务，或投递目标任务到待执行队列。
// run 操作带两遵循开关（RespectEnabled/RespectTimeRule，默认关闭；同时开启时
// 须同时满足才执行）；父任务手动触发时同样生效（由 pendingRun 语义保证）。
func (s *Scheduler) execControlTask(sc Schedule, e Executor, srcID int, srcName string, depth int) (string, []string) {
	switch e.TaskAction {
	case TaskActionEnable, TaskActionDisable:
		return s.execSetTaskEnabledExecutor(e, e.TaskAction == TaskActionEnable)
	case TaskActionRun:
		if e.TaskID <= 0 {
			return "", []string{"控制任务执行器缺少目标任务"}
		}
		if srcID == 0 {
			srcID, srcName = sc.ID, sc.Name
		}
		if depth >= maxScheduleChainDepth {
			return "", []string{fmt.Sprintf("串联链深度超过 %d 级，拒绝投递", maxScheduleChainDepth)}
		}
		target := s.findScheduleByID(e.TaskID)
		if target == nil {
			return "", []string{fmt.Sprintf("目标任务 #%d 不存在", e.TaskID)}
		}
		p := &pendingRun{
			ScheduleID:      e.TaskID,
			RunAt:           s.now(),
			SourceID:        srcID,
			SourceName:      srcName,
			RespectEnabled:  e.RespectEnabled,
			RespectTimeRule: e.RespectTimeRule,
			Depth:           depth + 1,
		}
		if ok, reason := s.enqueue(*p); !ok {
			return "", []string{reason}
		}
		return fmt.Sprintf("投递任务「%s」", target.Name), nil
	}
	return "", []string{"控制任务操作无效"}
}

// execSetTaskEnabledExecutor 执行启用/禁用目标任务（双写 store + ble）。
func (s *Scheduler) execSetTaskEnabledExecutor(e Executor, enable bool) (string, []string) {
	if e.TaskID <= 0 {
		return "", []string{"控制任务执行器缺少目标任务"}
	}
	schedules := s.store.GetSchedules()
	for i := range schedules {
		if schedules[i].ID != e.TaskID {
			continue
		}
		verb := "启用"
		if !enable {
			verb = "停用"
		}
		if schedules[i].Enabled == enable {
			return fmt.Sprintf("任务「%s」已%s", schedules[i].Name, verb), nil
		}
		schedules[i].Enabled = enable
		s.store.SaveSchedules(schedules)
		return fmt.Sprintf("已%s任务「%s」", verb, schedules[i].Name), nil
	}
	return "", []string{fmt.Sprintf("目标任务 #%d 不存在", e.TaskID)}
}

// execScriptExecutor 执行执行脚本执行器（引用执行脚本资源或内联代码）。
// 支持 sh / python：引用资源按资源保存的脚本类型执行；内联代码按 shebang 自动识别。
// 超时 30 秒；退出码 0 为成功，输出采集到日志。
// 参数：执行器 ScriptArgs 作为位置参数传入（sh 用 $1 $2 …，python 用 sys.argv[1:]）；
// 未显式传参时回退资源默认参数 Args。
func (s *Scheduler) execScriptExecutor(e Executor) (string, []string) {
	code := e.ScriptCode
	typ := e.ScriptType // 内联代码：显式类型优先（空=按 shebang 自动识别）
	args := e.ScriptArgs
	if e.ScriptID > 0 {
		var meta *ExecScript
		list := s.store.GetExecScripts()
		for i := range list {
			if list[i].ID == e.ScriptID {
				meta = &list[i]
				break
			}
		}
		if meta != nil {
			c, err := s.store.GetExecScriptCode(e.ScriptID)
			if err != nil {
				return "", []string{fmt.Sprintf("读取执行脚本 #%d 失败：%v", e.ScriptID, err)}
			}
			code = c
			typ = meta.ScriptType
			if strings.TrimSpace(args) == "" {
				args = meta.Args
			}
		}
		if meta == nil && strings.TrimSpace(code) == "" {
			return "", []string{fmt.Sprintf("执行脚本 #%d 不存在", e.ScriptID)}
		}
	}
	if strings.TrimSpace(code) == "" {
		return "", []string{"执行脚本内容为空"}
	}
	out, err := runScriptCode(code, typ, args, 30*time.Second)
	if err != nil {
		return "", []string{fmt.Sprintf("脚本执行失败：%s", err)}
	}
	return fmt.Sprintf("执行脚本成功：%s", strings.TrimSpace(out)), nil
}

// TriggerManual 手动触发任务：全部执行器执行（不消费触发器；执行器内
// control_task 的两遵循开关同样生效）。返回（错误消息, 是否成功）。
func (s *Scheduler) TriggerManual(id int) (string, bool) {
	sc := s.findScheduleByID(id)
	if sc == nil {
		return "任务不存在", false
	}
	logger.Info("schedule", "manual trigger task #%d %q", sc.ID, sc.Name)
	s.executeWithSource(*sc, 0, "", "", 0, "manual")
	return "", true
}

// TriggerManualAsync 异步触发任务（上电后任务等"触发后不再介入"场景）：
// 校验任务存在后投递调度循环执行，立即返回，不等待执行结果。
// 返回（错误消息, 是否成功）；任务丢失/引擎停止为失败。
func (s *Scheduler) TriggerManualAsync(id int) (string, bool) {
	if s.findScheduleByID(id) == nil {
		return "任务不存在", false
	}
	select {
	case s.manualCh <- id:
		logger.Info("schedule", "manual async trigger task #%d queued", id)
		return "", true
	case <-s.stopCh:
		return "调度引擎已停止", false
	}
}

// executeManualAsync 调度循环内执行一条异步手动触发条目（仅 loop goroutine 调用）。
func (s *Scheduler) executeManualAsync(id int) {
	sc := s.findScheduleByID(id)
	if sc == nil {
		logger.Warn("schedule", "manual async target #%d missing", id)
		return
	}
	logger.Info("schedule", "manual async execute task #%d %q", sc.ID, sc.Name)
	s.executeWithSource(*sc, 0, "", "", 0, "manual")
}

// execOne 分发执行单个执行器。
func (s *Scheduler) execOne(sc Schedule, e Executor, srcID int, srcName string, depth int) (string, []string) {
	switch e.Type {
	case ExecutorFanControl:
		return s.execFanControl(e)
	case ExecutorDiskGroupControl:
		return s.execDiskGroupControl(e)
	case ExecutorControlTask:
		return s.execControlTask(sc, e, srcID, srcName, depth)
	case ExecutorExecScript:
		return s.execScriptExecutor(e)
	}
	return "", []string{"未知执行器类型 " + e.Type}
}

// executorTypeLabel 执行器类型中文标签（日志/详情）。
func executorTypeLabel(t string) string {
	switch t {
	case ExecutorFanControl:
		return "风扇控制"
	case ExecutorDiskGroupControl:
		return "硬盘组控制"
	case ExecutorControlTask:
		return "控制任务"
	case ExecutorExecScript:
		return "执行脚本"
	}
	return t
}

// executeActions 执行一组动作，返回（成功详情, 失败列表, 待投递串联条目）。
// run_task 动作只构造待投递条目，由调用方在组结果确定后统一投递
// （来源结果 = 该组所属任务的最终结果）。
func (s *Scheduler) executeActions(sc Schedule, actions []ScheduleAction, srcID int, srcName string, depth int) (string, []string, []pendingRun) {
	var (
		detailParts []string
		fails       []string
		runs        []pendingRun
	)
	for _, a := range actions {
		switch a.Type {
		case ActionFanCurve:
			d, f := s.execFanCurve(a)
			detailParts = append(detailParts, d)
			fails = append(fails, f...)
		case ActionDiskPower:
			d, f := s.execDiskPower(a)
			detailParts = append(detailParts, d)
			fails = append(fails, f...)
		case ActionRunTask:
			d, f, p := s.buildRunTask(a, srcID, srcName, depth, sc.ID, sc.Name)
			detailParts = append(detailParts, d)
			fails = append(fails, f...)
			if p != nil {
				runs = append(runs, *p)
			}
		case ActionEnableTask, ActionDisableTask:
			d, f := s.execSetTaskEnabled(a, a.Type == ActionEnableTask)
			detailParts = append(detailParts, d)
			fails = append(fails, f...)
		default:
			fails = append(fails, "未知动作类型 "+a.Type)
		}
	}
	return strings.Join(detailParts, "；"), fails, runs
}

// execFanCurve 执行风扇曲线切换动作。
// 曲线切换为纯软件配置（settings.ActiveCurve），不依赖设备在线：
// thermal 自动调速器每 10s 从 ble 设置副本读取当前曲线，下一周期自动生效。
func (s *Scheduler) execFanCurve(a ScheduleAction) (string, []string) {
	set := s.store.GetSettings()
	var (
		fails    []string
		switched []string
		needSave bool
	)
	for _, ch := range a.FanChannels {
		found := false
		for i := range set.Fans {
			if set.Fans[i].ID != ch {
				continue
			}
			found = true
			if set.Fans[i].ActiveCurve != a.FanMode {
				set.Fans[i].ActiveCurve = a.FanMode
				needSave = true
			}
			switched = append(switched, fmt.Sprintf("FAN%d", ch))
			break
		}
		if !found {
			fails = append(fails, fmt.Sprintf("FAN%d 通道不存在", ch))
		}
	}
	if needSave {
		s.store.SaveSettings(set)
		if s.ble != nil {
			s.ble.SetSettings(set)
		}
	}
	return fmt.Sprintf("目标曲线 %s：%s", curveDisplayName(a.FanMode), strings.Join(switched, ",")), fails
}

// execDiskPower 执行硬盘上下线动作。
// 设备离线（非加密会话）→ 全部组跳过并记 failed；
// 下线路径（无人值守，无前端决策）：
//   - ForceOff=false → PowerOff(groupID, "force")：umount -f 强制卸载后安全下线
//   - ForceOff=true  → ForcePowerOff(groupID)：跳过安全流程直接断电
func (s *Scheduler) execDiskPower(a ScheduleAction) (string, []string) {
	var (
		fails       []string
		detailParts []string
	)
	offline := s.ble == nil || !s.ble.IsEncryptedSession()
	for _, g := range a.DiskGroups {
		if offline {
			fails = append(fails, fmt.Sprintf("组%d：设备离线，任务跳过", g))
			continue
		}
		switch a.DiskAction {
		case DiskActionOnline:
			res, err := s.disk.PowerOn(g)
			if err != nil || !res.OK {
				fails = append(fails, fmt.Sprintf("组%d 上线失败：%s", g, resMessage(res, err)))
			} else {
				detailParts = append(detailParts, fmt.Sprintf("组%d 上线", g))
			}
		case DiskActionOffline:
			if a.ForceOff {
				res, err := s.disk.ForcePowerOff(g)
				if err != nil || !res.OK {
					fails = append(fails, fmt.Sprintf("组%d 强制下线失败：%s", g, resMessage(res, err)))
				} else {
					detailParts = append(detailParts, fmt.Sprintf("组%d 强制下线", g))
				}
			} else {
				res, err := s.disk.PowerOff(g, "force")
				if err != nil || !res.OK {
					fails = append(fails, fmt.Sprintf("组%d 下线失败：%s", g, resMessage(res, err)))
				} else {
					detailParts = append(detailParts, fmt.Sprintf("组%d 下线", g))
				}
			}
		}
	}
	return strings.Join(detailParts, "；"), fails
}

// buildRunTask 构造 run_task 投递条目（不立即入队；返回失败时 p 为 nil）。
// 校验：目标任务存在、延迟范围、链深度、防重入（已在队列则失败）。
func (s *Scheduler) buildRunTask(a ScheduleAction, srcID int, srcName string, depth int, selfID int, selfName string) (string, []string, *pendingRun) {
	// 常规触发（无外部来源）时投递者即任务自己
	if srcID == 0 {
		srcID, srcName = selfID, selfName
	}
	if a.TaskID <= 0 {
		return "", []string{"执行任务动作缺少目标任务"}, nil
	}
	if a.DelaySec < 0 || a.DelaySec > maxScheduleActionDelaySec {
		return "", []string{fmt.Sprintf("投递延迟范围 0~%d 秒", maxScheduleActionDelaySec)}, nil
	}
	if depth >= maxScheduleChainDepth {
		return "", []string{fmt.Sprintf("串联链深度超过 %d 级，拒绝投递", maxScheduleChainDepth)}, nil
	}
	target := s.findScheduleByID(a.TaskID)
	if target == nil {
		return "", []string{fmt.Sprintf("目标任务 #%d 不存在", a.TaskID)}, nil
	}
	p := &pendingRun{
		ScheduleID:      a.TaskID,
		RunAt:           s.now().Add(time.Duration(a.DelaySec) * time.Second),
		SourceID:        srcID,
		SourceName:      srcName,
		RespectEnabled:  a.RespectEnabled,
		RespectTimeRule: a.RespectTimeRule,
		Depth:           depth + 1,
	}
	desc := fmt.Sprintf("投递任务「%s」", target.Name)
	if a.DelaySec > 0 {
		desc += fmt.Sprintf("（延迟 %d 秒）", a.DelaySec)
	}
	return desc, nil, p
}

// execSetTaskEnabled 执行启用/禁用目标任务动作（双写 store + ble）。
func (s *Scheduler) execSetTaskEnabled(a ScheduleAction, enable bool) (string, []string) {
	if a.TaskID <= 0 {
		return "", []string{"任务开关动作缺少目标任务"}
	}
	schedules := s.store.GetSchedules()
	for i := range schedules {
		if schedules[i].ID != a.TaskID {
			continue
		}
		verb := "启用"
		if !enable {
			verb = "停用"
		}
		if schedules[i].Enabled == enable {
			return fmt.Sprintf("任务「%s」已%s", schedules[i].Name, verb), nil
		}
		schedules[i].Enabled = enable
		s.store.SaveSchedules(schedules)
		return fmt.Sprintf("已%s任务「%s」", verb, schedules[i].Name), nil
	}
	return "", []string{fmt.Sprintf("目标任务 #%d 不存在", a.TaskID)}
}

// resMessage 汇总 CommandResult.Message 与 error，优先非空者。
func resMessage(res CommandResult, err error) string {
	if res.Message != "" {
		return res.Message
	}
	if err != nil {
		return err.Error()
	}
	return "未知错误"
}

// disableOneTime 禁用一次性任务（执行后自动停用）。
func (s *Scheduler) disableOneTime(id int) {
	schedules := s.store.GetSchedules()
	for i := range schedules {
		if schedules[i].ID != id {
			continue
		}
		if !schedules[i].Enabled {
			return
		}
		schedules[i].Enabled = false
		s.store.SaveSchedules(schedules)
		logger.Info("schedule", "one-time task #%d auto-disabled after run", id)
		return
	}
}

// addLog 写任务执行日志（内存 + 按天文件，由 ScheduleLogStore 统一处理）。
// srcID/srcResult 为串联来源（0/"" = 常规触发）；triggerKey 为命中的触发器摘要
// （per-trigger 防重复；串联投递为空）。
func (s *Scheduler) addLog(sc Schedule, result, detail string, srcID int, srcResult, triggerKey string) {
	st := GetScheduleLogStore()
	if st == nil {
		return
	}
	st.Add(ScheduleLog{
		ScheduleID:       sc.ID,
		TaskName:         sc.Name,
		Category:         sc.Category,
		ScheduleType:     sc.ScheduleType,
		Channels:         channelsSummary(sc),
		Result:           result,
		Detail:           detail,
		SourceScheduleID: srcID,
		SourceResult:     srcResult,
		TriggerKey:       triggerKey,
		TriggerType:      scheduleLogTriggerType(triggerKey, sc.ScheduleType, srcID),
	})
}

// scheduleLogTriggerType 由触发器摘要推导触发类型（修订版「触发类型」筛选）。
// 串联投递（srcID>0）不属于任何触发类型，返回空（前端显示「被调用」/归入全部）。
func scheduleLogTriggerType(triggerKey, scheduleType string, srcID int) string {
	if srcID > 0 {
		return ""
	}
	switch {
	case triggerKey == "manual":
		return ScheduleLogTriggerManual
	case strings.HasPrefix(triggerKey, "log:") || triggerKey == "sleep":
		return ScheduleLogTriggerLog
	case strings.HasPrefix(triggerKey, "monitor:") || triggerKey == "idle":
		return ScheduleLogTriggerMonitor
	case strings.HasPrefix(triggerKey, "one_time:") || strings.HasPrefix(triggerKey, "daily:") ||
		strings.HasPrefix(triggerKey, "weekly:") || strings.HasPrefix(triggerKey, "monthly:") ||
		triggerKey == "one_time" || triggerKey == "daily" || triggerKey == "weekly" ||
		triggerKey == "monthly" || triggerKey == "trigger":
		return ScheduleLogTriggerTime
	}
	switch scheduleType {
	case ScheduleTypeOneTime, ScheduleTypeDaily, ScheduleTypeWeekly, ScheduleTypeMonthly:
		return ScheduleLogTriggerTime
	}
	// 旧事件型存量（trigger）无法区分 log/monitor：留空归入「全部」
	return ""
}

// hasScheduleLogSince 该任务自 since 起是否已有执行日志（success/failed 均计入）。
func hasScheduleLogSince(scheduleID int, since time.Time) bool {
	st := GetScheduleLogStore()
	if st == nil {
		return false
	}
	return st.HasLogSince(scheduleID, since)
}

// hasTriggerLogSince 该任务指定触发器自 since 起是否已有执行日志（per-trigger 防重复）。
func hasTriggerLogSince(scheduleID int, triggerKey string, since time.Time) bool {
	st := GetScheduleLogStore()
	if st == nil {
		return false
	}
	return st.HasTriggerLogSince(scheduleID, triggerKey, since)
}

// ===== ScheduleLogStore 日志查询（防重复/兜底） =====

// HasTriggerLogSince 该任务指定触发器自 since 起是否已有执行日志（线程安全）。
// M8 per-trigger 防重复：多触发器任务各触发器独立消费（如 daily:09:00 与
// daily:18:00 互不遮挡）；旧日志（迁移前无 TriggerKey）不参与匹配，
// 仅迁移日可能多执行一次。
func (s *ScheduleLogStore) HasTriggerLogSince(scheduleID int, triggerKey string, since time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.events {
		if e.ScheduleID == scheduleID && e.TriggerKey == triggerKey && !e.Time.Before(since) {
			return true
		}
	}
	return false
}

// HasLogSince 该任务自 since 起是否已有执行日志（线程安全）。
func (s *ScheduleLogStore) HasLogSince(scheduleID int, since time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.events {
		if e.ScheduleID == scheduleID && !e.Time.Before(since) {
			return true
		}
	}
	return false
}

// curveDisplayName 曲线 key → 中文显示名。
func curveDisplayName(key string) string {
	switch key {
	case FanCurveEfficient:
		return "高效"
	case FanCurveDaily:
		return "日常"
	case FanCurveQuiet:
		return "静音"
	}
	return key
}

// validHHMMSS 校验 "HH:MM:SS" 非零时长格式（00:00:00 视为无效）。
func validHHMMSS(s string) bool {
	if len(s) != 8 || s[2] != ':' || s[5] != ':' {
		return false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	sec := int(s[6]-'0')*10 + int(s[7]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return false
	}
	return !(h == 0 && m == 0 && sec == 0)
}

// parseHHMMSS 解析 "HH:MM:SS" 为 time.Duration。调用方应先 validHHMMSS 校验。
func parseHHMMSS(s string) (time.Duration, bool) {
	if !validHHMMSS(s) {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	sec := int(s[6]-'0')*10 + int(s[7]-'0')
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second, true
}

// startOfDay 返回当天 00:00（同一 Location）。
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// goWeekdayToISO Go 的 time.Weekday（0=周日）→ 本项目约定（1=周一 ~ 7=周日）。
func goWeekdayToISO(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	return wd
}

// containsInt 判断整型列表是否包含目标值。
func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
