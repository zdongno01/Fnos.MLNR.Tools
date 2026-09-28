package main

import (
	"reflect"
	"testing"
)

// 挂载表样例：设备 sda1 挂载到 /vol00/DATA，sdb1 挂载到 /vol00/ST8000NM000A-2KE101（与用户配置一致）
func sampleProcMounts() []string {
	return []string{
		"overlay / overlay rw,relatime 0 0",
		"/dev/sda1 /vol00/DATA ext4 rw,relatime 0 0",
		"/dev/sdb1 /vol00/ST8000NM000A-2KE101 ext4 rw,relatime 0 0",
		"tmpfs /run tmpfs rw,nosuid 0 0",
	}
}

func TestMountsOfDevice(t *testing.T) {
	m := sampleProcMounts()
	if got := mountsOfDevice(m, "/dev/sdb1"); !reflect.DeepEqual(got, []string{"/vol00/ST8000NM000A-2KE101"}) {
		t.Fatalf("mountsOfDevice(sdb1) = %v, want [%s]", got, "/vol00/ST8000NM000A-2KE101")
	}
	if got := mountsOfDevice(m, "/dev/sda1"); !reflect.DeepEqual(got, []string{"/vol00/DATA"}) {
		t.Fatalf("mountsOfDevice(sda1) = %v, want [%s]", got, "/vol00/DATA")
	}
	// 未挂载设备 → 空
	if got := mountsOfDevice(m, "/dev/sdc1"); len(got) != 0 {
		t.Fatalf("mountsOfDevice(sdc1) = %v, want empty", got)
	}
	// 空设备 → 空
	if got := mountsOfDevice(m, ""); len(got) != 0 {
		t.Fatalf("mountsOfDevice(\"\") = %v, want empty", got)
	}
}

func TestIsMountPoint(t *testing.T) {
	m := sampleProcMounts()
	if !isMountPoint(m, "/vol00/ST8000NM000A-2KE101") {
		t.Fatal("isMountPoint should be true for existing mount point")
	}
	if isMountPoint(m, "/vol00/GONE") {
		t.Fatal("isMountPoint should be false for unmounted path")
	}
	if isMountPoint(m, "") {
		t.Fatal("isMountPoint should be false for empty path")
	}
}

func TestDiskStillMounted(t *testing.T) {
	d := &DiskManager{}
	m := sampleProcMounts()

	// 新格式：配置挂载点在表中 → 仍挂载
	disk1 := DiskEntry{Device: "/dev/sdb1", Mounts: []DiskMount{{Partition: "/dev/sdb1", MountPoint: "/vol00/ST8000NM000A-2KE101"}}}
	if !d.diskStillMounted(disk1, m) {
		t.Fatal("diskStillMounted: configured mount point present, want true")
	}
	// 新格式：设备挂载到别处、配置挂载点已卸载 → 设备实际挂载点仍在 → 仍挂载
	disk2 := DiskEntry{Device: "/dev/sda1", Mounts: []DiskMount{{Partition: "/dev/sda1", MountPoint: "/vol00/ST8000NM000A-2KE101"}}}
	if !d.diskStillMounted(disk2, m) {
		t.Fatal("diskStillMounted: device mounted elsewhere, want true (device-level check)")
	}
	// 新格式：设备未挂载、配置挂载点也不在表中（用户报错场景：挂载点已卸载）→ 已卸载
	disk3 := DiskEntry{Device: "/dev/sdc1", Mounts: []DiskMount{{Partition: "/dev/sdc1", MountPoint: "/vol00/GONE"}}}
	if d.diskStillMounted(disk3, m) {
		t.Fatal("diskStillMounted: nothing mounted, want false")
	}
	// 新格式：配置挂载点被其它设备占用（挂载点仍在表中）→ 仍挂载（该路径需要处理）
	disk6 := DiskEntry{Device: "/dev/sdc1", Mounts: []DiskMount{{Partition: "/dev/sdc1", MountPoint: "/vol00/ST8000NM000A-2KE101"}}}
	if !d.diskStillMounted(disk6, m) {
		t.Fatal("diskStillMounted: mount point still present (owned by other dev), want true")
	}
	// 旧格式 MountPath
	disk4 := DiskEntry{Device: "/dev/sdb1", MountPath: "/vol00/ST8000NM000A-2KE101"}
	if !d.diskStillMounted(disk4, m) {
		t.Fatal("diskStillMounted: legacy mount path present, want true")
	}
	disk5 := DiskEntry{Device: "/dev/sdc1", MountPath: "/vol00/GONE"}
	if d.diskStillMounted(disk5, m) {
		t.Fatal("diskStillMounted: legacy path unmounted, want false")
	}
}

// 回归：isMounted 仍保持"设备或挂载点任一匹配即挂载"的旧语义（挂载场景使用）
func TestIsMountedLegacySemantics(t *testing.T) {
	m := sampleProcMounts()
	if !isMounted(m, "/dev/sdb1", "/vol00/ST8000NM000A-2KE101") {
		t.Fatal("isMounted: both match, want true")
	}
	// 设备匹配、挂载点不同（旧逻辑误判点）
	if !isMounted(m, "/dev/sda1", "/vol00/ST8000NM000A-2KE101") {
		t.Fatal("isMounted: device match only, want true (legacy semantics)")
	}
	if isMounted(m, "/dev/sdc1", "/vol00/GONE") {
		t.Fatal("isMounted: nothing matches, want false")
	}
}

// isMountedMulti AND 语义 + 序列号绑定：覆盖换盘/换口后按 SN 解析当前分区的场景。
func TestIsMountedMultiAndSemantics(t *testing.T) {
	m := sampleProcMounts()
	// 旧格式 MountPath：挂载点在表 + 无 currentParts → 命中（兼容旧行为）
	if ok, _ := isMountedMulti(m, "/dev/sdb", "/vol00/ST8000NM000A-2KE101", nil, nil); !ok {
		t.Fatal("isMountedMulti legacy: mountpoint present, no cache, want true")
	}
	// 旧格式：挂载点不在表 → 未挂载
	if ok, _ := isMountedMulti(m, "/dev/sdb", "/vol00/GONE", nil, nil); ok {
		t.Fatal("isMountedMulti legacy: mountpoint absent, want false")
	}
	// 旧格式 + currentParts：挂载点在表即视为已挂载，currentParts 不全时不否决
	//（旧格式 device 为整盘路径，无法可靠校验分区归属，避免已挂载却显示未挂载）。
	otherParts := map[string]bool{"/dev/sdc1": true}
	if ok, _ := isMountedMulti(m, "/dev/sdb", "/vol00/ST8000NM000A-2KE101", nil, otherParts); !ok {
		t.Fatal("isMountedMulti legacy+parts: mountpoint present, want true (no rejection)")
	}
	// 旧格式 + currentParts：挂载点在表且设备归属本盘 → 已挂载
	mineParts := map[string]bool{"/dev/sdb1": true}
	if ok, mp := isMountedMulti(m, "/dev/sdb", "/vol00/ST8000NM000A-2KE101", nil, mineParts); !ok || mp != "/vol00/ST8000NM000A-2KE101" {
		t.Fatalf("isMountedMulti legacy+parts: owned, want true+path, got %v %s", ok, mp)
	}
}

// 新格式 Mounts：AND 语义——分区与挂载点须在同一行同时匹配。
func TestIsMountedMultiNewFormat(t *testing.T) {
	m := sampleProcMounts()
	mounts := []DiskMount{{Partition: "/dev/sdb1", MountPoint: "/vol00/ST8000NM000A-2KE101"}}
	// 缓存未就绪：配置分区+挂载点都在表中同一行 → 已挂载
	if ok, mp := isMountedMulti(m, "/dev/sdb", "", mounts, nil); !ok || mp != "/vol00/ST8000NM000A-2KE101" {
		t.Fatalf("isMountedMulti new: both match same line, want true, got %v %s", ok, mp)
	}
	// 缓存未就绪：分区挂到别处、配置挂载点被别的盘占用（OR 旧 bug 场景）→ AND 后应为未挂载
	// /dev/sda1 挂在 /vol00/DATA，但配置写的是分区 /dev/sdb1 + 挂载点 /vol00/ST8000NM000A-2KE101
	// 没有任何一行同时满足两者 → false（修复了旧的 OR 误判）
	mismatchMounts := []DiskMount{{Partition: "/dev/sda1", MountPoint: "/vol00/ST8000NM000A-2KE101"}}
	if ok, _ := isMountedMulti(m, "/dev/sdb", "", mismatchMounts, nil); ok {
		t.Fatal("isMountedMulti new: partition mounted elsewhere + mp taken by other, want false (AND fix)")
	}
	// 回归：缓存就绪但 currentParts 不含本分区（整盘挂载/缓存陈旧），配置路径仍命中 → 已挂载
	// （超集匹配：配置路径 OR currentParts，不因 currentParts 不全而误判）
	partsNoMatch := map[string]bool{"/dev/sdc1": true} // 不含 /dev/sdb1
	if ok, mp := isMountedMulti(m, "/dev/sdb", "", mounts, partsNoMatch); !ok || mp != "/vol00/ST8000NM000A-2KE101" {
		t.Fatalf("isMountedMulti new: config path matches but currentParts lacks it, want true (superset), got %v %s", ok, mp)
	}
	// 整盘挂载：/proc/mounts 里是整盘 /dev/sdb，currentParts 含 base 路径 → 已挂载
	wholeDiskMounts := []string{"/dev/sdb /vol00/ST8000NM000A-2KE101 btrfs rw,relatime 0 0"}
	wholeParts := map[string]bool{"/dev/sdb": true} // base 路径
	wholeCfg := []DiskMount{{Partition: "", MountPoint: "/vol00/ST8000NM000A-2KE101"}}
	if ok, mp := isMountedMulti(wholeDiskMounts, "/dev/sdb", "", wholeCfg, wholeParts); !ok || mp != "/vol00/ST8000NM000A-2KE101" {
		t.Fatalf("isMountedMulti new: whole-disk mount via base path, want true, got %v %s", ok, mp)
	}
}

// 换口场景：配置里存的是旧路径 /dev/sdb1，但盘现在在 /dev/sdc1（SN 不变）。
// currentParts 由 SN 解析得到 {/dev/sdc1}，应正确识别为已挂载。
func TestIsMountedMultiSerialResolution(t *testing.T) {
	// 当前系统里 sdc1 挂载到 /vol00/ST8000NM000A-2KE101
	mounts := []string{
		"/dev/sdc1 /vol00/ST8000NM000A-2KE101 ext4 rw,relatime 0 0",
	}
	// 配置仍是旧路径 /dev/sdb1（盘已换口到 sdc1）
	cfg := []DiskMount{{Partition: "/dev/sdb1", MountPoint: "/vol00/ST8000NM000A-2KE101"}}
	// 无缓存：配置的 /dev/sdb1 匹配不上 /dev/sdc1 → 误判未挂载（旧 bug）
	if ok, _ := isMountedMulti(mounts, "/dev/sdb", "", cfg, nil); ok {
		t.Fatal("isMountedMulti: stale path no cache, want false (path moved)")
	}
	// 有缓存（SN 解析出当前分区 /dev/sdc1）→ 正确识别已挂载
	currentParts := map[string]bool{"/dev/sdc1": true}
	if ok, mp := isMountedMulti(mounts, "/dev/sdb", "", cfg, currentParts); !ok || mp != "/vol00/ST8000NM000A-2KE101" {
		t.Fatalf("isMountedMulti: SN-resolved current part, want true+path, got %v %s", ok, mp)
	}
}
