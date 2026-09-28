package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// ===== M13 定时计划资源管理 =====

func newResourceHandlers(t *testing.T) (*Handlers, *Store) {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := store.Load(); err != nil {
		t.Fatalf("store load: %v", err)
	}
	gin.SetMode(gin.TestMode)
	return &Handlers{store: store}, store
}

// callHandler 以指定 handler 处理请求（构造 gin 上下文并设置 Request 与路由参数）。
func callHandler(h *Handlers, handler func(*gin.Context), method, path string, body interface{}, params ...gin.Param) (*httptest.ResponseRecorder, map[string]interface{}) {
	r := httptest.NewRecorder()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	ginCtx, _ := gin.CreateTestContext(r)
	ginCtx.Request = req
	ginCtx.Params = params
	handler(ginCtx)
	var out map[string]interface{}
	if r.Body.Len() > 0 {
		_ = json.Unmarshal(r.Body.Bytes(), &out)
	}
	return r, out
}

func TestResource_MonitorScriptCRUD(t *testing.T) {
	h, _ := newResourceHandlers(t)

	// 列表：首次启动应含预制条目
	rr, out := callHandler(h, h.listMonitorScripts, "GET", "/monitor-scripts", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d", rr.Code)
	}
	items := out["monitorScripts"].([]interface{})
	if len(items) == 0 {
		t.Fatalf("no monitor scripts, prebuilt should be seeded")
	}
	hasPrebuilt := false
	for _, it := range items {
		if it.(map[string]interface{})["name"] == "监控硬盘AB闲置（预制）" {
			hasPrebuilt = true
			break
		}
	}
	if !hasPrebuilt {
		t.Fatalf("prebuilt monitor script missing, got %v", items)
	}

	// 创建
	rr, out = callHandler(h, h.createMonitorScript, "POST", "/monitor-scripts", map[string]interface{}{
		"name": "磁盘IO监控", "code": "#!/bin/sh\nawk ...\n",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d %v", rr.Code, out)
	}
	id := int(out["monitorScript"].(map[string]interface{})["id"].(float64))
	if id <= 0 {
		t.Fatalf("invalid id %d", id)
	}
	idStr := strconv.Itoa(id)

	// 创建校验：名称/代码必填
	if rr, out = callHandler(h, h.createMonitorScript, "POST", "/monitor-scripts", map[string]interface{}{"name": "", "code": "x"}); rr.Code != 400 {
		t.Fatalf("empty name should 400, got %d %v", rr.Code, out)
	}
	if rr, out = callHandler(h, h.createMonitorScript, "POST", "/monitor-scripts", map[string]interface{}{"name": "x", "code": "  "}); rr.Code != 400 {
		t.Fatalf("empty code should 400, got %d %v", rr.Code, out)
	}

	// 更新
	rr, out = callHandler(h, h.updateMonitorScript, "PUT", "/monitor-scripts/"+idStr, map[string]interface{}{
		"name": "磁盘IO监控v2", "code": "new code",
	}, gin.Param{Key: "id", Value: idStr})
	if rr.Code != http.StatusOK || out["monitorScript"].(map[string]interface{})["name"] != "磁盘IO监控v2" {
		t.Fatalf("update failed: %d %v", rr.Code, out)
	}

	// 删除（无引用）
	rr, out = callHandler(h, h.deleteMonitorScript, "DELETE", "/monitor-scripts/"+idStr, nil, gin.Param{Key: "id", Value: idStr})
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status=%d %v", rr.Code, out)
	}
	rr, _ = callHandler(h, h.listMonitorScripts, "GET", "/monitor-scripts", nil)
	if len(rr.Body.Bytes()) == 0 {
		t.Fatal("empty response")
	}
}

func TestResource_ExecScriptCRUD(t *testing.T) {
	h, _ := newResourceHandlers(t)
	rr, out := callHandler(h, h.createExecScript, "POST", "/exec-scripts", map[string]interface{}{
		"name": "清理缓存", "code": "sync",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d %v", rr.Code, out)
	}
	id := int(out["execScript"].(map[string]interface{})["id"].(float64))
	idStr := strconv.Itoa(id)
	if rr, out = callHandler(h, h.updateExecScript, "PUT", "/exec-scripts/"+idStr, map[string]interface{}{"name": "清理缓存2", "code": "sync && echo ok"}, gin.Param{Key: "id", Value: idStr}); rr.Code != 200 {
		t.Fatalf("update status=%d %v", rr.Code, out)
	}
	// 不存在更新 404
	if rr, _ = callHandler(h, h.updateExecScript, "PUT", "/exec-scripts/99", map[string]interface{}{"name": "x", "code": "y"}, gin.Param{Key: "id", Value: "99"}); rr.Code != 404 {
		t.Fatalf("update missing should 404, got %d", rr.Code)
	}
}

func TestResource_LogEventCRUD(t *testing.T) {
	h, _ := newResourceHandlers(t)
	// 列表：无预制日志事件（写死「监控硬盘休眠」预制已抛弃），应为空
	rr, out := callHandler(h, h.listLogEvents, "GET", "/log-events", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d", rr.Code)
	}
	if items := out["logEvents"].([]interface{}); len(items) != 0 {
		t.Fatalf("log events should be empty at first, got %v", items)
	}

	// 创建
	rr, out = callHandler(h, h.createLogEvent, "POST", "/log-events", map[string]interface{}{
		"name": "温度告警", "path": "/var/log/syslog", "regex": "temp.*high",
		"cooldown": 5, "cooldownUnit": "minute", "consecutive": 2, "rotate": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d %v", rr.Code, out)
	}
	ev := out["logEvent"].(map[string]interface{})
	if ev["cooldownUnit"] != "minute" || ev["consecutive"].(float64) != 2 || ev["rotate"] != true {
		t.Fatalf("log event fields wrong: %v", ev)
	}

	// 冷却=0 → 连续强制 1
	rr, out = callHandler(h, h.createLogEvent, "POST", "/log-events", map[string]interface{}{
		"name": "无冷却", "path": "/x", "regex": "y", "cooldown": 0, "consecutive": 7,
	})
	if rr.Code != 200 || out["logEvent"].(map[string]interface{})["consecutive"].(float64) != 1 {
		t.Fatalf("cooldown=0 should force consecutive=1: %d %v", rr.Code, out)
	}

	// 校验：冷却越界 / 连续越界 / 单位无效
	for _, bad := range []map[string]interface{}{
		{"name": "x", "path": "/x", "regex": "y", "cooldown": 100},
		{"name": "x", "path": "/x", "regex": "y", "cooldown": 1, "consecutive": 0},
		{"name": "x", "path": "/x", "regex": "y", "cooldown": 1, "consecutive": 10},
		{"name": "x", "path": "/x", "regex": "y", "cooldown": 1, "cooldownUnit": "week"},
		{"name": "", "path": "/x", "regex": "y"},
		{"name": "x", "path": "", "regex": "y"},
		{"name": "x", "path": "/x", "regex": ""},
	} {
		if rr, _ = callHandler(h, h.createLogEvent, "POST", "/log-events", bad); rr.Code != 400 {
			t.Fatalf("bad payload %v should 400, got %d", bad, rr.Code)
		}
	}

	// 更新 / 不存在更新 404
	evMap := out["logEvent"].(map[string]interface{})
	idStr := strconv.Itoa(int(evMap["id"].(float64)))
	rr, out = callHandler(h, h.updateLogEvent, "PUT", "/log-events/"+idStr, map[string]interface{}{
		"name": "温度告警v2", "path": "/var/log/syslog", "regex": "temp.*high", "cooldown": 3, "consecutive": 2,
	}, gin.Param{Key: "id", Value: idStr})
	if rr.Code != 200 || out["logEvent"].(map[string]interface{})["name"] != "温度告警v2" {
		t.Fatalf("update failed: %d %v", rr.Code, out)
	}
	if rr, _ = callHandler(h, h.updateLogEvent, "PUT", "/log-events/99", map[string]interface{}{"name": "x", "path": "/x", "regex": "y"}, gin.Param{Key: "id", Value: "99"}); rr.Code != 404 {
		t.Fatalf("update missing should 404, got %d", rr.Code)
	}

	// 删除（无引用）
	if rr, _ = callHandler(h, h.deleteLogEvent, "DELETE", "/log-events/"+idStr, nil, gin.Param{Key: "id", Value: idStr}); rr.Code != 200 {
		t.Fatalf("delete should 200, got %d", rr.Code)
	}
	if rr, _ = callHandler(h, h.deleteLogEvent, "DELETE", "/log-events/"+idStr, nil, gin.Param{Key: "id", Value: idStr}); rr.Code != 404 {
		t.Fatalf("delete missing should 404, got %d", rr.Code)
	}
}



func TestResource_PrebuiltSeeded(t *testing.T) {
	h, store := newResourceHandlers(t)
	// 首次启动后资源列表应有预制条目（与用户创建等同）
	if len(store.GetMonitorScripts()) == 0 {
		t.Fatal("prebuilt monitor scripts missing")
	}
	// 执行脚本当前无预制条目（prebuiltExecScripts 留空），不应误判存在
	if len(store.GetExecScripts()) != 0 {
		t.Fatal("exec scripts should have no prebuilt entries")
	}

	// 预制条目应可以自由编辑和删除（等同用户创建）
	prebuiltMs := store.GetMonitorScriptByID(1)
	if prebuiltMs.ID == 0 {
		t.Fatalf("prebuilt #1 missing: ms=%d", prebuiltMs.ID)
	}

	msStr := strconv.Itoa(prebuiltMs.ID)

	// 可以编辑
	if rr, _ := callHandler(h, h.updateMonitorScript, "PUT", "/monitor-scripts/"+msStr, map[string]interface{}{"name": "改了", "code": "x"}, gin.Param{Key: "id", Value: msStr}); rr.Code != 200 {
		t.Fatalf("edit prebuilt monitor should 200, got %d", rr.Code)
	}
	// 可以删除
	if rr, _ := callHandler(h, h.deleteMonitorScript, "DELETE", "/monitor-scripts/"+msStr, nil, gin.Param{Key: "id", Value: msStr}); rr.Code != 200 {
		t.Fatalf("delete prebuilt monitor should 200, got %d", rr.Code)
	}
}

func TestResource_DeleteReferenceCheck(t *testing.T) {
	h, store := newResourceHandlers(t)
	// 创建监控脚本 + 引用它的任务
	_, out := callHandler(h, h.createMonitorScript, "POST", "/monitor-scripts", map[string]interface{}{"name": "io监控", "code": "echo x"})
	msID := int(out["monitorScript"].(map[string]interface{})["id"].(float64))
	_, out = callHandler(h, h.createExecScript, "POST", "/exec-scripts", map[string]interface{}{"name": "脚本", "code": "sync"})
	esID := int(out["execScript"].(map[string]interface{})["id"].(float64))
	_, out = callHandler(h, h.createLogEvent, "POST", "/log-events", map[string]interface{}{"name": "事件", "path": "/tmp/a.log", "regex": "hit"})
	leID := int(out["logEvent"].(map[string]interface{})["id"].(float64))

	sc := Schedule{
		ID: 1, Name: "复合任务", Enabled: true,
		Triggers: []Trigger{
			{Type: TriggerTypeMonitor, MonitorScriptID: msID, MonitorIntervalSec: 30},
			{Type: TriggerTypeLog, LogEventID: leID},
		},
		Executors: []Executor{{Type: ExecutorExecScript, ScriptID: esID}},
	}
	store.AddSchedule(sc)

	// 删除被引用资源 → 409 + referencedBy
	msStr := strconv.Itoa(msID)
	esStr := strconv.Itoa(esID)
	leStr := strconv.Itoa(leID)
	rr, out := callHandler(h, h.deleteMonitorScript, "DELETE", "/monitor-scripts/"+msStr, nil, gin.Param{Key: "id", Value: msStr})
	if rr.Code != 409 {
		t.Fatalf("referenced delete should 409, got %d %v", rr.Code, out)
	}
	refs := out["referencedBy"].([]interface{})
	if len(refs) != 1 || refs[0].(map[string]interface{})["name"] != "复合任务" {
		t.Fatalf("referencedBy wrong: %v", refs)
	}
	// 执行脚本 / 日志事件同样被引用
	if rr, _ := callHandler(h, h.deleteExecScript, "DELETE", "/exec-scripts/"+esStr, nil, gin.Param{Key: "id", Value: esStr}); rr.Code != 409 {
		t.Fatalf("referenced exec-script delete should 409, got %d", rr.Code)
	}
	if rr, _ := callHandler(h, h.deleteLogEvent, "DELETE", "/log-events/"+leStr, nil, gin.Param{Key: "id", Value: leStr}); rr.Code != 409 {
		t.Fatalf("referenced log-event delete should 409, got %d", rr.Code)
	}

	// force=1 → 删除资源 + 清引用 + 任务本体保留
	for _, del := range []struct {
		path string
		id   string
		fn   func(*gin.Context)
	}{
		{"/monitor-scripts/" + msStr + "?force=1", msStr, h.deleteMonitorScript},
		{"/exec-scripts/" + esStr + "?force=1", esStr, h.deleteExecScript},
		{"/log-events/" + leStr + "?force=1", leStr, h.deleteLogEvent},
	} {
		if rr, _ := callHandler(h, del.fn, "DELETE", del.path, nil, gin.Param{Key: "id", Value: del.id}); rr.Code != 200 {
			t.Fatalf("force delete should 200, got %d", rr.Code)
		}
	}
	keptSchedules := store.GetSchedules()
	if len(keptSchedules) != 1 {
		t.Fatalf("task should be kept, got %d schedules", len(keptSchedules))
	}
	kept := keptSchedules[0]
	if len(kept.Triggers) != 2 || kept.Triggers[0].MonitorScriptID != 0 || kept.Triggers[1].LogEventID != 0 {
		t.Fatalf("task references should be cleared: %+v", kept.Triggers)
	}
	if kept.Executors[0].ScriptID != 0 {
		t.Fatalf("executor script ref should be cleared: %+v", kept.Executors)
	}
	// 资源已被删除（预制仍存在，所以总长度 > 0 正常）
	if store.GetMonitorScriptByID(msID).ID != 0 ||
		store.GetExecScriptByID(esID).ID != 0 ||
		store.GetLogEventByID(leID).ID != 0 {
		t.Fatalf("user resources should be deleted: ms#%d es#%d le#%d", msID, esID, leID)
	}
}

func TestResource_LogEventTest(t *testing.T) {
	h, _ := newResourceHandlers(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	content := "line1\nERROR something\nline3\nERROR again\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 有匹配：返回全部匹配行
	rr, out := callHandler(h, h.testLogEvent, "POST", "/log-events/test", map[string]interface{}{"path": path, "regex": "ERROR"})
	if rr.Code != 200 {
		t.Fatalf("test status=%d", rr.Code)
	}
	matches := out["matches"].([]interface{})
	if len(matches) != 2 || matches[0] != "ERROR something" || matches[1] != "ERROR again" {
		t.Fatalf("matches wrong: %v", matches)
	}

	// 无匹配
	rr, out = callHandler(h, h.testLogEvent, "POST", "/log-events/test", map[string]interface{}{"path": path, "regex": "NOPE"})
	if rr.Code != 200 || out["error"] == nil {
		t.Fatalf("no-match should return error, got %d %v", rr.Code, out)
	}

	// 正则错误
	rr, out = callHandler(h, h.testLogEvent, "POST", "/log-events/test", map[string]interface{}{"path": path, "regex": "(["})
	if rr.Code != 200 || out["error"] == nil {
		t.Fatalf("bad regex should return error, got %d %v", rr.Code, out)
	}

	// 文件不存在
	rr, out = callHandler(h, h.testLogEvent, "POST", "/log-events/test", map[string]interface{}{"path": filepath.Join(dir, "missing.log"), "regex": "x"})
	if rr.Code != 200 || out["error"] == nil {
		t.Fatalf("missing file should return error, got %d %v", rr.Code, out)
	}

	// 空参数
	if rr, _ := callHandler(h, h.testLogEvent, "POST", "/log-events/test", map[string]interface{}{"path": "", "regex": ""}); rr.Code != 400 {
		t.Fatalf("empty payload should 400, got %d", rr.Code)
	}
}

