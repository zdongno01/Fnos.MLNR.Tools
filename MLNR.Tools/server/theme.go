package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mlnr/logger"
)

// ===== fnOS 主题检测（需求 #6：上位机"自动"主题跟随 fnOS 系统主题） =====

// detectFnOSTheme 检测 fnOS 系统当前主题（"light" | "dark"）。
// 检测优先级：
//  1. 环境变量 MLNR_THEME_OVERRIDE=light|dark（强制覆盖，适用于无桌面会话的部署场景）
//  2. gsettings org.gnome.desktop.interface color-scheme（prefer-dark / prefer-light）
//  3. gsettings org.freedesktop.appearance color-scheme（xdg-desktop-portal 标准，GTK4/新应用）
//  4. gsettings org.gnome.desktop.interface gtk-theme（主题名含 dark）
//  5. gsettings org.deepin.dde.appearance gtk-theme（deepin/fnOS 外观 schema，存在时优先）
//  6. /etc/.ddenv 中 DDE_THEME_DARK_MODE（deepin 环境变量：1/true=深色）
//  7. ~/.config/gtk-3.0/settings.ini 的 gtk-application-prefer-dark-theme
//  8. ~/.config/gtk-4.0/settings.ini 的 gtk-application-prefer-dark-theme
//  9. ~/.config/kdeglobals [General] ColorScheme（KDE Plasma，含 dark 判定深色）
//
// gsettings 查询在无 DBUS_SESSION_BUS_ADDRESS 时自动尝试 /run/user/<uid>/bus（systemd user bus），
// 覆盖以 systemd 服务方式运行、无桌面会话环境变量的部署场景。
// 全部不可用或无法判定时回退 "light"。
// 每个来源的判定结果都会写入日志，便于真机排查（需求 #6）。
func detectFnOSTheme() string {
	// 1) 环境变量强制覆盖
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("MLNR_THEME_OVERRIDE"))); v != "" {
		if v == "dark" {
			logger.Info("theme", "detect: MLNR_THEME_OVERRIDE=dark")
			return "dark"
		}
		if v == "light" {
			logger.Info("theme", "detect: MLNR_THEME_OVERRIDE=light")
			return "light"
		}
		logger.Warn("theme", "ignored invalid MLNR_THEME_OVERRIDE=%q (expect light|dark)", v)
	}

	// 2) gsettings color-scheme（GNOME 新机制）
	if v, ok := gsettingsGet("org.gnome.desktop.interface", "color-scheme"); ok {
		lower := strings.ToLower(v)
		if strings.Contains(lower, "prefer-dark") {
			logger.Info("theme", "detect: gsettings color-scheme = %q -> dark", v)
			return "dark"
		}
		if strings.Contains(lower, "prefer-light") || lower == "''" || lower == "default" {
			logger.Info("theme", "detect: gsettings color-scheme = %q -> light", v)
			return "light"
		}
	}

	// 3) gsettings org.freedesktop.appearance color-scheme（xdg-desktop-portal 标准）
	if v, ok := gsettingsGet("org.freedesktop.appearance", "color-scheme"); ok {
		lower := strings.ToLower(v)
		if strings.Contains(lower, "prefer-dark") {
			logger.Info("theme", "detect: freedesktop.appearance color-scheme = %q -> dark", v)
			return "dark"
		}
		if strings.Contains(lower, "prefer-light") || lower == "''" || lower == "default" {
			logger.Info("theme", "detect: freedesktop.appearance color-scheme = %q -> light", v)
			return "light"
		}
	}

	// 4) gsettings gtk-theme 含 dark
	if v, ok := gsettingsGet("org.gnome.desktop.interface", "gtk-theme"); ok {
		if strings.Contains(strings.ToLower(v), "dark") {
			logger.Info("theme", "detect: gsettings gtk-theme = %q -> dark", v)
			return "dark"
		}
	}

	// 4) deepin/fnOS 外观 schema（先确认 schema 存在，避免无 schema 时报错噪音）
	if schemaExists("org.deepin.dde.appearance") {
		if v, ok := gsettingsGet("org.deepin.dde.appearance", "gtk-theme"); ok {
			if strings.Contains(strings.ToLower(v), "dark") {
				logger.Info("theme", "detect: deepin appearance gtk-theme = %q -> dark", v)
				return "dark"
			}
		}
	}

	// 5) /etc/.ddenv（deepin 旧版环境变量）
	if v, ok := ddenvDark(); ok {
		logger.Info("theme", "detect: /etc/.ddenv DDE_THEME_DARK_MODE -> %s", v)
		return v
	}

	// 7)/8) GTK 主题偏好（gtk-3.0 与 gtk-4.0）
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range []string{"gtk-3.0", "gtk-4.0"} {
			gtkCfg := filepath.Join(home, ".config", rel, "settings.ini")
			if v, ok := gtkIniDark(gtkCfg); ok {
				logger.Info("theme", "detect: %s -> %s", gtkCfg, v)
				return v
			}
		}
	}

	// 9) KDE Plasma：~/.config/kdeglobals [General] ColorScheme
	if home, err := os.UserHomeDir(); err == nil {
		kg := filepath.Join(home, ".config", "kdeglobals")
		if v, ok := kdeColorScheme(kg); ok {
			logger.Info("theme", "detect: %s -> %s", kg, v)
			return v
		}
	}

	logger.Info("theme", "detect: no theme source found, default light")
	return "light"
}

// kdeColorScheme 解析 KDE 全局配色 ~/.config/kdeglobals 的 [General] ColorScheme；
// 主题名含 dark 判深色，未配置返回 found=false。
func kdeColorScheme(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	inGeneral := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inGeneral = line == "[General]"
			continue
		}
		if inGeneral && strings.HasPrefix(line, "ColorScheme=") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "ColorScheme="))
			if strings.Contains(strings.ToLower(v), "dark") {
				return "dark", true
			}
			return "light", true
		}
	}
	return "", false
}

// gsettingsGet 读取 gsettings 键值；失败（无 DBus 会话/schema 不存在）返回 found=false。
// 无 DBUS_SESSION_BUS_ADDRESS 时自动尝试 /run/user/<uid>/bus（systemd user bus），
// 覆盖上位机以 systemd 服务运行、未继承桌面会话环境变量的场景（需求 #6 真机排查）。
func gsettingsGet(schema, key string) (string, bool) {
	for _, bus := range gsettingsBusPaths() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, "gsettings", "get", schema, key)
		if bus != "" {
			cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=unix:path="+bus)
		}
		out, err := cmd.Output()
		cancel()
		if err == nil {
			return strings.TrimSpace(string(out)), true
		}
	}
	return "", false
}

// gsettingsBusPaths 返回候选 DBus session bus 路径："" 表示继承当前环境，其余为 /run/user/<uid>/bus。
func gsettingsBusPaths() []string {
	paths := []string{""}
	entries, err := os.ReadDir("/run/user")
	if err != nil {
		return paths
	}
	for _, e := range entries {
		bus := filepath.Join("/run/user", e.Name(), "bus")
		if _, err := os.Stat(bus); err == nil {
			paths = append(paths, bus)
		}
	}
	return paths
}

// schemaExists 检查 gsettings schema 是否存在。
func schemaExists(schema string) bool {
	out, err := runCmdOutput("gsettings", 3*time.Second, "list-schemas")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == schema {
			return true
		}
	}
	return false
}

// ddenvDark 解析 /etc/.ddenv 的 DDE_THEME_DARK_MODE；未找到该变量时返回 found=false。
func ddenvDark() (string, bool) {
	b, err := os.ReadFile("/etc/.ddenv")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "DDE_THEME_DARK_MODE=") {
			continue
		}
		val := strings.Trim(strings.TrimPrefix(line, "DDE_THEME_DARK_MODE="), `"'`)
		if val == "1" || strings.EqualFold(val, "true") {
			return "dark", true
		}
		return "light", true
	}
	return "", false
}

// gtkIniDark 解析 GTK settings.ini 的 gtk-application-prefer-dark-theme；未配置时返回 found=false。
func gtkIniDark(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "gtk-application-prefer-dark-theme=") {
			continue
		}
		val := strings.Trim(strings.TrimPrefix(line, "gtk-application-prefer-dark-theme="), `"'`)
		if val == "1" || strings.EqualFold(val, "true") {
			return "dark", true
		}
		return "light", true
	}
	return "", false
}

// getSystemTheme 返回 fnOS 系统当前主题（上位机"自动"模式的数据源）。
func (h *Handlers) getSystemTheme(c *gin.Context) {
	c.JSON(200, gin.H{
		"theme":  detectFnOSTheme(),
		"source": "fnos",
	})
}
