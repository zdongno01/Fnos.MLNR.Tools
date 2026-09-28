package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mlnr/logger"
)

// ============================================================
// 定时计划资源管理（M13：监控脚本 / 执行脚本 / 日志事件）
//
//   - CRUD：三类资源各自列表/新建/更新/删除
//   - 测试接口：POST /xxx/test 按弹窗内未保存内容测试（root/30s/dry-run），
//     仅采集输出与错误，不执行真实硬件控制动作；日志测试仅校验不改监控游标
//   - 删除引用检查：删除资源时检查全部计划任务的引用（触发器 LogEventID /
//     MonitorScriptID、执行器 ScriptID）；存在引用时返回 409 + 引用任务清单，
//     前端弹窗确认后带 force=1 重发 → 删资源 + 清任务引用字段 + 任务本体保留
//
// 持久化：资源不跟随 BLE MAC 隔离，统一存 dataDir 顶层
//   - resources.json（schedules + logEvents）
//   - monitor-scripts.json + scripts/monitor-<id>.sh
//   - exec-scripts.json + scripts/exec-<id>.sh
// ============================================================

// ScheduleBrief 资源引用任务摘要。
type ScheduleBrief struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// refNames 引用任务显示名（409 错误消息用，前端解析 [..] 列出任务清单）。
func refNames(refs []ScheduleBrief) string {
	if len(refs) == 0 {
		return "[]"
	}
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		n := r.Name
		if n == "" {
			n = "任务 #" + strconv.Itoa(r.ID)
		}
		names = append(names, n)
	}
	return "[" + strings.Join(names, "、") + "]"
}

// resourceReferences 资源被哪些计划任务引用（resType: monitor-script/exec-script/log-event）。
func (h *Handlers) resourceReferences(resType string, id int) []ScheduleBrief {
	var out []ScheduleBrief
	for _, sc := range h.store.GetSchedules() {
		hit := false
		switch resType {
		case "monitor-script":
			for _, tr := range sc.Triggers {
				if tr.MonitorScriptID == id {
					hit = true
					break
				}
			}
		case "exec-script":
			for _, ex := range sc.Executors {
				if ex.ScriptID == id {
					hit = true
					break
				}
			}
		case "log-event":
			for _, tr := range sc.Triggers {
				if tr.LogEventID == id {
					hit = true
					break
				}
			}
		}
		if hit {
			out = append(out, ScheduleBrief{ID: sc.ID, Name: sc.Name})
		}
	}
	return out
}

// clearResourceReferences 从计划任务中清除指定资源的引用字段（任务本体保留）。
func clearResourceReferences(store *Store, resType string, id int) {
	schedules := store.GetSchedules()
	changed := false
	for i := range schedules {
		sc := &schedules[i]
		switch resType {
		case "monitor-script":
			for j := range sc.Triggers {
				if sc.Triggers[j].MonitorScriptID == id {
					sc.Triggers[j].MonitorScriptID = 0
					changed = true
				}
			}
		case "exec-script":
			for j := range sc.Executors {
				if sc.Executors[j].ScriptID == id {
					sc.Executors[j].ScriptID = 0
					changed = true
				}
			}
		case "log-event":
			for j := range sc.Triggers {
				if sc.Triggers[j].LogEventID == id {
					sc.Triggers[j].LogEventID = 0
					changed = true
				}
			}
		}
	}
	if changed {
		store.SaveSchedules(schedules)
	}
}

// ===== 监控脚本 =====

func (h *Handlers) listMonitorScripts(c *gin.Context) {
	// 预制已由 Store.seedPrebuiltIfMissing 写入 store.GetMonitorScriptsWithCode()
	c.JSON(200, gin.H{"monitorScripts": h.store.GetMonitorScriptsWithCode()})
}

func (h *Handlers) createMonitorScript(c *gin.Context) {
	var ms MonitorScript
	if err := c.ShouldBindJSON(&ms); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeScriptResource(&ms.Name, ms.Code, "监控脚本"); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	ms.ScriptType = normalizeScriptType(ms.ScriptType)
	ms.Args = strings.TrimSpace(ms.Args)
	code := ms.Code
	id := h.store.AddMonitorScript(ms, code)
	// 读回含完整正文的记录（元数据 JSON 不含 Code）
	created := h.store.GetMonitorScriptByID(id)
	logger.Info("schedule", "created monitor script #%d %q", id, ms.Name)
	c.JSON(200, gin.H{"monitorScript": created})
}

func (h *Handlers) updateMonitorScript(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的资源 ID"})
		return
	}
	var ms MonitorScript
	if err := c.ShouldBindJSON(&ms); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeScriptResource(&ms.Name, ms.Code, "监控脚本"); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	ms.ScriptType = normalizeScriptType(ms.ScriptType)
	ms.Args = strings.TrimSpace(ms.Args)
	if !h.store.UpdateMonitorScript(id, ms, ms.Code) {
		c.JSON(404, gin.H{"error": "监控脚本不存在"})
		return
	}
	logger.Info("schedule", "updated monitor script #%d %q", id, ms.Name)
	c.JSON(200, gin.H{"monitorScript": h.store.GetMonitorScriptByID(id)})
}

func (h *Handlers) deleteMonitorScript(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的资源 ID"})
		return
	}
	refs := h.resourceReferences("monitor-script", id)
	if len(refs) > 0 && c.Query("force") != "1" {
		c.JSON(409, gin.H{"error": "该监控脚本被以下计划任务引用：" + refNames(refs) + "，请确认后删除", "referencedBy": refs})
		return
	}
	if !h.store.DeleteMonitorScript(id) {
		c.JSON(404, gin.H{"error": "监控脚本不存在"})
		return
	}
	clearResourceReferences(h.store, "monitor-script", id)
	logger.Info("schedule", "deleted monitor script #%d (cleared %d refs)", id, len(refs))
	c.JSON(200, gin.H{"ok": true, "clearedReferences": len(refs)})
}

// testMonitorScript 监控脚本测试（root/30s/dry-run：仅采集输出与错误，支持传参）。
func (h *Handlers) testMonitorScript(c *gin.Context) {
	var body struct {
		Code string `json:"code"`
		Type string `json:"type"` // "shell" / "python"；空=按 shebang 自动识别
		Args string `json:"args"` // 命令行参数（sh 用 $1 $2 …，python 用 sys.argv[1:]）
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(body.Code) == "" {
		c.JSON(400, gin.H{"error": "脚本代码不能为空"})
		return
	}
	out, err := runScriptCode(body.Code, normalizeScriptType(body.Type), body.Args, 30*time.Second)
	if err != nil {
		c.JSON(200, gin.H{"output": out, "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"output": out})
}

// ===== 执行脚本 =====

func (h *Handlers) listExecScripts(c *gin.Context) {
	c.JSON(200, gin.H{"execScripts": h.store.GetExecScriptsWithCode()})
}

func (h *Handlers) createExecScript(c *gin.Context) {
	var es ExecScript
	if err := c.ShouldBindJSON(&es); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeScriptResource(&es.Name, es.Code, "执行脚本"); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	es.ScriptType = normalizeScriptType(es.ScriptType)
	es.Args = strings.TrimSpace(es.Args)
	code := es.Code
	id := h.store.AddExecScript(es, code)
	logger.Info("schedule", "created exec script #%d %q", id, es.Name)
	c.JSON(200, gin.H{"execScript": h.store.GetExecScriptByID(id)})
}

func (h *Handlers) updateExecScript(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的资源 ID"})
		return
	}
	var es ExecScript
	if err := c.ShouldBindJSON(&es); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeScriptResource(&es.Name, es.Code, "执行脚本"); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	es.ScriptType = normalizeScriptType(es.ScriptType)
	es.Args = strings.TrimSpace(es.Args)
	if !h.store.UpdateExecScript(id, es, es.Code) {
		c.JSON(404, gin.H{"error": "执行脚本不存在"})
		return
	}
	logger.Info("schedule", "updated exec script #%d %q", id, es.Name)
	c.JSON(200, gin.H{"execScript": h.store.GetExecScriptByID(id)})
}

func (h *Handlers) deleteExecScript(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的资源 ID"})
		return
	}
	refs := h.resourceReferences("exec-script", id)
	if len(refs) > 0 && c.Query("force") != "1" {
		c.JSON(409, gin.H{"error": "该执行脚本被以下计划任务引用：" + refNames(refs) + "，请确认后删除", "referencedBy": refs})
		return
	}
	if !h.store.DeleteExecScript(id) {
		c.JSON(404, gin.H{"error": "执行脚本不存在"})
		return
	}
	clearResourceReferences(h.store, "exec-script", id)
	logger.Info("schedule", "deleted exec script #%d (cleared %d refs)", id, len(refs))
	c.JSON(200, gin.H{"ok": true, "clearedReferences": len(refs)})
}

// testExecScript 执行脚本测试（root/30s/dry-run，支持传参）。
func (h *Handlers) testExecScript(c *gin.Context) {
	var body struct {
		Code string `json:"code"`
		Type string `json:"type"` // "shell" / "python"；空=按 shebang 自动识别
		Args string `json:"args"` // 命令行参数（sh 用 $1 $2 …，python 用 sys.argv[1:]）
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(body.Code) == "" {
		c.JSON(400, gin.H{"error": "脚本代码不能为空"})
		return
	}
	out, err := runScriptCode(body.Code, normalizeScriptType(body.Type), body.Args, 30*time.Second)
	if err != nil {
		c.JSON(200, gin.H{"output": out, "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"output": out})
}

// ===== 日志事件 =====

func (h *Handlers) listLogEvents(c *gin.Context) {
	c.JSON(200, gin.H{"logEvents": h.store.GetLogEvents()})
}

func (h *Handlers) createLogEvent(c *gin.Context) {
	var ev LogEvent
	if err := c.ShouldBindJSON(&ev); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeLogEventResource(&ev); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	id := h.store.AddLogEvent(ev)
	logger.Info("schedule", "created log event #%d %q", id, ev.Name)
	c.JSON(200, gin.H{"logEvent": h.store.GetLogEventByID(id)})
}

func (h *Handlers) updateLogEvent(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的资源 ID"})
		return
	}
	var ev LogEvent
	if err := c.ShouldBindJSON(&ev); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if msg := normalizeLogEventResource(&ev); msg != "" {
		c.JSON(400, gin.H{"error": msg})
		return
	}
	if !h.store.UpdateLogEvent(id, ev) {
		c.JSON(404, gin.H{"error": "日志事件不存在"})
		return
	}
	logger.Info("schedule", "updated log event #%d %q", id, ev.Name)
	c.JSON(200, gin.H{"logEvent": h.store.GetLogEventByID(id)})
}

func (h *Handlers) deleteLogEvent(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的资源 ID"})
		return
	}
	refs := h.resourceReferences("log-event", id)
	if len(refs) > 0 && c.Query("force") != "1" {
		c.JSON(409, gin.H{"error": "该日志事件被以下计划任务引用：" + refNames(refs) + "，请确认后删除", "referencedBy": refs})
		return
	}
	if !h.store.DeleteLogEvent(id) {
		c.JSON(404, gin.H{"error": "日志事件不存在"})
		return
	}
	clearResourceReferences(h.store, "log-event", id)
	logger.Info("schedule", "deleted log event #%d (cleared %d refs)", id, len(refs))
	c.JSON(200, gin.H{"ok": true, "clearedReferences": len(refs)})
}

// testLogEvent 日志事件测试（仅校验路径+正则，不修改监控游标）。
func (h *Handlers) testLogEvent(c *gin.Context) {
	var body struct {
		Path  string `json:"path"`
		Regex string `json:"regex"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(body.Path) == "" || strings.TrimSpace(body.Regex) == "" {
		c.JSON(400, gin.H{"error": "日志路径与日志监控内容不能为空"})
		return
	}
	hits, err := testLogEvent(body.Path, body.Regex)
	if err != nil {
		c.JSON(200, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"matches": hits})
}

// ===== 校验 =====

// normalizeScriptResource 脚本资源校验（名称 ≤50 必填、代码非空）。
func normalizeScriptResource(name *string, code, label string) string {
	*name = strings.TrimSpace(*name)
	if *name == "" {
		return label + "名称必填"
	}
	if len([]rune(*name)) > 50 {
		return label + "名称不能超过 50 字"
	}
	if strings.TrimSpace(code) == "" {
		return label + "代码不能为空"
	}
	return ""
}

// normalizeLogEventResource 日志事件校验（名称/路径/正则/冷却/连续/单位）。
func normalizeLogEventResource(ev *LogEvent) string {
	ev.Name = strings.TrimSpace(ev.Name)
	ev.Path = strings.TrimSpace(ev.Path)
	ev.Regex = strings.TrimSpace(ev.Regex)
	if ev.Name == "" {
		return "日志事件名称必填"
	}
	if len([]rune(ev.Name)) > 50 {
		return "日志事件名称不能超过 50 字"
	}
	if ev.Path == "" {
		return "日志路径必填"
	}
	if ev.Regex == "" {
		return "日志监控内容必填"
	}
	if ev.Cooldown < 0 || ev.Cooldown > 99 {
		return "冷却时间范围为 0~99"
	}
	switch ev.CooldownUnit {
	case "", CooldownUnitSecond:
		ev.CooldownUnit = CooldownUnitSecond
	case CooldownUnitMinute, CooldownUnitHour:
	default:
		return "冷却时间单位无效"
	}
	if ev.Cooldown == 0 {
		ev.Consecutive = 1 // 不冷却：连续匹配强制 1 次
	} else if ev.Consecutive < 1 || ev.Consecutive > 9 {
		return "连续匹配次数范围为 1~9"
	}
	return ""
}
