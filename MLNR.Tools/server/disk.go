package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mlnr/logger"
)

// ============================================================
// DiskManager 硬盘组管理器
//
// 依据 FS3.1.md：
//   - 硬盘组复用硬件开关通道(SW1~SW4)控制硬盘电源
//   - 上线流程：SW ON → 等待 PowerOnDelay → 检测块设备 → 自动挂载
//   - 下线流程（安全）：卸载挂载点 → 停转机械盘(SAFE STANDBY) → SW OFF
//   - 强制下线：直接 SW OFF（可能损坏机械盘，需前端二次确认）
//   - NVMe SSD 无需停转指令
//   - 挂载状态从 fnOS 系统挂载表读取(/proc/mounts)
// ============================================================

// DiskManager 硬盘组管理器。
type DiskManager struct {
	mu      sync.RWMutex
	ble     *BLEManager
	store   *Store
	hub     *Hub
	thermal *ThermalManager   // 温度管理器（3.md：按 SN 关联磁盘温度，可 nil）
	config  []DiskGroupConfig // 来自 settings
	cache   *DiskInfoCache    // 启动时加载的磁盘信息全量缓存
	swOnAt  map[int]time.Time // SW 通道最近一次 OFF→ON 的时间（上电过渡容错用）
	sched   TaskRunner        // 计划任务触发接口（上电后/下电前任务；main 装配后注入，可 nil）
	offTaskTimeout time.Duration // 下电前任务同步等待总超时上限（0=默认 120s；测试可注入短值）
}

// NewDiskManager 创建硬盘组管理器。
func NewDiskManager(store *Store, hub *Hub) *DiskManager {
	s := store.GetSettings()
	return &DiskManager{
		store:  store,
		hub:    hub,
		config: s.DiskGroups,
		cache:  NewDiskInfoCache(),
		swOnAt: make(map[int]time.Time),
	}
}

// SetScheduler 注入计划任务触发接口（main 装配：sched 创建后调用）。
// 未注入时上电后/下电前任务自动跳过（仅记录错误）。
func (d *DiskManager) SetScheduler(sched TaskRunner) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sched = sched
}

// LoadDiskInfo 启动时一次性加载磁盘信息全量缓存（参考 zdaitest001_go buildDiskSummary）。
// 缓存成功后，后续 ListDisks/ListPartitions 都从内存读取，不再重复执行 lsblk/smartctl。
func (d *DiskManager) LoadDiskInfo() {
	// 优先尝试加载 drivetemp 内核模块 → SATA 盘温度走 hwmon（无需 smartctl/root）
	tryProbeDrivetemp()

	logger.Info("disk", "loading disk info cache (lsblk + smartctl + hwmon + df + mounts) ...")
	d.cache.Reload()
}

// RefreshDiskInfo 手动刷新磁盘信息缓存（SW 电源切换后调用，处理磁盘热插拔）。
func (d *DiskManager) RefreshDiskInfo() {
	d.cache.Reload()
}

// GetDiskCache 返回磁盘信息全量缓存（供 ThermalManager 等模块读取磁盘元数据，避免重复执行 lsblk）。
func (d *DiskManager) GetDiskCache() *DiskInfoCache {
	return d.cache
}

// IsDiskGroupOnlineByDiskSerial 判断指定序列号的磁盘是否属于某个已启用的硬盘组，
// 且该硬盘组对应的 SW 通道当前为 ONLINE（电平=1）。
// 用途：thermal.go 故障转速逻辑 — 硬盘组手动下线后，温度读取自然缺失，不应触发故障转速。
// 返回 false 表示：磁盘不属于任何启用组、或属于的组当前为下线状态、或磁盘 SN 为空。
func (d *DiskManager) IsDiskGroupOnlineByDiskSerial(serial string) bool {
	if serial == "" {
		return false
	}
	switchN, found := d.findGroupByDiskSerial(serial)
	if !found {
		return false
	}
	// 查 SW 通道状态
	if d.ble == nil {
		return false
	}
	for _, sw := range d.ble.GetSwitchStates() {
		if sw.Index == switchN && sw.State == 1 {
			return true
		}
	}
	return false
}

// findGroupByDiskSerial 返回指定 SN 所属（启用）硬盘组的 SW 通道号；未找到返回 false。
func (d *DiskManager) findGroupByDiskSerial(serial string) (int, bool) {
	if serial == "" {
		return 0, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, g := range d.config {
		if !g.Enabled {
			continue
		}
		for _, disk := range g.Disks {
			if disk.Serial != "" && disk.Serial == serial {
				return g.SwitchN, true
			}
		}
	}
	return 0, false
}

// MarkSwitchOn 记录 SW 通道最近一次 OFF→ON 的时间（PowerOn / OnSwitchChanged 调用），
// 供 thermal.go 判断硬盘是否处于"上电过渡期"（温度尚未可读，不应触发故障转速）。
func (d *DiskManager) MarkSwitchOn(swN int) {
	d.mu.Lock()
	if d.swOnAt == nil {
		d.swOnAt = make(map[int]time.Time)
	}
	d.swOnAt[swN] = time.Now()
	d.mu.Unlock()
}

// IsDiskGroupPoweringUp 判断指定 SN 所属硬盘组是否处于上电过渡期：
// 组在线（SW=1）且 SW 最近从 OFF→ON 的时刻距今不足 grace。
// 用途：thermal.go — 硬盘刚上线、温度/缓存尚未就绪时视为上电中，不触发故障转速。
func (d *DiskManager) IsDiskGroupPoweringUp(serial string, grace time.Duration) bool {
	switchN, found := d.findGroupByDiskSerial(serial)
	if !found {
		return false
	}
	d.mu.RLock()
	onAt, ok := d.swOnAt[switchN]
	d.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Since(onAt) >= grace {
		return false
	}
	// 组确实在线才视为上电中（SW 又关了则不是）
	if d.ble == nil {
		return false
	}
	for _, sw := range d.ble.GetSwitchStates() {
		if sw.Index == switchN && sw.State == 1 {
			return true
		}
	}
	return false
}

// SetBLE 注入 BLE 管理器（main.go 启动时调用）。
func (d *DiskManager) SetBLE(b *BLEManager) {
	d.ble = b
}

// SetThermal 注入温度管理器（main.go 启动时调用；3.md：按 SN 关联磁盘温度）。
func (d *DiskManager) SetThermal(t *ThermalManager) {
	d.thermal = t
}

// UpdateConfig 更新硬盘组配置（settings 变更时调用）。
func (d *DiskManager) UpdateConfig(groups []DiskGroupConfig) {
	d.mu.Lock()
	d.config = groups
	d.mu.Unlock()
	d.broadcast()
}

// GetViews 返回所有硬盘组的实时视图。
func (d *DiskManager) GetViews() []DiskGroupView {
	d.mu.RLock()
	defer d.mu.RUnlock()

	switches := d.ble.GetSwitchStates()
	switchOn := make(map[int]bool, MaxSwitchChannels)
	switchEnabled := make(map[int]bool, MaxSwitchChannels)
	for _, sw := range switches {
		switchOn[sw.Index] = sw.State == 1
		switchEnabled[sw.Index] = sw.Enabled
	}
	mounts := readSystemMounts()

	// 检查 SW 通道冲突（多组绑定同一 SW）
	// #Fix：冲突检测以固件 SW ENABLED 为准（与下方 Enabled 口径一致）
	swRefCount := make(map[int]int)
	for _, g := range d.config {
		en := g.Enabled
		if v, ok := switchEnabled[g.SwitchN]; ok {
			en = v
		}
		if en {
			swRefCount[g.SwitchN]++
		}
	}

	// 磁盘温度（3.md：SN 为关联 key，无 SN 时回退设备路径匹配）
	var tempBySerial map[string]float64
	if d.thermal != nil {
		tempBySerial = d.thermal.DiskTemperatureMap()
	}
	tempByDev := make(map[string]float64, len(tempBySerial))
	for sn, v := range tempBySerial {
		tempByDev[sn] = v
	}

	out := make([]DiskGroupView, 0, len(d.config))
	for _, g := range d.config {
		// #Fix：启用状态以固件 SW ENABLED 为权威（与固件/硬盘组通道设置页显示一致），
		// 本地配置仅作固件无数据（未连接）时的兜底。
		enabled := g.Enabled
		if v, ok := switchEnabled[g.SwitchN]; ok {
			enabled = v
		}
		view := DiskGroupView{
			ID:         g.ID,
			Alias:      g.Alias,
			SwitchN:    g.SwitchN,
			Enabled:    enabled,
			AutoOnline: g.AutoOnline,
			Online:     switchOn[g.SwitchN],
			Conflict:   swRefCount[g.SwitchN] > 1,
			Disks:      []DiskView{},
		}
		for _, disk := range g.Disks {
			// 3.md：SN 优先匹配温度，SN 为空回退设备路径
			var temp *float64
			if v, ok := tempByDev[disk.Serial]; ok && disk.Serial != "" {
				t := v
				temp = &t
			} else if v, ok := tempByDev[disk.Device]; ok {
				t := v
				temp = &t
			}
			// 按序列号解析当前设备路径与分区集合（换盘/换口后 /dev/sdX 可能变化，
			// 配置里存的是旧路径；这里用 SN 从缓存查出当前路径与分区，传给 isMountedMulti
			// 做准确的挂载匹配）。缓存未就绪或 SN 未命中时 currentParts=nil，回退配置路径。
			resolvedDevice := disk.Device
			var currentParts map[string]bool
			if d.cache != nil && d.cache.IsLoaded() && disk.Serial != "" {
				if base, parts, ok := d.cache.DeviceBySerial(disk.Serial); ok {
					if base != "" {
						resolvedDevice = base
					}
					currentParts = parts
				}
			}
			mounted, mountPath := isMountedMulti(mounts, disk.Device, disk.MountPath, disk.Mounts, currentParts)
			dv := DiskView{
				Device:      resolvedDevice,
				Alias:       disk.Alias,
				Serial:      disk.Serial,
				MountPath:   mountPath,
				AutoMount:   disk.AutoMount,
				IsNVMe:      disk.IsNVMe,
				Mounted:     mounted,
				Temperature: temp,
			}
			view.Disks = append(view.Disks, dv)
		}
		out = append(out, view)
	}

	// #Fix：GetViews 内填充统计信息（此前仅 HTTP getStatus 填充，WS 推送复用 GetViews
	// 导致 stats 丢失、前端统计长期显示 0 / '—'）。HTTP 与 WS 路径统一带 stats。
	// 统计组件按 settings.diskStatsEnabled 按需计算（空 = 全部）。
	if dl := GetDiskLogger(); dl != nil {
		enabled := d.store.GetSettings().DiskStatsEnabled
		// 会话补偿：蓝牙已连接（固件状态可信）时，若某组存在未闭合会话但固件上报为离线
		// （整机断电/异常断电，真实下线事件丢失，日志最后事件是上线）→ 虚拟闭合，
		// 结束时间取上位机本次启动时刻，避免该会话时长永久丢失。未连接时跳过（状态未知，
		// 硬盘可能仍在线，不能误闭合）。
		if d.ble != nil && d.ble.IsEncryptedSession() {
			bootAt := dl.BootTime()
			for i := range out {
				if !out[i].Online {
					dl.ClosePendingSession(out[i].ID, bootAt)
				}
			}
		}
		for i := range out {
			var since time.Time
			if out[i].Online {
				since = dl.LastOnlineSince(out[i].ID)
			}
			stats := dl.GroupStats(out[i].ID, out[i].Alias, out[i].Online, since, enabled)
			out[i].Stats = &stats
		}
	}
	return out
}

// PowerOn 上线硬盘组：SW ON → 等待延迟 → 自动挂载 → 验证。
func (d *DiskManager) PowerOn(groupID int) (CommandResult, error) {
	g, err := d.findGroup(groupID)
	if err != nil {
		LogDiskEvent(groupID, "", DiskLogPowerOn, "", "fail", err.Error())
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	if !g.Enabled {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOn, "", "fail", "硬盘组未启用")
		return CommandResult{OK: false, Message: "硬盘组未启用"}, fmt.Errorf("group disabled")
	}

	// 1. 下发 SW ON
	res, err := d.ble.SetSwitch(g.SwitchN, 1)
	if err != nil {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOn, fmt.Sprintf("SW%d ON", g.SwitchN), "fail", err.Error())
		return res, err
	}
	if !res.OK {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOn, fmt.Sprintf("SW%d ON", g.SwitchN), "fail", res.Message)
		return res, fmt.Errorf("SW ON 失败: %s", res.Message)
	}
	// 记录上电过渡起点（thermal.go 上电容错用）
	d.MarkSwitchOn(g.SwitchN)

	// 2. 等待硬件开机延迟（让磁盘就绪）
	delay := d.getSwitchPowerOnDelay(g.SwitchN)
	if delay > 0 {
		logger.Info("disk", "group %d: waiting %dms for power-on delay", groupID, delay)
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	// 3. 验证 SW 电平确实为 1
	if !d.verifySwitchLevel(g.SwitchN, 1) {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOn, fmt.Sprintf("SW%d ON (verify)", g.SwitchN), "fail", "电源状态未生效")
		return CommandResult{OK: false, Message: "电源状态未生效：SW 通道电平未达到 ON"}, fmt.Errorf("switch state verify failed")
	}

	// 4. 等待磁盘就绪（默认最多 8s；配置了"上电后等待时间"时按配置的秒数等待）
	readyTimeout := 8 * time.Second
	if g.PowerOnDelaySec > 0 {
		readyTimeout = time.Duration(g.PowerOnDelaySec) * time.Second
	}
	d.waitDisksReady(g, readyTimeout)

	// 4.1 尽快刷新磁盘缓存：设备节点出现即可读到 SN，
	// 让 ThermalManager 下一轮就能按 SN 关联温度，避免"上线后仍用旧缓存 → 误判参考点离线"。
	d.RefreshDiskInfo()

	// 5. 等待 3 秒让 fnOS 系统自动挂载先生效；已自动挂载的分区跳过手动挂载
	time.Sleep(3 * time.Second)
	mounts := readSystemMounts()

	// 6. 自动挂载（AutoMount=true 且尚未被系统自动挂载的盘）
	var mountMsgs []string
	var mountFail bool
	for _, disk := range g.Disks {
		if !disk.AutoMount {
			continue
		}
		if d.isDiskMounted(disk, mounts) {
			logger.Info("disk", "group %d: %s already auto-mounted, skip", groupID, disk.Device)
			continue
		}
		if err := d.mountDisk(disk); err != nil {
			logger.Warn("disk", "group %d: mount %s failed: %v", groupID, disk.Device, err)
			mountMsgs = append(mountMsgs, disk.Device+": "+err.Error())
			mountFail = true
		}
	}

	// 7. 最终验证：SW 电平=1 + 配置了分区挂载的都已挂载
	mounts = readSystemMounts()
	var mountVerifyFail bool
	for _, disk := range g.Disks {
		if len(disk.Mounts) > 0 {
			for _, m := range disk.Mounts {
				if m.MountPoint != "" && !isMounted(mounts, m.Partition, m.MountPoint) {
					mountVerifyFail = true
					mountMsgs = append(mountMsgs, fmt.Sprintf("%s:%s 未挂载", m.Partition, m.MountPoint))
				}
			}
		} else if disk.MountPath != "" && !isMounted(mounts, disk.Device, disk.MountPath) {
			mountVerifyFail = true
			mountMsgs = append(mountMsgs, fmt.Sprintf("%s 未挂载到 %s", disk.Device, disk.MountPath))
		}
	}

	// 成功判定：SW ON OK + (磁盘验证 OK) + (挂载无必须失败)
	result := "ok"
	errMsg := ""
	if mountFail || mountVerifyFail {
		result = "fail"
		errMsg = strings.Join(mountMsgs, "; ")
	}
	LogDiskEvent(groupID, g.Alias, DiskLogPowerOn,
		fmt.Sprintf("SW%d ON, %d disks", g.SwitchN, len(g.Disks)),
		result, errMsg)

	// #Fix：上线后无条件重新收集硬盘信息并更新缓存（不论挂载结果）——
	// ThermalManager 按 SN 关联温度参考点时若缓存仍是旧列表，读不到刚上线的硬盘，
	// 风扇会误判全部参考点离线进入故障模式。尽早刷新（步骤 4.1）之外，末尾再兜底一次，
	// 覆盖"系统自动挂载晚于本流程"的情形。
	d.RefreshDiskInfo()

	// 8. 上电后任务（上电流程完成后异步触发执行，不再介入，交由调度引擎管理；
	// 触发失败仅记日志，不阻断上线）
	if err := d.runGroupTask(g.OnTask, false); err != nil {
		logger.Warn("disk", "group %d on-task failed: %v", groupID, err)
	}

	d.broadcast()
	if result == "ok" {
		return CommandResult{OK: true, Message: "硬盘组已上线"}, nil
	}
	return CommandResult{OK: false, Message: "硬盘组上线成功但部分挂载失败"}, nil
}

// verifySwitchLevel 读取当前 SW 电平，期望值 0 或 1，允许 1s 内等待
func (d *DiskManager) verifySwitchLevel(swN int, expect int) bool {
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		for _, sw := range d.ble.GetSwitchStates() {
			if sw.Index == swN && sw.State == expect {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitDisksReady 等待配置的磁盘出现在系统块设备清单中（SW ON 后磁盘可能需要几秒就绪）。
func (d *DiskManager) waitDisksReady(g *DiskGroupConfig, timeout time.Duration) {
	if len(g.Disks) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	var pending []string
	for time.Now().Before(deadline) {
		pending = nil
		for _, disk := range g.Disks {
			if disk.Serial == "" && disk.Device == "" {
				continue
			}
			if !d.isDiskPresent(disk) {
				pending = append(pending, disk.Serial)
			}
		}
		if len(pending) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	logger.Warn("disk", "group %d: disks not ready after %s: %v", g.ID, timeout, pending)
}

// isDiskPresent 检查指定 DiskEntry 的设备是否在系统中就绪。
func (d *DiskManager) isDiskPresent(disk DiskEntry) bool {
	if d.cache != nil && d.cache.IsLoaded() {
		for _, sd := range d.cache.Disks() {
			if disk.Serial != "" && sd.Serial == disk.Serial {
				return true
			}
			if disk.Device != "" && sd.Path == disk.Device {
				return true
			}
		}
	}
	// 回退：尝试访问 /sys/block
	if disk.Device != "" {
		if _, err := os.Stat("/sys/block/" + strings.TrimPrefix(disk.Device, "/dev/")); err == nil {
			return true
		}
	}
	return false
}

// PowerOff 安全下线硬盘组：卸载 → 停转 → SW OFF → 验证。
// action 为卸载失败后的处理方式：
//   - ""    ：尝试正常卸载；失败且检测到占用进程时返回 Decision=unmount_fail_occupied（前端弹窗选择）
//   - "kill"：终止占用进程后重试卸载，然后继续下线
//   - "force"：umount -f 强制卸载，然后继续下线
func (d *DiskManager) PowerOff(groupID int, action string) (CommandResult, error) {
	g, err := d.findGroup(groupID)
	if err != nil {
		LogDiskEvent(groupID, "", DiskLogPowerOff, "", "fail", err.Error())
		return CommandResult{OK: false, Message: err.Error()}, err
	}

	// 0. 下电前任务（整个下电流程开始前同步触发执行，等待执行完成：
	// 必须执行成功才继续正常下电；执行失败/任务丢失记录日志并终止正常下电）
	if err := d.runGroupTask(g.OffTask, true); err != nil {
		logger.Error("disk", "group %d off-task failed, abort normal power off: %v", groupID, err)
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOff, "下电前任务", "fail", err.Error())
		return CommandResult{OK: false, Message: "下电前任务执行失败，已终止下电：" + err.Error()}, err
	}

	// 1. 卸载所有挂载点（失败且被占用时按 action 处理，可能返回决策请求）
	// 卸载成功（全部挂载点已卸载）后，等待配置的"下线等待时间"（默认 2 秒），
	// 给系统留出 umount 完成后的释放时间，再执行停转与断电。
	if dr := d.unmountGroupDisks(g, action); dr != nil {
		return *dr, nil
	}
	time.Sleep(d.offlineWaitDelay(g))

	// 1. 停转机械盘（SATA HDD only，NVMe 跳过）
	for _, disk := range g.Disks {
		if disk.IsNVMe {
			continue
		}
		if err := d.standbyDisk(disk); err != nil {
			logger.Warn("disk", "group %d: standby %s failed: %v", groupID, disk.Device, err)
		}
	}

	// 2. 等待 2 秒让磁头归位
	time.Sleep(2 * time.Second)

	// 3. SW OFF
	res, err := d.ble.SetSwitch(g.SwitchN, 0)
	if err != nil {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOff,
			fmt.Sprintf("SW%d OFF", g.SwitchN), "fail", err.Error())
		return res, err
	}

	// 4. 验证 SW 电平 = 0
	swOK := d.verifySwitchLevel(g.SwitchN, 0)

	result := "ok"
	errMsg := ""
	if !swOK {
		result = "fail"
		errMsg = "电源关闭未生效"
	}
	LogDiskEvent(groupID, g.Alias, DiskLogPowerOff,
		fmt.Sprintf("SW%d OFF, %d disks", g.SwitchN, len(g.Disks)),
		result, errMsg)

	d.broadcast()
	if !swOK {
		return res, fmt.Errorf("SW OFF 未生效")
	}
	return CommandResult{OK: true, Message: "硬盘组已下线"}, nil
}

// unmountGroupDisks 卸载组内全部磁盘并验证。
// 正常卸载失败且检测到占用进程时：
//   - action==""   → 返回需要前端决策的 CommandResult（Decision=unmount_fail_occupied）
//   - action="kill" → 终止占用进程后重试卸载
//   - action="force" → umount -f 强制卸载
//
// 返回 nil 表示全部卸载成功（可继续下线）；返回非 nil 表示需决策或最终卸载失败。
func (d *DiskManager) unmountGroupDisks(g *DiskGroupConfig, action string) *CommandResult {
	var umountMsgs []string
	var umountFail bool
	for _, disk := range g.Disks {
		if err := d.unmountDisk(disk); err != nil {
			// 卸载报错：先确认是否仍挂载，再检测占用进程
			mounts := readSystemMounts()
			if d.diskStillMounted(disk, mounts) {
				procs := findOccupyProcs(d.diskMountTargets(disk))
				if len(procs) > 0 {
					switch action {
					case "kill":
						if failed := killProcs(procs); len(failed) > 0 {
							umountMsgs = append(umountMsgs, disk.Device+": 终止占用进程失败 "+strings.Join(failed, ", "))
							umountFail = true
							continue
						}
						// 进程已终止，等待进程释放句柄后重试卸载
						time.Sleep(500 * time.Millisecond)
						if err2 := d.unmountDisk(disk); err2 != nil {
							umountMsgs = append(umountMsgs, disk.Device+": "+err2.Error())
							umountFail = true
						}
					case "force":
						if errF := d.forceUnmountDisk(disk); errF != nil {
							umountMsgs = append(umountMsgs, disk.Device+": "+errF.Error())
							umountFail = true
						}
					default:
						// 返回决策请求：前端弹窗选择终止进程 / 强制卸载 / 取消
						return &CommandResult{
							OK:       false,
							Message:  fmt.Sprintf("硬盘组 %d 卸载失败：%d 个进程正在占用挂载点", g.ID, len(procs)),
							Decision: "unmount_fail_occupied",
							Occupied: procs,
						}
					}
				} else {
					umountMsgs = append(umountMsgs, disk.Device+": "+err.Error())
					umountFail = true
				}
			}
			// 已不再挂载（umountDisk 报错但挂载表已无该盘）：视为卸载完成
		}
	}

	// 验证：配置了挂载的分区应全部卸载成功
	mounts := readSystemMounts()
	for _, disk := range g.Disks {
		if len(disk.Mounts) > 0 {
			for _, m := range disk.Mounts {
				if m.MountPoint != "" && isMountPoint(mounts, m.MountPoint) {
					umountFail = true
					umountMsgs = append(umountMsgs, fmt.Sprintf("%s:%s 仍在挂载", m.Partition, m.MountPoint))
				}
			}
		} else if disk.MountPath != "" && isMountPoint(mounts, disk.MountPath) {
			umountFail = true
			umountMsgs = append(umountMsgs, fmt.Sprintf("%s 仍挂载在 %s", disk.Device, disk.MountPath))
		}
	}

	if umountFail {
		return &CommandResult{OK: false, Message: strings.Join(umountMsgs, "; ")}
	}
	return nil
}

// forceUnmountDisk 对硬盘全部挂载点执行 umount -f 强制卸载。
func (d *DiskManager) forceUnmountDisk(disk DiskEntry) error {
	var lastErr error
	for _, mp := range d.diskMountPoints(disk) {
		if err := forceUnmount(mp); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// ForcePowerOff 强制下电：先 umount -f 强制卸载已挂载分区（无论成败），
// 等待"下线等待时间"后直接 SW OFF（不执行安全停转）。
func (d *DiskManager) ForcePowerOff(groupID int) (CommandResult, error) {
	g, err := d.findGroup(groupID)
	if err != nil {
		LogDiskEvent(groupID, "", DiskLogPowerOff, "", "fail", err.Error())
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	logger.Warn("disk", "force power off group %d (no safe shutdown)", groupID)

	// 强制下电不受限：跳过下电前任务（紧急路径不做任何前置，直接卸载并断电）

	// 1. 检查并强制卸载已挂载分区（umount -f，成败均不中断）
	// 卸载目标 = 配置挂载点 ∪ 设备当前实际挂载点，只处理确实在表中的路径，
	// 避免配置与现状不一致时 umount -f 报 not mounted
	for _, disk := range g.Disks {
		targets := map[string]bool{}
		for _, mp := range d.diskMountPoints(disk) {
			targets[mp] = true
		}
		mounts := readSystemMounts()
		if disk.Device != "" {
			for _, mp := range mountsOfDevice(mounts, disk.Device) {
				targets[mp] = true
			}
		}
		for mp := range targets {
			if !isMountPoint(mounts, mp) {
				continue
			}
			if errF := forceUnmount(mp); errF != nil {
				logger.Warn("disk", "group %d: force unmount %s failed: %v", groupID, mp, errF)
			}
		}
	}
	// 无论是否卸载成功，等待配置的"下线等待时间"后继续（默认 2 秒）
	time.Sleep(d.offlineWaitDelay(g))

	res, err := d.ble.SetSwitch(g.SwitchN, 0)
	if err != nil {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOff,
			fmt.Sprintf("SW%d OFF", g.SwitchN), "fail", err.Error())
		return res, err
	}
	swOK := d.verifySwitchLevel(g.SwitchN, 0)
	if !swOK {
		LogDiskEvent(groupID, g.Alias, DiskLogPowerOff,
			fmt.Sprintf("SW%d OFF (verify)", g.SwitchN), "fail", "电源关闭未生效")
		return CommandResult{OK: false, Message: "电源关闭未生效：SW 通道电平未达到 OFF"}, fmt.Errorf("switch state verify failed")
	}
	LogDiskEvent(groupID, g.Alias, DiskLogPowerOff,
		fmt.Sprintf("SW%d OFF (no safe shutdown)", g.SwitchN), "ok", "强制下线")
	d.broadcast()
	return res, nil
}

// MountDisk 挂载单个硬盘（带验证 + 正确 Result 标记）。
func (d *DiskManager) MountDisk(groupID int, device string) (CommandResult, error) {
	g, err := d.findGroup(groupID)
	if err != nil {
		LogDiskEvent(groupID, "", DiskLogMount, device, "fail", err.Error())
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	for _, disk := range g.Disks {
		if disk.Device == device {
			if err := d.mountDisk(disk); err != nil {
				LogDiskEvent(groupID, g.Alias, DiskLogMount, device, "fail", err.Error())
				return CommandResult{OK: false, Message: err.Error()}, err
			}
			// 验证
			mounts := readSystemMounts()
			if !d.isDiskMounted(disk, mounts) {
				LogDiskEvent(groupID, g.Alias, DiskLogMount, device, "fail", "挂载后系统未检测到挂载点")
				return CommandResult{OK: false, Message: "挂载验证失败"}, nil
			}
			LogDiskEvent(groupID, g.Alias, DiskLogMount, device, "ok", "")
			d.broadcast()
			return CommandResult{OK: true, Message: "挂载成功"}, nil
		}
	}
	return CommandResult{OK: false, Message: "未找到硬盘"}, fmt.Errorf("disk not found")
}

// UnmountDisk 卸载单个硬盘（带验证 + 正确 Result 标记）。
// action："" 正常卸载（失败且被占用时返回决策请求）；"kill" 终止占用进程后重试；"force" umount -f。
func (d *DiskManager) UnmountDisk(groupID int, device string, action string) (CommandResult, error) {
	g, err := d.findGroup(groupID)
	if err != nil {
		LogDiskEvent(groupID, "", DiskLogUnmount, device, "fail", err.Error())
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	for _, disk := range g.Disks {
		if disk.Device == device {
			if err := d.unmountDisk(disk); err != nil {
				// 卸载失败：确认仍挂载后检测占用进程，按 action 处理
				mounts := readSystemMounts()
				if d.diskStillMounted(disk, mounts) {
					procs := findOccupyProcs(d.diskMountTargets(disk))
					if len(procs) > 0 {
						switch action {
						case "kill":
							if failed := killProcs(procs); len(failed) > 0 {
								LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "fail", "终止占用进程失败 "+strings.Join(failed, ", "))
								return CommandResult{OK: false, Message: "终止占用进程失败：" + strings.Join(failed, ", ")}, nil
							}
							time.Sleep(500 * time.Millisecond)
							if err2 := d.unmountDisk(disk); err2 != nil {
								LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "fail", err2.Error())
								return CommandResult{OK: false, Message: err2.Error()}, nil
							}
						case "force":
							if errF := d.forceUnmountDisk(disk); errF != nil {
								LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "fail", errF.Error())
								return CommandResult{OK: false, Message: errF.Error()}, nil
							}
						default:
							LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "fail", "进程占用")
							return CommandResult{
								OK:       false,
								Message:  fmt.Sprintf("%s 卸载失败：%d 个进程正在占用", device, len(procs)),
								Decision: "unmount_fail_occupied",
								Occupied: procs,
							}, nil
						}
					} else {
						LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "fail", err.Error())
						return CommandResult{OK: false, Message: err.Error()}, nil
					}
				}
				// 已不再挂载：视为卸载完成
			}
			// 验证：所有配置的挂载点都已卸载
			mounts := readSystemMounts()
			if d.diskStillMounted(disk, mounts) {
				LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "fail", "卸载后系统仍检测到挂载点")
				return CommandResult{OK: false, Message: "卸载验证失败"}, nil
			}
			LogDiskEvent(groupID, g.Alias, DiskLogUnmount, device, "ok", "")
			d.broadcast()
			return CommandResult{OK: true, Message: "卸载成功"}, nil
		}
	}
	return CommandResult{OK: false, Message: "未找到硬盘"}, fmt.Errorf("disk not found")
}

// isDiskMounted 检查 DiskEntry 是否有任一挂载点在 /proc/mounts 中
func (d *DiskManager) isDiskMounted(disk DiskEntry, procMounts []string) bool {
	if len(disk.Mounts) > 0 {
		for _, m := range disk.Mounts {
			if m.MountPoint == "" && m.Partition == "" {
				continue
			}
			if isMounted(procMounts, m.Partition, m.MountPoint) {
				return true
			}
		}
	}
	if disk.MountPath != "" {
		return isMounted(procMounts, disk.Device, disk.MountPath)
	}
	return false
}

// diskStillMounted 卸载语义下检查硬盘是否仍挂载：
// 设备在挂载表中的实际挂载点，或任一配置挂载点仍在表中。
func (d *DiskManager) diskStillMounted(disk DiskEntry, procMounts []string) bool {
	if disk.Device != "" && len(mountsOfDevice(procMounts, disk.Device)) > 0 {
		return true
	}
	if len(disk.Mounts) > 0 {
		for _, m := range disk.Mounts {
			if m.MountPoint != "" && isMountPoint(procMounts, m.MountPoint) {
				return true
			}
		}
	} else if disk.MountPath != "" && isMountPoint(procMounts, disk.MountPath) {
		return true
	}
	return false
}

// mountsOfDevice 返回 /proc/mounts 中该设备当前的全部实际挂载点（空=未挂载）。
func mountsOfDevice(procMounts []string, device string) []string {
	var out []string
	if device == "" {
		return out
	}
	for _, line := range procMounts {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != device {
			continue
		}
		out = append(out, fields[1])
	}
	return out
}

// isMountPoint 判断路径是否当前挂载点（仅按挂载点字段精确匹配）。
func isMountPoint(procMounts []string, path string) bool {
	if path == "" {
		return false
	}
	for _, line := range procMounts {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == path {
			return true
		}
	}
	return false
}

// OnSwitchChanged 按钮或外部触发的开关状态变化回调。
// 自动处理对应硬盘组的挂载/卸载。
// OnSwitchChanged 处理固件推送的 SWn 状态变化事件（EVT_BTN）。
// cond 为事件来源（bpCondButton=硬件按钮 / bpCondAuto=上电自动上线 / bpCondCmd=指令）：
// 日志动作按来源区分——硬件按钮记"按钮打开/关闭"，上电自动上线记"自动上线/自动下线"，
// 其它归为"上线/下线"。修复此前所有外部变化被误记为 auto 的问题。
func (d *DiskManager) OnSwitchChanged(swN int, on bool, cond uint8) {
	d.mu.RLock()
	var matched *DiskGroupConfig
	for i := range d.config {
		// 只按 SwitchN 匹配：硬件按钮/上电事件即使组被禁用也应记录（#Fix：禁用组的
		// 硬件按钮打开日志此前因 Enabled 过滤被丢弃）
		if d.config[i].SwitchN == swN {
			matched = &d.config[i]
			break
		}
	}
	d.mu.RUnlock()

	if matched == nil {
		return
	}

	if on {
		logger.Info("disk", "group %d online (SW%d ON, cond=%d)", matched.ID, swN, cond)
		// 记录上电过渡起点（thermal.go 上电容错用；PowerOn 流程内已标记，重复标记无副作用）
		d.MarkSwitchOn(swN)
		// 等待开机延迟
		delay := d.getSwitchPowerOnDelay(swN)
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		// #Fix：指令触发的状态回推（cond=bpCondCmd）不在此挂载/刷新——
		// 上线动作由 PowerOn 流程统一完成（含等待磁盘就绪、挂载、缓存刷新），
		// 此处再挂载会与 PowerOn 并行执行，磁盘尚未就绪必然失败并产生重复日志。
		if cond != bpCondCmd {
			for _, disk := range matched.Disks {
				if disk.AutoMount {
					if err := d.mountDisk(disk); err != nil {
						logger.Warn("disk", "group %d auto mount %s failed: %v", matched.ID, disk.Device, err)
					}
				}
			}
			// #Fix：硬件按钮/上电自动上线无 PowerOn 流程，挂载后立即刷新缓存，
			// 否则风扇按 SN 关联温度参考点读不到新盘，误判离线进入故障模式。
			d.RefreshDiskInfo()
		}
		// #Fix：指令触发的状态回推（cond=bpCondCmd）日志由 PowerOn 流程统一记录，
		// 此处跳过避免"一次上线两条日志"；硬件按钮/上电自动上线仍在此记录。
		if cond == bpCondCmd {
			d.broadcast()
			return
		}
		content := fmt.Sprintf("SW%d ON", swN)
		remark := ""
		switch cond {
		case bpCondAuto:
			content = fmt.Sprintf("SW%d ON (auto)", swN)
			remark = "自动执行"
		case bpCondButton:
			content = fmt.Sprintf("SW%d ON (button)", swN)
			remark = "按钮打开"
		}
		LogDiskEvent(matched.ID, matched.Alias, DiskLogPowerOn, content, "ok", remark)
	} else {
		logger.Info("disk", "group %d offline (SW%d OFF, cond=%d)", matched.ID, swN, cond)
		// 开关已被外部关闭，仅做卸载清理
		var umountMsgs []string
		for _, disk := range matched.Disks {
			if err := d.unmountDisk(disk); err != nil {
				logger.Warn("disk", "group %d auto unmount %s failed: %v", matched.ID, disk.Device, err)
				umountMsgs = append(umountMsgs, disk.Device+": "+err.Error())
			}
		}
		// #Fix：同上，指令回推不重复记日志（PowerOff 流程统一记录）
		if cond == bpCondCmd {
			d.broadcast()
			return
		}
		content := fmt.Sprintf("SW%d OFF", swN)
		remark := ""
		switch cond {
		case bpCondAuto:
			content = fmt.Sprintf("SW%d OFF (auto)", swN)
			remark = "自动执行"
		case bpCondButton:
			content = fmt.Sprintf("SW%d OFF (button)", swN)
			remark = "按钮关闭"
		}
		// 卸载失败信息作为错误并入备注（形如：按钮关闭; error:"umount ...失败"）
		LogDiskEvent(matched.ID, matched.Alias, DiskLogPowerOff, content, "ok", remark+errSuffix(umountMsgs))
	}
	d.broadcast()
}

// errSuffix 将错误列表转为备注追加段（空列表返回空串，否则返回 `; error:"m1; m2"`）。
func errSuffix(msgs []string) string {
	if len(msgs) == 0 {
		return ""
	}
	return `; error:"` + strings.Join(msgs, "; ") + `"`
}

// LogOfflineSwitchEvent 记录离线期间发生、由上位机按固件时间戳反推真实时间的 SWn 事件。
// 仅写入操作日志（含反推的真实时间），不执行挂载/卸载——事件已发生，状态由全量查询同步。
func (d *DiskManager) LogOfflineSwitchEvent(swN int, on bool, cond uint8, ts time.Time) {
	d.mu.RLock()
	var matched *DiskGroupConfig
	for i := range d.config {
		// 同 OnSwitchChanged：不按 Enabled 过滤，禁用组离线期间的按钮事件也需记录
		if d.config[i].SwitchN == swN {
			matched = &d.config[i]
			break
		}
	}
	d.mu.RUnlock()
	if matched == nil {
		return
	}
	action := DiskLogPowerOn
	content := fmt.Sprintf("SW%d ON (offline)", swN)
	if !on {
		action = DiskLogPowerOff
		content = fmt.Sprintf("SW%d OFF (offline)", swN)
	}
	condName := "other"
	switch cond {
	case bpCondAuto:
		condName = "auto"
	case bpCondButton:
		condName = "button"
	}
	content += " [" + condName + "]"
	logger.Info("disk", "group %d offline event recorded at %s", matched.ID, ts.Format("2006-01-02 15:04:05"))
	LogDiskEventAt(matched.ID, matched.Alias, action, content, "ok", "离线事件", ts)
}

// ===== 内部方法 =====

// runGroupTask 触发硬盘组任务（上电后 / 下电前）。
//   - wait=false（上电后）：异步触发执行，触发成功即返回、不再介入（交由调度引擎管理）；
//   - wait=true（下电前）：同步触发并等待任务主执行器执行完成，成功才返回 nil；
//     任务丢失/执行失败返回错误，由调用方决定终止正常下电。
// 未配置任务（nil / TaskID<=0）或调度引擎未注入（nil）时：未配置跳过返回 nil；
// 未注入视为错误返回（上电后仅记日志；下电前终止下电）。
func (d *DiskManager) runGroupTask(t *DiskGroupTask, wait bool) error {
	if t == nil || t.TaskID <= 0 {
		return nil
	}
	if d.sched == nil {
		return fmt.Errorf("调度引擎未注入，任务 #%d 未执行", t.TaskID)
	}
	if wait {
		timeout := d.offTaskTimeout
		if timeout <= 0 {
			timeout = 120 * time.Second // 总超时上限：任务主执行器全部立即执行应在该窗口内完成
		}
		type res struct {
			msg string
			ok  bool
		}
		done := make(chan res, 1)
		go func() {
			msg, ok := d.sched.TriggerManual(t.TaskID)
			done <- res{msg: msg, ok: ok}
		}()
		select {
		case r := <-done:
			if !r.ok {
				return fmt.Errorf("下电前任务 #%d 执行失败：%s", t.TaskID, r.msg)
			}
			return nil
		case <-time.After(timeout):
			return fmt.Errorf("下电前任务 #%d 执行超时（%s），已终止正常下电", t.TaskID, timeout)
		}
	}
	msg, ok := d.sched.TriggerManualAsync(t.TaskID)
	if !ok {
		return fmt.Errorf("上电后任务 #%d 触发失败：%s", t.TaskID, msg)
	}
	return nil
}

func (d *DiskManager) findGroup(id int) (*DiskGroupConfig, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for i := range d.config {
		if d.config[i].ID == id {
			return &d.config[i], nil
		}
	}
	return nil, fmt.Errorf("硬盘组 %d 不存在", id)
}

func (d *DiskManager) getSwitchPowerOnDelay(swN int) int {
	switches := d.ble.GetSwitchStates()
	for _, s := range switches {
		if s.Index == swN {
			return s.PowerOnDelay
		}
	}
	return 0
}

// offlineWaitDelay 返回硬盘组配置的"下线等待时间"（秒，1~300；0/非法回退默认 2 秒）。
// 安全下线在 umount 成功后等待该时长，强制下线在 umount -f 发送后等待该时长。
func (d *DiskManager) offlineWaitDelay(g *DiskGroupConfig) time.Duration {
	if g != nil && g.OfflineDelaySec > 0 && g.OfflineDelaySec <= 300 {
		return time.Duration(g.OfflineDelaySec) * time.Second
	}
	return 2 * time.Second
}

// mountDisk 挂载硬盘（3.md：优先按 Mounts 分区→挂载点逐项挂载；未配置时回退旧 MountPath）。
func (d *DiskManager) mountDisk(disk DiskEntry) error {
	if len(disk.Mounts) > 0 {
		var lastErr error
		for _, m := range disk.Mounts {
			if m.Partition == "" || m.MountPoint == "" {
				continue
			}
			if err := mountOne(m.Partition, m.MountPoint); err != nil {
				lastErr = err
			}
		}
		// 至少有一组成功即视为成功；全部失败才报错
		mounts := readSystemMounts()
		for _, m := range disk.Mounts {
			if isMounted(mounts, m.Partition, m.MountPoint) {
				return nil
			}
		}
		if lastErr == nil {
			return fmt.Errorf("未配置有效分区/挂载点")
		}
		return lastErr
	}
	if disk.Device == "" || disk.MountPath == "" {
		return fmt.Errorf("设备路径或挂载点为空")
	}
	return mountOne(disk.Device, disk.MountPath)
}

// mountOne 挂载单个设备到挂载点。
func mountOne(device, mountPath string) error {
	// G4: 防御——拒绝含 ".." 的挂载点, 防止挂载路径逃逸到任意目录
	//     (mountPath 来自用户配置/分区表, 正常路径不应含路径遍历段)
	if strings.Contains(mountPath, "..") {
		return fmt.Errorf("挂载点含非法路径段: %s", mountPath)
	}
	// 确保挂载点存在
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		return fmt.Errorf("创建挂载点失败: %w", err)
	}
	// 检查是否已挂载
	mounts := readSystemMounts()
	if isMounted(mounts, device, mountPath) {
		logger.Info("disk", "%s already mounted at %s", device, mountPath)
		return nil
	}
	// 使用 mount 命令（fnOS Linux 环境；走路径探测避免 PATH 不含 /usr/sbin）
	mountBin := findMount()
	if mountBin == "" {
		return fmt.Errorf("系统缺少 mount 命令（util-linux 未安装）")
	}
	if out, err := privRun(mountBin, 15*time.Second, device, mountPath); err != nil {
		return fmt.Errorf("mount 失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	logger.Info("disk", "mounted %s at %s", device, mountPath)
	return nil
}

// unmountDisk 卸载硬盘（Mounts 优先逐项卸载，回退旧 MountPath）。
// 卸载目标以系统挂载表为准 = 配置的挂载点 ∪ 设备当前实际挂载点；
// 只对确实在表中的挂载点执行 umount（不在表中的视为已卸载，跳过），
// 避免配置挂载点与系统实际挂载点不一致时 umount 报 "not mounted"。
func (d *DiskManager) unmountDisk(disk DiskEntry) error {
	targets := map[string]bool{}
	if len(disk.Mounts) > 0 {
		for _, m := range disk.Mounts {
			if m.MountPoint != "" {
				targets[m.MountPoint] = true
			}
		}
	} else if disk.MountPath != "" {
		targets[disk.MountPath] = true
	}
	mounts := readSystemMounts()
	if disk.Device != "" {
		for _, mp := range mountsOfDevice(mounts, disk.Device) {
			targets[mp] = true
		}
	}
	if len(targets) == 0 {
		return nil
	}
	umountBin := findUmount()
	if umountBin == "" {
		return fmt.Errorf("系统缺少 umount 命令（util-linux 未安装）")
	}
	var lastErr error
	for mp := range targets {
		mounts = readSystemMounts()
		if !isMountPoint(mounts, mp) {
			continue
		}
		if out, err := privRun(umountBin, 15*time.Second, mp); err != nil {
			lastErr = fmt.Errorf("umount %s 失败: %w (%s)", mp, err, strings.TrimSpace(string(out)))
		} else {
			logger.Info("disk", "unmounted %s", mp)
		}
	}
	// 有失败时先确认最终是否已全部卸载：设备/配置挂载点均不在表中视为成功
	//（覆盖 umount 瞬间被并发移除的竞态，避免把 not mounted 误报为失败）
	if lastErr != nil {
		mounts = readSystemMounts()
		if disk.Device != "" && len(mountsOfDevice(mounts, disk.Device)) > 0 {
			return lastErr
		}
		for mp := range targets {
			if isMountPoint(mounts, mp) {
				return lastErr
			}
		}
	}
	return nil
}

// standbyDisk 发送 SATA STANDBY 指令停转机械盘。
func (d *DiskManager) standbyDisk(disk DiskEntry) error {
	if disk.Device == "" {
		return nil
	}
	// 使用 hdparm -y 让硬盘进入待机状态（走路径探测避免 PATH 不含 /usr/sbin）
	hdparmBin := findHdparm()
	if hdparmBin == "" {
		return fmt.Errorf("系统缺少 hdparm 命令（hdparm 未安装）")
	}
	if out, err := privRun(hdparmBin, 15*time.Second, "-y", disk.Device); err != nil {
		return fmt.Errorf("hdparm standby 失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	logger.Info("disk", "standby sent to %s", disk.Device)
	return nil
}

// readSystemMounts 读取 /proc/mounts 获取当前系统挂载表。
func readSystemMounts() []string {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// isMountedMulti 判断硬盘是否已挂载（AND 语义 + 序列号绑定）：
// 优先按新格式 Mounts（分区→挂载点）匹配 /proc/mounts；未配置时回退旧字段 MountPath。
// currentParts 为按序列号从缓存解析出的"当前"分区/整盘路径集合（nil/空=缓存未就绪或 SN 未命中）。
// 分区归属采用超集匹配：配置中的 m.Partition 命中 或 currentParts 命中 均算归属本盘，
// 避免 currentParts 不全（整盘挂载/缓存陈旧/分区结构差异）时误判未挂载。
// 返回 (是否挂载, 展示挂载点)。
func isMountedMulti(procMounts []string, device, legacyMountPath string, mounts []DiskMount, currentParts map[string]bool) (bool, string) {
	hasParts := len(currentParts) > 0
	if len(mounts) > 0 {
		var mountedPaths []string
		for _, m := range mounts {
			if m.Partition == "" && m.MountPoint == "" {
				continue
			}
			for _, line := range procMounts {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				// AND 语义：分区归属与挂载点须在同一行同时匹配。
				// 分区归属（超集）：配置路径命中 或 SN 解析的当前路径集合命中。
				partMatch := m.Partition == "" ||
					fields[0] == m.Partition ||
					(hasParts && currentParts[fields[0]])
				mpMatch := m.MountPoint == "" || fields[1] == m.MountPoint
				if partMatch && mpMatch {
					if m.MountPoint != "" {
						mountedPaths = append(mountedPaths, m.MountPoint)
					} else {
						mountedPaths = append(mountedPaths, fields[1])
					}
					break
				}
			}
		}
		if len(mountedPaths) > 0 {
			return true, strings.Join(mountedPaths, ", ")
		}
		return false, legacyMountPath
	}
	// 旧格式（单挂载点）：device 通常为整盘路径，/proc/mounts 中是分区路径，
	// 整盘路径无法直接匹配分区，因此以 MountPath 挂载点命中为准（等价原 isMounted 对挂载点的匹配）。
	// currentParts 仅作为额外认可途径，绝不因其不全而否决，避免已挂载却显示未挂载。
	if legacyMountPath != "" {
		for _, line := range procMounts {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != legacyMountPath {
				continue
			}
			// 挂载点命中即视为已挂载。currentParts/device 命中同样认可（超集，不否决）。
			return true, legacyMountPath
		}
	}
	// 回退：device 直接匹配（整盘路径少命中，兼容 device 配为分区路径的旧配置）。
	if isMounted(procMounts, device, legacyMountPath) {
		return true, legacyMountPath
	}
	return false, legacyMountPath
}

// isMounted 旧格式：按设备路径或单挂载点匹配（OR 语义，上电挂载流程用：系统自动挂到别处也算已挂载可跳过手动挂载）。
func isMounted(mounts []string, device, mountPath string) bool {
	for _, line := range mounts {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// fields[0]=device, fields[1]=mountpoint
		if (device != "" && fields[0] == device) ||
			(mountPath != "" && fields[1] == mountPath) {
			return true
		}
	}
	return false
}

// broadcast 推送状态更新。
func (d *DiskManager) broadcast() {
	if d.hub == nil {
		return
	}
	d.ble.broadcastState()
}

// ============================================================
// 占用检测 / 进程终止 / 强制卸载（下线与卸载流程的决策支持）
// ============================================================

// diskMountPoints 返回硬盘的全部挂载点（新格式 Mounts 优先，回退旧 MountPath）。
func (d *DiskManager) diskMountPoints(disk DiskEntry) []string {
	var out []string
	if len(disk.Mounts) > 0 {
		for _, m := range disk.Mounts {
			if m.MountPoint != "" {
				out = append(out, m.MountPoint)
			}
		}
	} else if disk.MountPath != "" {
		out = append(out, disk.MountPath)
	}
	return out
}

// diskMountTargets 返回硬盘用于占用检测的目标串（设备路径 + 挂载点，去重）。
func (d *DiskManager) diskMountTargets(disk DiskEntry) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(disk.Device)
	for _, mp := range d.diskMountPoints(disk) {
		add(mp)
	}
	return out
}

// findOccupyProcs 检测占用指定设备/挂载点的进程。
// 优先 fuser -m（psmisc），缺失时回退 lsof；两者都不可用时返回空（无法检测）。
func findOccupyProcs(targets []string) []OccupyProcess {
	for _, target := range targets {
		if procs := occupyViaFuser(target); len(procs) > 0 {
			return procs
		}
	}
	for _, target := range targets {
		if procs := occupyViaLsof(target); len(procs) > 0 {
			return procs
		}
	}
	return nil
}

// occupyViaFuser 使用 fuser -v -m <target> 解析占用进程（输出形如：/dev/sda1: user pid  cmd）。
func occupyViaFuser(target string) []OccupyProcess {
	bin := findFuser()
	if bin == "" {
		return nil
	}
	out, err := runCmd(bin, 10*time.Second, "-v", "-m", target)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var procs []OccupyProcess
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, perr := strconv.Atoi(fields[len(fields)-2])
		if perr != nil || pid <= 0 {
			continue
		}
		cmd := fields[len(fields)-1]
		user := ""
		for _, f := range fields {
			if strings.HasPrefix(f, "/") || f == "root" || strings.ContainsAny(f, "@") {
				user = f
				break
			}
		}
		if user == "" {
			user = fields[0]
		}
		procs = append(procs, OccupyProcess{PID: pid, Command: cmd, User: user})
	}
	return procs
}

// occupyViaLsof 使用 lsof +t <target> 解析占用进程（输出为每行一个 PID）。
func occupyViaLsof(target string) []OccupyProcess {
	bin := findLsof()
	if bin == "" {
		return nil
	}
	out, err := runCmd(bin, 10*time.Second, "+t", target)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var procs []OccupyProcess
	for _, line := range lines {
		pid, perr := strconv.Atoi(strings.TrimSpace(line))
		if perr != nil || pid <= 0 {
			continue
		}
		procs = append(procs, OccupyProcess{PID: pid, Command: procCommandName(pid), User: procUserName(pid)})
	}
	return procs
}

// procCommandName 从 /proc/<pid>/comm 读取进程名。
func procCommandName(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// procUserName 从 /proc/<pid>/status 读取 Uid，映射为用户名（读不到时返回空）。
func procUserName(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				uid := fields[1]
				if uid == "0" {
					return "root"
				}
				return uid
			}
		}
	}
	return ""
}

// killProcs 终止占用进程：先 SIGTERM，等待 1s 未退出再 SIGKILL。
func killProcs(procs []OccupyProcess) []string {
	var failed []string
	for _, p := range procs {
		proc, err := os.FindProcess(p.PID)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%d(%s): %v", p.PID, p.Command, err))
			continue
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			// 进程可能已退出
			continue
		}
		deadline := time.Now().Add(1 * time.Second)
		alive := true
		for time.Now().Before(deadline) {
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				alive = false
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if alive {
			if err := proc.Kill(); err != nil {
				failed = append(failed, fmt.Sprintf("%d(%s): %v", p.PID, p.Command, err))
			}
		}
	}
	return failed
}

// forceUnmount 使用 umount -f 强制卸载指定挂载点。
func forceUnmount(mountPoint string) error {
	umountBin := findUmount()
	if umountBin == "" {
		return fmt.Errorf("系统缺少 umount 命令（util-linux 未安装）")
	}
	if out, err := privRun(umountBin, 15*time.Second, "-f", mountPoint); err != nil {
		return fmt.Errorf("umount -f %s 失败: %w (%s)", mountPoint, err, strings.TrimSpace(string(out)))
	}
	logger.Info("disk", "force unmounted %s", mountPoint)
	return nil
}

// unmountFailedWithDecision 卸载失败后检查占用进程：
//   - 有占用 → 返回需要前端决策的 CommandResult（Decision=unmount_fail_occupied）
//   - 无占用 → 返回 nil，由调用方按普通失败处理
func occupiedDecisionResult(groupID int, alias string, device string, mountDesc string, err error) *CommandResult {
	targets := []string{device}
	if mountDesc != "" {
		targets = append(targets, mountDesc)
	}
	procs := findOccupyProcs(targets)
	if len(procs) == 0 {
		return nil
	}
	logger.Warn("disk", "group %d %s: unmount %s blocked by %d processes", groupID, device, mountDesc, len(procs))
	return &CommandResult{
		OK:       false,
		Message:  fmt.Sprintf("%s 卸载失败：%d 个进程正在占用", device, len(procs)),
		Decision: "unmount_fail_occupied",
		Occupied: procs,
	}
}

// ============================================================
// 磁盘信息查询（供前端硬盘组配置页下拉选择使用）
// ============================================================

// DiskInfo 物理磁盘信息（lsblk -d 输出）
type DiskInfo struct {
	Name            string `json:"name"`            // 设备名，如 sda
	Path            string `json:"path"`            // 设备路径，如 /dev/sda
	Model           string `json:"model"`           // 型号
	Size            string `json:"size"`            // 容量，如 1.4T
	Serial          string `json:"serial"`          // 序列号（可能为空）
	SerialAvailable bool   `json:"serialAvailable"` // 序列号是否可读
	Mountpoint      string `json:"mountpoint"`      // 挂载点（磁盘级，通常为空）
	Bound           bool   `json:"bound"`           // 是否已被硬盘组通道绑定
	IsNVMe          bool   `json:"isNvme"`          // NVMe SSD（3.md：按传输类型/设备名判定）
}

// PartitionInfo 磁盘分区信息（lsblk 子节点 TYPE=part）
type PartitionInfo struct {
	Name        string   `json:"name"`        // 分区名，如 sda1
	Path        string   `json:"path"`        // 分区路径，如 /dev/sda1
	Size        string   `json:"size"`        // 分区大小
	Mountpoints []string `json:"mountpoints"` // 挂载点列表
}

// lsblkBlockDevice lsblk --json 输出的单条块设备结构
type lsblkBlockDevice struct {
	Name        string             `json:"name"`
	Model       string             `json:"model"`
	Size        string             `json:"size"`
	Serial      string             `json:"serial"`
	Mountpoint  any                `json:"mountpoint"` // 单数形式：string 或 null
	Type        string             `json:"type"`
	Tran        string             `json:"tran"`        // 传输类型：nvme / sata / usb 等
	Mountpoints []any              `json:"mountpoints"` // 复数形式：数组，元素为 string 或 null
	Children    []lsblkBlockDevice `json:"children"`
}

// lsblkOutput lsblk --json 顶层结构
type lsblkOutput struct {
	BlockDevices []lsblkBlockDevice `json:"blockdevices"`
}

// ListDisks 优先从磁盘信息缓存读取（启动时一次性加载），缓存未加载时回退到 lsblk 实时读取。
// 排除回环/ram/zram 虚拟设备，标注已绑定磁盘、序列号可用性与 NVMe 属性。
func (d *DiskManager) ListDisks() ([]DiskInfo, error) {
	// 优先从缓存读取
	if d.cache != nil && d.cache.IsLoaded() {
		disks := d.cache.Disks()
		boundSet := d.boundDiskSet()
		mounts := readSystemMounts()

		result := make([]DiskInfo, 0, len(disks))
		for _, sd := range disks {
			path := sd.Path

			// 排除虚拟设备（缓存里 buildDiskSummary 已过滤 loop/ram/zram，但再次确认）
			if strings.HasPrefix(sd.Name, "loop") ||
				strings.HasPrefix(sd.Name, "ram") ||
				strings.HasPrefix(sd.Name, "zram") {
				continue
			}

			// 挂载点：从第一个分区挂载点取（简化）
			var mountpoint string
			if len(sd.Partitions) > 0 && len(sd.Partitions[0].Mounts) > 0 {
				mountpoint = sd.Partitions[0].Mounts[0].Mountpoint
			} else {
				mountpoint = mountpointOf(mounts, path)
			}

			isNVMe := strings.HasPrefix(sd.Name, "nvme") || strings.EqualFold(sd.Tran, "nvme")
			result = append(result, DiskInfo{
				Name:            sd.Name,
				Path:            path,
				Model:           sd.Model,
				Size:            sd.SizeHuman,
				Serial:          sd.Serial,
				SerialAvailable: sd.Serial != "",
				Mountpoint:      mountpoint,
				Bound:           boundSet[path],
				IsNVMe:          isNVMe,
			})
		}
		logger.Debug("disk", "ListDisks from cache: %d disks", len(result))
		return result, nil
	}

	// 缓存未加载：回退 lsblk 实时读取（首次启动或开发调试用）
	logger.Debug("disk", "ListDisks cache not loaded, fallback to lsblk")
	return d.listDisksLsblk()
}

// listDisksLsblk 原始 lsblk 实时读取（缓存未加载时的兜底）
func (d *DiskManager) listDisksLsblk() ([]DiskInfo, error) {
	lsblk := findLsblk()
	if lsblk == "" {
		logger.Warn("disk", "lsblk not found in PATH or common paths, fallback to sysfs")
		return d.listDisksSysfs()
	}
	out, err := runCmdOutput(lsblk, 5*time.Second, "-d", "-o", "NAME,MODEL,SIZE,SERIAL,MOUNTPOINT,TRAN", "--json")
	if err != nil {
		logger.Warn("disk", "lsblk failed: %v, fallback to sysfs", err)
		return d.listDisksSysfs()
	}
	var result lsblkOutput
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("lsblk 输出解析失败: %w", err)
	}

	boundSet := d.boundDiskSet()

	disks := make([]DiskInfo, 0, len(result.BlockDevices))
	for _, bd := range result.BlockDevices {
		// 排除回环/ram/zram 虚拟设备
		if strings.HasPrefix(bd.Name, "loop") ||
			strings.HasPrefix(bd.Name, "ram") ||
			strings.HasPrefix(bd.Name, "zram") {
			continue
		}
		path := "/dev/" + bd.Name
		mp := ""
		if bd.Mountpoint != nil {
			mp = fmt.Sprint(bd.Mountpoint)
		}
		serial := strings.TrimSpace(bd.Serial)
		// SATA 盘 lsblk 的 SERIAL 列常见为空：经 /dev/disk/by-id 反推补齐
		if serial == "" {
			serial = diskByIDSerial(bd.Name, strings.TrimSpace(bd.Model))
		}
		isNVMe := strings.HasPrefix(bd.Name, "nvme") || strings.EqualFold(bd.Tran, "nvme")
		disks = append(disks, DiskInfo{
			Name:            bd.Name,
			Path:            path,
			Model:           strings.TrimSpace(bd.Model),
			Size:            bd.Size,
			Serial:          serial,
			SerialAvailable: serial != "",
			Mountpoint:      mp,
			Bound:           boundSet[path],
			IsNVMe:          isNVMe,
		})
	}
	return disks, nil
}

// boundDiskSet 返回已被硬盘组绑定的设备路径集合。
func (d *DiskManager) boundDiskSet() map[string]bool {
	bound := make(map[string]bool)
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, g := range d.config {
		for _, disk := range g.Disks {
			if disk.Device != "" {
				bound[disk.Device] = true
			}
		}
	}
	return bound
}

// listDisksSysfs 纯 sysfs 读取磁盘清单（lsblk 缺失/失败时的兜底，字段与 lsblk 路径对齐）。
func (d *DiskManager) listDisksSysfs() ([]DiskInfo, error) {
	boundSet := d.boundDiskSet()
	mounts := readSystemMounts()
	raw := sysfsListDisks()
	disks := make([]DiskInfo, 0, len(raw))
	for _, bd := range raw {
		path := "/dev/" + bd.Name
		disks = append(disks, DiskInfo{
			Name:            bd.Name,
			Path:            path,
			Model:           bd.Model,
			Size:            humanSize(sysfsBlockSize(bd.Name)),
			Serial:          bd.Serial,
			SerialAvailable: bd.Serial != "",
			Mountpoint:      mountpointOf(mounts, path),
			Bound:           boundSet[path],
			IsNVMe:          strings.HasPrefix(bd.Name, "nvme"),
		})
	}
	return disks, nil
}

// ListPartitions 优先从磁盘信息缓存读取指定磁盘的分区列表（TYPE=part）。
// 缓存未加载时回退 lsblk 实时读取，再回退纯 sysfs。
func (d *DiskManager) ListPartitions(device string) ([]PartitionInfo, error) {
	if strings.TrimSpace(device) == "" {
		return nil, fmt.Errorf("设备路径为空")
	}

	// 优先从缓存读取
	if d.cache != nil && d.cache.IsLoaded() {
		disks := d.cache.Disks()
		for _, sd := range disks {
			if sd.Path == device || sd.Name == strings.TrimPrefix(device, "/dev/") {
				var parts []PartitionInfo
				for _, p := range sd.Partitions {
					parts = append(parts, partitionFromSummary(p))
				}
				logger.Debug("disk", "ListPartitions from cache for %s: %d partitions", device, len(parts))
				return parts, nil
			}
		}
		// 缓存里没找到该磁盘（可能是分区路径），回退 lsblk
		logger.Debug("disk", "ListPartitions cache miss for %s, fallback to lsblk", device)
	}

	// 回退 lsblk 实时读取
	return d.listPartitionsLsblk(device)
}

// partitionFromSummary 递归将缓存 Partition 转换为前端 PartitionInfo
func partitionFromSummary(p Partition) PartitionInfo {
	mps := make([]string, 0, len(p.Mounts))
	for _, m := range p.Mounts {
		if m.Mountpoint != "" {
			mps = append(mps, m.Mountpoint)
		}
	}
	pi := PartitionInfo{
		Name:        p.Name,
		Path:        p.Path,
		Size:        p.SizeHuman,
		Mountpoints: mps,
	}
	// 递归子分区（如果有）
	if len(p.Children) > 0 {
		for _, c := range p.Children {
			childPi := partitionFromSummary(c)
			pi.Mountpoints = append(pi.Mountpoints, childPi.Mountpoints...)
		}
	}
	return pi
}

// listPartitionsLsblk 原始 lsblk 实时读取（缓存未加载时的兜底）
func (d *DiskManager) listPartitionsLsblk(device string) ([]PartitionInfo, error) {
	lsblk := findLsblk()
	if lsblk == "" {
		return listPartitionsSysfs(device)
	}
	out, err := runCmdOutput(lsblk, 5*time.Second, "-o", "NAME,SIZE,TYPE,MOUNTPOINTS", "--json", device)
	if err != nil {
		logger.Warn("disk", "lsblk failed for %s: %v, fallback to sysfs", device, err)
		return listPartitionsSysfs(device)
	}
	var result lsblkOutput
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("lsblk 输出解析失败: %w", err)
	}

	parts := make([]PartitionInfo, 0)
	for _, bd := range result.BlockDevices {
		for _, child := range bd.Children {
			if child.Type != "part" {
				continue
			}
			mps := make([]string, 0, len(child.Mountpoints))
			for _, mp := range child.Mountpoints {
				if mp != nil {
					mps = append(mps, fmt.Sprint(mp))
				}
			}
			parts = append(parts, PartitionInfo{
				Name:        child.Name,
				Path:        "/dev/" + child.Name,
				Size:        child.Size,
				Mountpoints: mps,
			})
		}
	}
	return parts, nil
}

// listPartitionsSysfs 纯 sysfs 读取分区清单（lsblk 缺失/失败时的兜底）。
func listPartitionsSysfs(device string) ([]PartitionInfo, error) {
	parts := sysfsPartitions(device)
	out := make([]PartitionInfo, 0, len(parts))
	for _, p := range parts {
		mps := make([]string, 0, 1)
		if p.Mount != "" {
			mps = append(mps, p.Mount)
		}
		out = append(out, PartitionInfo{
			Name:        p.Name,
			Path:        "/dev/" + p.Name,
			Size:        humanSize(p.Size),
			Mountpoints: mps,
		})
	}
	return out, nil
}
