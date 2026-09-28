package main

// 外部命令路径探测与 sysfs 兜底。
// 背景：上位机以 systemd/无登录会话运行在 fnOS 上时，PATH 可能不含 /usr/sbin 等目录，
// 导致 exec.Command("lsblk") 报 "executable file not found in $PATH"。
// 方案：
//  1. findTool 按 常见绝对路径（/usr/bin /bin /usr/sbin /sbin /usr/local/bin）→ PATH 顺序探测；
//  2. 读盘类信息（磁盘列表/分区/SN）提供纯 sysfs 兜底，不依赖任何外部命令。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// findTool 探测外部命令可执行文件路径；找不到返回 ""。
func findTool(names ...string) string {
	for _, n := range names {
		for _, dir := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin"} {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func findLsblk() string    { return findTool("lsblk") }
func findSmartctl() string { return findTool("smartctl") }
func findNvme() string     { return findTool("nvme") }
func findHdparm() string   { return findTool("hdparm") }
func findMount() string    { return findTool("mount") }
func findUmount() string   { return findTool("umount") }
func findFuser() string    { return findTool("fuser") }
func findLsof() string     { return findTool("lsof") }

// runCmd 执行外部命令（带超时），返回 CombinedOutput 结果。
// 超时后取消 context 并杀掉子进程，避免 mount/umount/hdparm 等命令卡死挂起 API。
func runCmd(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// privRun 以特权执行外部命令（带超时）：
//   - 进程为 root 时直接执行；
//   - 非 root 时通过 `sudo -n` 提权（NOPASSWD 白名单已配置时无需交互）；
//   - 无 sudo 或未授权时返回带修复提示的错误（如"请以 root 运行，或为运行用户配置 sudo NOPASSWD"）。
//
// mount / umount / hdparm 等涉及挂载与硬盘电源管理的命令必须 root 权限，
// fnOS 上上位机常以普通用户/systemd 服务运行，此时需要 sudoers 授权。
func privRun(name string, timeout time.Duration, args ...string) ([]byte, error) {
	if os.Geteuid() == 0 {
		return runCmd(name, timeout, args...)
	}
	sudo := findTool("sudo")
	if sudo == "" {
		return nil, fmt.Errorf("需要 root 权限执行 %s（当前 uid=%d），但系统缺少 sudo 命令：请以 root 运行上位机，或安装 sudo 并为运行用户配置 NOPASSWD 白名单（见 visudo）", name, os.Geteuid())
	}
	out, err := runCmd(sudo, timeout, append([]string{"-n", name}, args...)...)
	if err != nil {
		return out, fmt.Errorf("%s 需要 root 权限（当前 uid=%d），sudo -n 提权失败：%w (%s)——请以 root 运行上位机，或在 fnOS 上执行 `visudo` 为运行用户添加 NOPASSWD 白名单，例如：\n%v ALL=(root) NOPASSWD: %v, /bin/mount, /bin/umount, /usr/sbin/hdparm",
			name, os.Geteuid(), err, strings.TrimSpace(string(out)), userOrUid(), name)
	}
	return out, nil
}

// userOrUid 返回当前用户名（读不到时返回 uid 字符串）。
func userOrUid() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return strconv.Itoa(os.Geteuid())
}

// runCmdOutput 执行外部命令（带超时），仅返回 stdout。
func runCmdOutput(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// execCombinedOutputCtx 执行命令并分别收集 stdout/stderr（带超时）。
func execCombinedOutputCtx(name string, timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), errBuf.String(), err
}

// readTrim 读取单行文本文件并去除空白。
func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// humanSize 把字节数格式化为 lsblk SIZE 风格的可读容量（如 1.4T）。
func humanSize(bytes int64) string {
	if bytes < 0 {
		return ""
	}
	units := []string{"B", "K", "M", "G", "T", "P"}
	v := float64(bytes)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", bytes)
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

// diskByIDSerial 通过 /dev/disk/by-id 反推磁盘序列号：
// ata 条目形如 ata-<MODEL>_<SERIAL>，nvme 形如 nvme-<MODEL>_<SERIAL>。
// 仅作 lsblk 缺失时的兜底（SATA sysfs 不暴露 serial，by-id 是唯一无 smartctl 来源）。
func diskByIDSerial(dev, model string) string {
	dir := "/dev/disk/by-id"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	norm := strings.ReplaceAll(strings.TrimSpace(model), " ", "_")
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil || !strings.HasSuffix(target, "/"+dev) {
			continue
		}
		name := e.Name()
		var rest string
		switch {
		case strings.HasPrefix(name, "ata-"):
			rest = strings.TrimPrefix(name, "ata-")
		case strings.HasPrefix(name, "nvme-") &&
			!strings.HasPrefix(name, "nvme-eui.") &&
			!strings.HasPrefix(name, "nvme-wwid"):
			rest = strings.TrimPrefix(name, "nvme-")
		default:
			continue
		}
		// 已知 model：去掉 "<model>_" 前缀后剩余即为 serial（最准确）
		if norm != "" {
			if s, ok := strings.CutPrefix(rest, norm+"_"); ok && s != "" {
				return s
			}
		}
		// model 未知：取最后一段下划线之后
		if i := strings.LastIndex(rest, "_"); i > 0 && i < len(rest)-1 {
			return rest[i+1:]
		}
	}
	return ""
}

// sysfsListDisks 纯 sysfs 读取全部物理磁盘（lsblk 缺失/失败时的兜底）。
// 与 listBlockDisks 返回相同结构（Name/Serial/Model）。
func sysfsListDisks() []lsblkDisk {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil
	}
	var out []lsblkDisk
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		base := filepath.Join("/sys/class/block", name)
		// 分区（sda1 / nvme0n1p1 等）含 partition 属性文件，跳过
		if _, err := os.Stat(filepath.Join(base, "partition")); err == nil {
			continue
		}
		model := readTrim(filepath.Join(base, "device", "model"))
		serial := readTrim(filepath.Join(base, "device", "serial"))
		if serial == "" {
			serial = diskByIDSerial(name, model)
		}
		out = append(out, lsblkDisk{Name: name, Serial: serial, Model: model})
	}
	return out
}

// sysfsBlockSize 读取块设备大小（512B 扇区数 → 字节）。
func sysfsBlockSize(name string) int64 {
	v := readTrim(filepath.Join("/sys/class/block", name, "size"))
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n * 512
}

// sysfsPartitions 纯 sysfs 读取设备分区（lsblk 缺失/失败时的兜底）。
// 返回 name 与挂载点（挂载点仍需 /proc/mounts 匹配）。
type sysfsPart struct {
	Name  string
	Size  int64
	Mount string
}

func sysfsPartitions(device string) []sysfsPart {
	dev := strings.TrimPrefix(device, "/dev/")
	base := filepath.Join("/sys/class/block", dev)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	mounts := readSystemMounts()
	var out []sysfsPart
	for _, e := range entries {
		part := e.Name()
		partBase := filepath.Join(base, part)
		// 仅子分区：含 partition 属性文件
		if _, err := os.Stat(filepath.Join(partBase, "partition")); err != nil {
			continue
		}
		out = append(out, sysfsPart{Name: part, Size: sysfsBlockSize(part), Mount: mountpointOf(mounts, "/dev/"+part)})
	}
	return out
}

// mountpointOf 在 /proc/mounts 中查找设备对应的挂载点（不存在返回 ""）。
func mountpointOf(mounts []string, device string) string {
	for _, line := range mounts {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == device {
			return fields[1]
		}
	}
	return ""
}
