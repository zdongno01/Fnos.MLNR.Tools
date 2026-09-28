package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskLogMigrationAndRemark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disk_logs.jsonl")
	// 旧格式历史行（error 字段 + 旧动作）
	lines := []string{
		`{"time":"2026-09-13T22:00:00+08:00","groupId":1,"alias":"A","action":"power_off","content":"SW1 OFF","result":"fail","error":"umount \"/vol1/1000/1.5T\" 失败: exit status 1 (umount: \"/vol1/1000/1.5T\": No such file or directory)"}`,
		`{"time":"2026-09-13T22:01:00+08:00","groupId":1,"alias":"A","action":"auto_on","content":"SW1 ON (auto)","result":"ok"}`,
		`{"time":"2026-09-13T22:02:00+08:00","groupId":1,"alias":"A","action":"offline_off","content":"SW1 OFF (offline) [button]","result":"ok"}`,
		`{"time":"2026-09-13T22:03:00+08:00","groupId":1,"alias":"A","action":"mount","content":"/dev/sdb1","result":"ok"}`,
	}
	f, _ := os.Create(path)
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
	f.Close()

	InitDiskLogger(dir)
	l := GetDiskLogger()
	res := l.Query(DiskLogQueryParams{GroupID: -1, Page: 1, PageSize: 20})
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	_ = time.Now
}
