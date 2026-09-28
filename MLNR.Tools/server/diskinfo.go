package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mlnr/logger"
)

// ============================================================
// 磁盘信息全量缓存（启动时加载一次，后续从内存读取）
// 参考测试程序 zdaitest001_go/collector.go + types.go + temperature.go
// ============================================================

// ---- 工具函数（与 syscmd.go 互补，这里只提供 disk info 专属的） ----

var (
	// isDiskRe 精确匹配物理磁盘名，排除分区和 nvme 控制器本身：
	//   sda/sdb/sdz/sdaa...       (SATA/SAS)
	//   nvme0n1/nvme1n2/...       (NVMe namespace，不含 nvme0 控制器)
	//   mmcblk0/mmcblk1/...       (SD/MMC)
	//   hda/hdb/...               (IDE)
	//   vda/vdb/...               (虚拟磁盘)
	// 关键：sda1/sdb2/nvme0n1p1 等分区 **不会** 被匹配
	isDiskRe = regexp.MustCompile(`^(sd[a-z]+|nvme\d+n\d+|mmcblk\d+|hd[a-z]+|vd[a-z]+)$`)
	nvmeRe   = regexp.MustCompile(`n\d+.*`)
)

// isDiskName 判断设备名是否为物理磁盘（精确匹配，过滤分区和 loop/ram/zram/nvme 控制器）
func isDiskName(name string) bool {
	return isDiskRe.MatchString(name)
}

// nvmeControllerName nvme0n1 -> nvme0（控制器设备，用于 smartctl 查询）
func nvmeControllerName(name string) string {
	return nvmeRe.ReplaceAllString(name, "")
}

// listSysBlockDisks 从 /sys/class/block 列出物理磁盘名（排序）
// 精确正则匹配 + partition 属性文件双重保险，确保不包含分区、loop/ram/zram、nvme 控制器
func listSysBlockDisks() []string {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil
	}
	var disks []string
	for _, e := range entries {
		name := e.Name()
		if !isDiskName(name) {
			continue
		}
		// 双重保险：排除含有 partition 属性文件的条目（极少见的 edge case）
		if _, err := os.Stat(filepath.Join("/sys/class/block", name, "partition")); err == nil {
			continue
		}
		disks = append(disks, name)
	}
	sort.Strings(disks)
	return disks
}

// formatSize 把字节数格式化为人类可读容量（B/KiB/MiB/GiB/TiB/PiB，保留2位小数）
func formatSize(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(b)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}

// parseDFValue df 输出值可能是 "-"，解析失败保留原始字符串
func parseDFValue(s string) interface{} {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}

// ---- lsblk JSON 结构 ----

type LsblkData struct {
	Blockdevices []LsblkDevice `json:"blockdevices"`
}

type LsblkDevice struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Tran        string        `json:"tran"`
	Fstype      string        `json:"fstype"`
	Pttype      string        `json:"pttype"`
	Size        int64         `json:"size"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Rota        *bool         `json:"rota"`
	Mountpoints []string      `json:"mountpoints"`
	Children    []LsblkDevice `json:"children"`
}

// ---- smartctl JSON 结构（仅提取需要的字段） ----

type SmartctlData struct {
	ModelName    string `json:"model_name"`
	Vendor       string `json:"vendor"`
	Product      string `json:"product"`
	SerialNumber string `json:"serial_number"`
	UserCapacity *struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	RotationRate *struct {
		Value int    `json:"value"`
		Name  string `json:"name"`
	} `json:"rotation_rate"`
	Temperature *struct {
		Current *int `json:"current"`
	} `json:"temperature"`
	NvmeSmartHealth *struct {
		Temperature        *int  `json:"temperature"`
		TemperatureSensors []int `json:"temperature_sensors"`
	} `json:"nvme_smart_health_information"`
}

// ---- 温度相关类型 ----

// TempReading 单盘单个温度测量点（来自 hwmon 或 SMART）
type TempReading struct {
	Label   string `json:"label"`
	Celsius int    `json:"celsius"`
}

// ---- 全量磁盘信息结构（缓存用） ----

// DiskSummary 单块物理磁盘的全量信息（等价于测试程序 buildDiskSummary 的输出）
type DiskSummary struct {
	Name         string        `json:"name"`
	Path         string        `json:"path"`
	Model        string        `json:"model"`
	Vendor       string        `json:"vendor"`
	Serial       string        `json:"serial"`
	SizeBytes    int64         `json:"size_bytes"`
	SizeHuman    string        `json:"size_human"`
	Pttype       string        `json:"pttype"`
	Tran         string        `json:"tran"`
	DiskType     string        `json:"disk_type"`
	RotationRate interface{}   `json:"rotation_rate"`
	Temperature  []TempReading `json:"temperature"`
	Partitions   []Partition   `json:"partitions"`
}

// Partition 分区信息
type Partition struct {
	Name        string      `json:"name"`
	Path        string      `json:"path"`
	Type        string      `json:"type"`
	Fstype      string      `json:"fstype"`
	SizeBytes   int64       `json:"size_bytes"`
	SizeHuman   string      `json:"size_human"`
	Mounts      []Mount     `json:"mounts"`
	StartSector int64       `json:"start_sector,omitempty"`
	SizeSectors int64       `json:"size_sectors,omitempty"`
	Children    []Partition `json:"children,omitempty"`
}

// Mount 分区挂载条目
type Mount struct {
	Mountpoint string      `json:"mountpoint"`
	Fstype     string      `json:"fstype"`
	Options    string      `json:"options,omitempty"`
	Total      interface{} `json:"total,omitempty"`
	Used       interface{} `json:"used,omitempty"`
	Avail      interface{} `json:"avail,omitempty"`
	UsePercent string      `json:"use_percent,omitempty"`
}

// ---- 内部数据类型 ----

type dfEntry struct {
	Target     string
	Fstype     string
	Total      interface{}
	Used       interface{}
	Avail      interface{}
	UsePercent string
}

type mountEntry struct {
	Mountpoint string
	Fstype     string
	Options    string
}

type partSector struct {
	StartSector int64
	SizeSectors int64
}

// DiskInfoCache 启动时一次性加载的磁盘信息缓存
type DiskInfoCache struct {
	mu                sync.RWMutex
	disks             []DiskSummary
	smartctlAvailable bool
	loaded            bool
}

// NewDiskInfoCache 创建磁盘信息缓存
func NewDiskInfoCache() *DiskInfoCache {
	return &DiskInfoCache{}
}

// Disks 返回缓存的磁盘列表（只读拷贝）
func (c *DiskInfoCache) Disks() []DiskSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]DiskSummary, len(c.disks))
	copy(out, c.disks)
	return out
}

// IsSmartctlAvailable smartctl 是否可用
func (c *DiskInfoCache) IsSmartctlAvailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.smartctlAvailable
}

// IsLoaded 是否已加载
func (c *DiskInfoCache) IsLoaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

// Reload 强制重新收集所有磁盘信息
func (c *DiskInfoCache) Reload() {
	disks, scOK := buildDiskSummary()
	c.mu.Lock()
	c.disks = disks
	c.smartctlAvailable = scOK
	c.loaded = true
	c.mu.Unlock()
	logger.Info("diskinfo", "reloaded disk cache: %d disks, smartctl=%v", len(disks), scOK)
}

// DeviceBySerial 按序列号查当前设备路径与分区路径集合。
// 返回 (baseDevPath, partitionPathSet, ok)。ok=false 表示未找到该 SN（缓存未就绪或无此盘）。
// partitionPathSet 可直接用于 isMountedMulti 校验 /proc/mounts 中设备归属（换盘/换口后自动对齐）。
func (c *DiskInfoCache) DeviceBySerial(serial string) (base string, parts map[string]bool, ok bool) {
	if serial == "" {
		return "", nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, sd := range c.disks {
		if sd.Serial != serial {
			continue
		}
		base = sd.Path
		parts = make(map[string]bool)
		if base != "" {
			parts[base] = true // 整盘路径也纳入，兼容整盘直接挂载（无分区表）的场景
		}
		for _, p := range sd.Partitions {
			collectPartitionPaths(p, parts)
		}
		return base, parts, true
	}
	return "", nil, false
}

// collectPartitionPaths 递归收集分区及其子分区的路径到 out。
func collectPartitionPaths(p Partition, out map[string]bool) {
	if p.Path != "" {
		out[p.Path] = true
	}
	for _, child := range p.Children {
		collectPartitionPaths(child, out)
	}
}

// ============================================================
// 数据采集方法（参考 zdaitest001_go/collector.go）
// ============================================================

// collectLsblk lsblk -J -b -o NAME,TRAN,TYPE,FSTYPE,PTTYPE,SIZE,MODEL,SERIAL,ROTA,MOUNTPOINTS
func collectLsblk() LsblkData {
	var lsblk LsblkData
	lsblkBin := findLsblk()
	if lsblkBin == "" {
		return lsblk
	}
	output, err := runCmdOutput(lsblkBin, 5*time.Second, "-J", "-b", "-o",
		"NAME,TRAN,TYPE,FSTYPE,PTTYPE,SIZE,MODEL,SERIAL,ROTA,MOUNTPOINTS")
	if err != nil {
		output2, err2 := runCmd(lsblkBin, 5*time.Second, "-J", "-b", "-o",
			"NAME,TRAN,TYPE,FSTYPE,PTTYPE,SIZE,MODEL,SERIAL,ROTA,MOUNTPOINTS")
		if err2 != nil {
			return lsblk
		}
		output = output2
	}
	json.Unmarshal(output, &lsblk)
	return lsblk
}

// ============================================================
// smartctl 执行（自动降级 sudo）
// 关键：smartctl -J/-Aj 模式会把错误信息放在 **stdout JSON** 里（messages 数组），
// stderr 通常为空。Permission denied 检测必须同时查 stdout 和 stderr！
//
// 策略：
//   1. 普通 smartctl 执行
//   2. exit code != 0 且 stdout/stderr 任一含 Permission denied → sudo -n 重试
//   3. sudo -n 非交互：免密则成功，有密码/无 sudo 则快速失败（不阻塞 UI）
//
// 附加策略（drivetemp）：如果 fnOS 内核有 drivetemp 模块（6.18.18 >= 5.5），
// modprobe drivetemp 后 SATA 盘温度会出现在 /sys/class/hwmon 下，
// collectHwmonTemps() 自动采集——无需 smartctl、无需 root！
// ============================================================

// runSmartctl 执行 smartctl，返回 stdout、stderr、error。
// 内部处理 sudo fallback，调用方只传 smartctl 二进制路径 + 参数。
func runSmartctl(smartctlBin string, args ...string) (stdout, stderr string, err error) {
	// 1) 普通执行
	stdout, stderr, err = execCombinedOutput(smartctlBin, args...)

	// 2) Permission denied → 尝试 sudo -n smartctl
	//    注意：smartctl -J 把错误放在 stdout JSON 里，stderr 可能为空
	if err != nil && isPermDenied(stdout, stderr) {
		if sudoBin := findSudo(); sudoBin != "" {
			sudoArgs := append([]string{"-n", smartctlBin}, args...)
			s2, e2, err2 := execCombinedOutput(sudoBin, sudoArgs...)
			if err2 == nil {
				// sudo 成功 → 覆盖结果
				stdout, stderr, err = s2, e2, nil
				return
			}
			// sudo 也失败，返回原始结果（带 exit status 2 + Permission denied JSON）
		}
	}
	return
}

// isPermDenied 同时检查 stdout 和 stderr（smartctl -J 错误在 stdout）
func isPermDenied(stdout, stderr string) bool {
	check := stdout + " " + stderr
	return strings.Contains(check, "Permission denied") ||
		strings.Contains(check, "权限") ||
		strings.Contains(check, "EACCES") ||
		strings.Contains(check, "failed to open") ||
		strings.Contains(check, "Smartctl open device")
}

// findSudo 定位 sudo 二进制
func findSudo() string {
	if p, err := exec.LookPath("sudo"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/bin/sudo", "/bin/sudo"} {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0111 != 0 {
			return p
		}
	}
	return ""
}

// execCombinedOutput 执行命令并分别收集 stdout/stderr（带 10s 超时，避免 smartctl/modprobe 卡死）。
func execCombinedOutput(name string, args ...string) (stdout, stderr string, err error) {
	return execCombinedOutputCtx(name, 10*time.Second, args...)
}

// ============================================================
// drivetemp 内核模块自动探测（hwmon 读 SATA 温度的前提，无需 smartctl/root）
// 内核 >= 5.5 有 CONFIG_SENSORS_DRIVETEMP，fnOS 6.18.18 肯定支持
// modprobe drivetemp 成功后，/sys/class/hwmon/hwmonX/name = drivetemp
// collectHwmonTemps() 已经扫描 drivetemp → 自动拿到 SATA 盘温度
// 这是绕开 smartctl 权限问题最干净的方案！
// ============================================================

// tryProbeDrivetemp 尝试加载 drivetemp 内核模块
// 返回是否成功（加载成功或已加载）
func tryProbeDrivetemp() bool {
	// 1) 先检查是否已经加载：modinfo 或 /sys/class/hwmon 下是否已有 drivetemp
	if isDrivetempLoaded() {
		logger.Info("diskinfo", "drivetemp module already loaded, SATA disk temps via hwmon OK")
		return true
	}

	// 2) 尝试 modprobe drivetemp（普通用户可能没权限 → 如果 fnOS 有 sudo 免密则自动降级）
	modprobe := findModprobe()
	if modprobe == "" {
		logger.Warn("diskinfo", "modprobe not found, cannot probe drivetemp")
		return false
	}

	// 先直接试
	_, stderr, err := execCombinedOutput(modprobe, "drivetemp")
	if err == nil {
		logger.Info("diskinfo", "modprobe drivetemp OK — SATA disk temperatures will flow through hwmon")
		return true
	}

	// 权限不足 → sudo -n modprobe drivetemp
	if strings.Contains(stderr, "Operation not permitted") || strings.Contains(stderr, "Permission denied") {
		if sudoBin := findSudo(); sudoBin != "" {
			_, e2, err2 := execCombinedOutput(sudoBin, "-n", modprobe, "drivetemp")
			if err2 == nil {
				logger.Info("diskinfo", "sudo modprobe drivetemp OK — SATA disk temperatures will flow through hwmon")
				return true
			}
			logger.Debug("diskinfo", "sudo modprobe drivetemp failed: %s", strings.TrimSpace(e2))
		}
	}

	logger.Warn("diskinfo", "modprobe drivetemp failed (%s) — SATA temps need smartctl + root", strings.TrimSpace(stderr))
	return false
}

// isDrivetempLoaded 检查 /sys/class/hwmon 下是否已有 name = drivetemp 的条目
func isDrivetempLoaded() bool {
	dirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	sort.Strings(dirs)
	for _, d := range dirs {
		name := readTrim(filepath.Join(d, "name"))
		if name == "drivetemp" {
			return true
		}
	}
	return false
}

// findModprobe 定位 modprobe
func findModprobe() string {
	if p, err := exec.LookPath("modprobe"); err == nil {
		return p
	}
	for _, p := range []string{"/sbin/modprobe", "/usr/sbin/modprobe"} {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0111 != 0 {
			return p
		}
	}
	return ""
}

// collectSmartctl smartctl -Aj，每块盘一个 *SmartctlData（nil 表示无数据）
func collectSmartctl() (map[string]*SmartctlData, bool) {
	result := make(map[string]*SmartctlData)
	path := findSmartctl()
	if path == "" {
		return result, false
	}

	for _, d := range listSysBlockDisks() {
		var dev string
		if strings.HasPrefix(d, "nvme") {
			dev = "/dev/" + nvmeControllerName(d)
		} else {
			dev = "/dev/" + d
		}
		// runSmartctl 内部自动处理 sudo fallback
		stdout, stderr, err := runSmartctl(path, "-Aj", dev)
		if err != nil {
			logger.Debug("diskinfo", "smartctl %s failed: err=%v stderr=%s", dev, err, strings.TrimSpace(stderr))
			result[dev] = nil
			continue
		}
		var sc SmartctlData
		if err := json.Unmarshal([]byte(stdout), &sc); err != nil {
			result[dev] = nil
			continue
		}
		result[dev] = &sc
	}
	return result, true
}

// collectHwmonTemps hwmon 温度: disk_name -> []TempReading
// 使用 EvalSymlinks 真实路径比对，比字符串 split 更准确
func collectHwmonTemps() map[string][]TempReading {
	temps := make(map[string][]TempReading)

	// 建立 sysfs device 真实路径 -> disk_name 映射
	diskDevMap := make(map[string]string)
	for _, d := range listSysBlockDisks() {
		rp, err := filepath.EvalSymlinks(filepath.Join("/sys/block", d, "device"))
		if err != nil {
			continue
		}
		diskDevMap[rp] = d
	}

	// 遍历 hwmon
	hwmonDirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	sort.Strings(hwmonDirs)

	for _, h := range hwmonDirs {
		if info, err := os.Stat(h); err != nil || !info.IsDir() {
			continue
		}
		name := readTrim(filepath.Join(h, "name"))
		if name != "drivetemp" && name != "nvme" {
			continue
		}
		rp, err := filepath.EvalSymlinks(filepath.Join(h, "device"))
		if err != nil {
			continue
		}
		dn, ok := diskDevMap[rp]
		if !ok || dn == "" {
			continue
		}

		var tempReadings []TempReading
		tempFiles, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		sort.Strings(tempFiles)

		for _, tf := range tempFiles {
			val := readTrim(tf)
			if val == "" {
				continue
			}
			celsius, err := strconv.Atoi(val)
			if err != nil {
				continue
			}
			label := readTrim(strings.Replace(tf, "_input", "_label", 1))
			if label == "" {
				label = filepath.Base(tf)
			}
			tempReadings = append(tempReadings, TempReading{
				Label:   label,
				Celsius: celsius / 1000,
			})
		}
		if len(tempReadings) > 0 {
			temps[dn] = tempReadings
		}
	}
	return temps
}

// collectDF df -B1 --output=source,target,fstype,size,used,avail,pcent
func collectDF() map[string][]dfEntry {
	dfMap := make(map[string][]dfEntry)
	dfBin := findTool("df")
	if dfBin == "" {
		return dfMap
	}
	output, _ := runCmdOutput(dfBin, 5*time.Second, "-B1", "--output=source,target,fstype,size,used,avail,pcent")
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) <= 1 {
		return dfMap
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		dfMap[fields[0]] = append(dfMap[fields[0]], dfEntry{
			Target:     fields[1],
			Fstype:     fields[2],
			Total:      parseDFValue(fields[3]),
			Used:       parseDFValue(fields[4]),
			Avail:      parseDFValue(fields[5]),
			UsePercent: fields[6],
		})
	}
	return dfMap
}

// collectMounts /proc/mounts: source -> []mountEntry
func collectMounts() map[string][]mountEntry {
	mountsMap := make(map[string][]mountEntry)
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return mountsMap
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		tgt := strings.ReplaceAll(fields[1], "\\040", " ")
		mountsMap[fields[0]] = append(mountsMap[fields[0]], mountEntry{
			Mountpoint: tgt,
			Fstype:     fields[2],
			Options:    fields[3],
		})
	}
	return mountsMap
}

// collectPartSectors sysfs 分区 start/size 扇区: part_name -> partSector
func collectPartSectors() map[string]partSector {
	result := make(map[string]partSector)
	for _, d := range listSysBlockDisks() {
		base := filepath.Join("/sys/block", d)
		partPaths, _ := filepath.Glob(filepath.Join(base, d+"*"))
		sort.Strings(partPaths)
		for _, p := range partPaths {
			startFile := filepath.Join(p, "start")
			if _, err := os.Stat(startFile); err != nil {
				continue
			}
			pn := filepath.Base(p)
			start, _ := strconv.ParseInt(readTrim(startFile), 10, 64)
			size, _ := strconv.ParseInt(readTrim(filepath.Join(p, "size")), 10, 64)
			result[pn] = partSector{StartSector: start, SizeSectors: size}
		}
	}
	return result
}

// ---- 递归收集分区 ----

func collectPartitions(node LsblkDevice, dfMap map[string][]dfEntry, mountsMap map[string][]mountEntry, partSectors map[string]partSector) []Partition {
	var parts []Partition

	for _, child := range node.Children {
		cn := child.Name
		cpath := "/dev/" + cn

		// 挂载 + 用量
		var mountsList []Mount
		for _, m := range mountsMap[cpath] {
			entry := Mount{
				Mountpoint: m.Mountpoint,
				Fstype:     m.Fstype,
				Options:    m.Options,
			}
			for _, dfE := range dfMap[cpath] {
				if dfE.Target == m.Mountpoint {
					entry.Total = dfE.Total
					entry.Used = dfE.Used
					entry.Avail = dfE.Avail
					entry.UsePercent = dfE.UsePercent
					break
				}
			}
			mountsList = append(mountsList, entry)
		}

		// lsblk mountpoints（补充 /proc/mounts 未列出的）
		for _, mp := range child.Mountpoints {
			if mp == "" {
				continue
			}
			found := false
			for _, m := range mountsList {
				if m.Mountpoint == mp {
					found = true
					break
				}
			}
			if !found {
				mountsList = append(mountsList, Mount{
					Mountpoint: mp,
					Fstype:     child.Fstype,
				})
			}
		}

		pi := Partition{
			Name:      cn,
			Path:      cpath,
			Type:      child.Type,
			Fstype:    child.Fstype,
			SizeBytes: child.Size,
			SizeHuman: formatSize(child.Size),
			Mounts:    mountsList,
		}
		if ps, ok := partSectors[cn]; ok {
			pi.StartSector = ps.StartSector
			pi.SizeSectors = ps.SizeSectors
		}
		if len(child.Children) > 0 {
			pi.Children = collectPartitions(child, dfMap, mountsMap, partSectors)
		}
		parts = append(parts, pi)
	}
	return parts
}

// ============================================================
// 温度策略（hwmon 优先，SMART 兜底，均无则 nil）
// ============================================================

// getTemperatures 温度优先级策略（参考 zdaitest001_go/temperature.go）:
// 1. hwmon 有数据则全部测量点输出
// 2. hwmon 无数据时从 smartctl JSON 读取（temperature.current → nvme temperature → temperature_sensors[]）
// 3. 均无数据返回 nil
func getTemperatures(name string, sc *SmartctlData, hwmonTemps map[string][]TempReading) []TempReading {
	// 1. hwmon 优先（多测量点）
	if temps, ok := hwmonTemps[name]; ok && len(temps) > 0 {
		return temps
	}

	// 2. SMART 兜底
	if sc != nil {
		isNvme := strings.HasPrefix(name, "nvme")
		var temps []TempReading

		// temperature.current（SATA 和部分 NVMe）
		if sc.Temperature != nil && sc.Temperature.Current != nil {
			label := "Current"
			if isNvme {
				label = "Composite"
			}
			temps = append(temps, TempReading{Label: label, Celsius: *sc.Temperature.Current})
		}

		// NVMe SMART 健康信息
		if sc.NvmeSmartHealth != nil {
			// 如果还没有温度，用 nvme temperature
			if len(temps) == 0 && sc.NvmeSmartHealth.Temperature != nil {
				temps = append(temps, TempReading{Label: "Composite", Celsius: *sc.NvmeSmartHealth.Temperature})
			}
			// 温度传感器数组
			for i, s := range sc.NvmeSmartHealth.TemperatureSensors {
				temps = append(temps, TempReading{
					Label:   fmt.Sprintf("Sensor %d", i+1),
					Celsius: s,
				})
			}
		}

		if len(temps) > 0 {
			return temps
		}
	}

	// 3. 均无数据
	return nil
}

// ============================================================
// 全量磁盘信息构建（入口）
// ============================================================

// buildDiskSummary 收集并整合所有磁盘信息（参考 zdaitest001_go/buildDiskSummary）
func buildDiskSummary() ([]DiskSummary, bool) {
	lsblk := collectLsblk()
	smartctlData, smartctlOK := collectSmartctl()
	hwmonTemps := collectHwmonTemps()
	dfMap := collectDF()
	mountsMap := collectMounts()
	partSectors := collectPartSectors()

	var summary []DiskSummary

	for _, disk := range lsblk.Blockdevices {
		if disk.Type != "disk" {
			continue
		}
		name := disk.Name
		path := "/dev/" + name
		tran := disk.Tran
		sc := smartctlData[path]

		// 型号 / 厂商 / 序列号
		model := disk.Model
		if model == "" && sc != nil {
			model = sc.ModelName
		}
		if model == "" && sc != nil {
			model = sc.Product
		}
		model = strings.TrimSpace(model)

		vendor := ""
		if sc != nil {
			vendor = strings.TrimSpace(sc.Vendor)
		}

		serial := disk.Serial
		if serial == "" && sc != nil {
			serial = sc.SerialNumber
		}
		// SATA 盘 lsblk 的 SERIAL 列常见为空：经 /dev/disk/by-id 反推补齐
		if serial == "" {
			serial = diskByIDSerial(name, model)
		}
		serial = strings.TrimSpace(serial)

		// 大小
		sizeBytes := disk.Size
		if sizeBytes == 0 && sc != nil && sc.UserCapacity != nil {
			sizeBytes = sc.UserCapacity.Bytes
		}

		// SSD/HDD 判定（按优先级）
		var diskType string
		var rotationRate interface{}

		if tran == "nvme" {
			diskType = "SSD"
		} else if sc != nil && sc.RotationRate != nil {
			rv := sc.RotationRate.Value
			rn := sc.RotationRate.Name
			if rv == 0 || strings.Contains(rn, "Solid State") {
				diskType = "SSD"
			} else {
				diskType = "HDD"
			}
			if rn != "" {
				rotationRate = rn
			} else {
				rotationRate = rv
			}
		} else {
			// 回退到 lsblk rota
			if disk.Rota != nil && !*disk.Rota {
				diskType = "SSD"
			} else {
				diskType = "HDD"
			}
		}

		// 温度
		temperature := getTemperatures(name, sc, hwmonTemps)

		// 分区
		partitions := collectPartitions(disk, dfMap, mountsMap, partSectors)

		summary = append(summary, DiskSummary{
			Name:         name,
			Path:         path,
			Model:        model,
			Vendor:       vendor,
			Serial:       serial,
			SizeBytes:    sizeBytes,
			SizeHuman:    formatSize(sizeBytes),
			Pttype:       disk.Pttype,
			Tran:         tran,
			DiskType:     diskType,
			RotationRate: rotationRate,
			Temperature:  temperature,
			Partitions:   partitions,
		})
	}

	return summary, smartctlOK
}

// ============================================================
// 辅助：从 smartctl JSON 读取单盘温度（供 ThermalManager 兜底使用）
// 返回最高温度摄氏度（hwmon 优先，SMART 兜底）
// ============================================================

// smartctlJSONDiskTemp 通过 smartctl -Aj JSON 解析单块盘的温度
// 与 getTemperatures 逻辑一致，但只返回一个 float64（最高温度）
func smartctlJSONDiskTemp(devName string) (float64, error) {
	smartctlBin := findSmartctl()
	if smartctlBin == "" {
		return 0, fmt.Errorf("smartctl not found")
	}

	var devPath string
	if strings.HasPrefix(devName, "nvme") {
		devPath = "/dev/" + nvmeControllerName(devName)
	} else {
		devPath = "/dev/" + devName
	}

	stdout, stderr, err := runSmartctl(smartctlBin, "-Aj", devPath)
	if err != nil {
		return 0, fmt.Errorf("smartctl -Aj %s failed: %w (stderr: %s)", devPath, err, strings.TrimSpace(stderr))
	}

	var sc SmartctlData
	if err := json.Unmarshal([]byte(stdout), &sc); err != nil {
		return 0, fmt.Errorf("smartctl JSON parse failed: %w", err)
	}

	temps := getTemperatures(devName, &sc, nil)
	if len(temps) == 0 {
		return 0, fmt.Errorf("no temperature data in smartctl JSON")
	}

	// 取最高温度
	var maxC int
	for _, t := range temps {
		if t.Celsius > maxC {
			maxC = t.Celsius
		}
	}
	return float64(maxC), nil
}
