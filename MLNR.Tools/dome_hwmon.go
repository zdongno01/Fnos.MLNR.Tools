package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const sysHwmon = "/sys/class/hwmon"

var (
	reTempInput = regexp.MustCompile(`^temp(\d+)_input$`)
	reNvmePath  = regexp.MustCompile(`nvme/nvme(\d+)`)
	reScsiTgt   = regexp.MustCompile(`(\d+:\d+:\d+:\d+)$`)
)

// HwmonItem 一条测温点
type HwmonItem struct {
	HwmonPath string  // /sys/class/hwmon/hwmon0
	ChipName  string  // name文件内容 coretemp / drivetemp / nvme
	Label     string  // tempX_label，没有则填tempN
	TempC     float64 // 摄氏度
	DiskDev   string  // 磁盘设备 /dev/sda /dev/nvme0n1；非磁盘为空
}

// readFileTrim 读取sysfs小文件，去掉换行空格
func readFileTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// getScsiBlockDev 传入scsi target真实路径 /sys/devices/.../0:0:0:0
// 返回 /dev/sda 之类
func getScsiBlockDev(scsiTargetPath string) string {
	blockDir := filepath.Join(scsiTargetPath, "block")
	entries, err := os.ReadDir(blockDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		return "/dev/" + e.Name()
	}
	return ""
}

// hwmonToDiskDev 解析 hwmonX/device 的真实路径，得到块设备；不是磁盘返回空
func hwmonToDiskDev(hwmonDeviceLink string) string {
	realDev, err := filepath.EvalSymlinks(hwmonDeviceLink)
	if err != nil {
		return ""
	}

	// NVMe: .../nvme/nvme0
	if m := reNvmePath.FindStringSubmatch(realDev); m != nil {
		n := m[1]
		return fmt.Sprintf("/dev/nvme%sn1", n)
	}

	// SATA drivetemp scsi target: 0:0:0:0
	if m := reScsiTgt.FindStringSubmatch(realDev); m != nil {
		return getScsiBlockDev(realDev)
	}

	return ""
}

// ScanHwmon 扫描全部hwmon测温点
func ScanHwmon() ([]HwmonItem, error) {
	var res []HwmonItem

	hwmonEntries, err := os.ReadDir(sysHwmon)
	if err != nil {
		return nil, err
	}

	for _, hwEnt := range hwmonEntries {
		hwName := hwEnt.Name()
		if !strings.HasPrefix(hwName, "hwmon") {
			continue
		}
		hwPath := filepath.Join(sysHwmon, hwName)

		chipName, err := readFileTrim(filepath.Join(hwPath, "name"))
		if err != nil {
			continue
		}

		// 解析是否磁盘设备
		devLink := filepath.Join(hwPath, "device")
		diskDev := hwmonToDiskDev(devLink)

		// 遍历 temp*_input
		tempFiles, err := os.ReadDir(hwPath)
		if err != nil {
			continue
		}

		for _, tf := range tempFiles {
			fn := tf.Name()
			m := reTempInput.FindStringSubmatch(fn)
			if m == nil {
				continue
			}
			idx := m[1]
			inputPath := filepath.Join(hwPath, fn)

			rawStr, err := readFileTrim(inputPath)
			if err != nil {
				continue
			}
			rawVal, err := strconv.Atoi(rawStr)
			if err != nil {
				continue
			}
			tempC := float64(rawVal) / 1000.0

			// label
			label := fmt.Sprintf("temp%s", idx)
			labelPath := filepath.Join(hwPath, fmt.Sprintf("temp%s_label", idx))
			if lblStr, err := readFileTrim(labelPath); err == nil && lblStr != "" {
				label = lblStr
			}

			res = append(res, HwmonItem{
				HwmonPath: hwPath,
				ChipName:  chipName,
				Label:     label,
				TempC:     tempC,
				DiskDev:   diskDev,
			})
		}
	}
	return res, nil
}

func main() {
	items, err := ScanHwmon()
	if err != nil {
		fmt.Printf("scan hwmon error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("================ HWMON ALL ================")
	for _, it := range items {
		if it.DiskDev != "" {
			fmt.Printf("[DISK]  %-25s chip=%-12s dev=%-12s label=%-12s temp=%.2f ℃\n",
				it.HwmonPath, it.ChipName, it.DiskDev, it.Label, it.TempC)
		} else {
			fmt.Printf("[SENSOR]%-25s chip=%-12s            label=%-12s temp=%.2f ℃\n",
				it.HwmonPath, it.ChipName, it.Label, it.TempC)
		}
	}

	// 只打印磁盘温度
	fmt.Println("\n========== ONLY DISK TEMPERATURE ==========")
	for _, it := range items {
		if it.DiskDev != "" {
			fmt.Printf("dev=%-12s chip=%-12s temp=%.2f ℃\n", it.DiskDev, it.ChipName, it.TempC)
		}
	}
}
