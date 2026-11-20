# MLNR.Tools 麻辣牛肉

麻辣牛肉是 fnOS（飞牛OS）第三方应用，通过 **BLE 蓝牙**连接「麻辣牛肉控制板」（NR_F2S4 固件），实现 **风扇控制** 与 **硬盘组电源管理**。支持温度自动调速、定时计划任务、事件触发脚本等能力。

- **应用标识**：`mlnr`（`mlnr.Application`）
- **版本**：0.1.3
- **平台**：x86
- **维护者**：墨迹

## 功能特性

### 风扇控制
- 2 个风扇通道（FAN1/FAN2），手动调速 / 自动调速（跟随温度曲线）
- 自动调速基于系统温度采集（hwmon / smartctl / drivetemp）
- 风扇硬件参数配置下发（PWM 曲线、启停等）

### 硬盘组管理
- 4 个硬盘组，复用硬件开关通道（SW1~SW4）控制硬盘电源
- **安全上电**：SW ON → 等待上电延时 → 检测块设备 → 自动挂载
- **安全下线**：卸载挂载点 → 机械盘停转（SAFE STANDBY）→ SW OFF；强制下线需二次确认
- 设备路径按硬盘序列号（SN）动态解析，避免接口变动导致误判
- 硬盘组操作日志与累计统计（定时落盘）

### 温度监控
- 采集 CPU / 主板 / 硬盘（SATA 走 drivetemp hwmon，无需 root）温度
- I2C 环境传感器通道
- 历史趋势存储：14 天环形缓冲（每分钟 1 点），前端图表展示

### 定时计划与事件引擎
- 时间任务：一次性 / 每日 / 每周 / 每月，多触发器组合
- 事件触发：日志事件（log）、监控脚本（monitor）
- 执行器：风扇控制 / 硬盘组控制 / 控制任务（串联，支持延迟与失败重试）/ 执行脚本（如 ntfy 通知、rsync 备份）
- 防重复触发：以「任务+触发器」执行日志为唯一事实源，重启不重复执行
- 执行日志按天落盘，保留天数可配置

### BLE 通信与安全
- 二进制帧协议：`[Magic(1)=0xAA][SeqID(1)][CmdID(1)][PayloadLen(1)][Payload(N)][CRC16(2)]`（CCITT-FALSE）
- 加密会话：AES-128-GCM，密钥经 RSA-2048-OAEP 动态分发；固件重置后密钥作废需重新初始化
- 固件广播名前缀 `NR_F2S4`，收发特征分离（旧固件自动回退单特征模式）
- 心跳保活（5s）与断线自动重连、开机自动回连（等待 BlueZ 就绪）

## 系统架构

```
┌─────────────┐  fnOS 网关   ┌──────────────────┐   BLE (AES-GCM)   ┌────────────┐
│  fnOS 桌面   │ ──────────▶ │  Go 常驻服务       │ ────────────────▶ │ NR_F2S4     │
│  (iframe UI) │  app.sock   │  /app/mlnr        │                   │ 控制板固件   │
└─────────────┘             │  HTTP + WebSocket │                   │ FAN×2 SW×4  │
                            └──────────────────┘                   └────────────┘
                                     │ lsblk/smartctl/hwmon/df/mount(privRun)
                                     ▼
                              系统磁盘 / 挂载 / 温度
```

### 目录结构

```
MLNR.Tools/
├── manifest              # FPK 清单（应用名/版本/桌面入口等）
├── config/
│   ├── privilege         # 运行身份：root（服务进程降权到包用户 mlnr）
│   └── resource          # 数据共享目录授权
├── cmd/                  # FPK 生命周期脚本（bash）
│   ├── main              # 服务启停（Unix Socket、日志轮转）
│   ├── install_callback  # 安装回调（source grant_privileges）
│   ├── upgrade_callback  # 升级回调
│   ├── uninstall_*       # 卸载清理
│   └── grant_privileges  # 特权授权：sudoers 白名单 + smartctl/nvme setuid
├── server/               # Go 后端源码
│   ├── main.go           # 入口：组件装配（BLE/Disk/Thermal/Scheduler/Hub）
│   ├── ble.go            # BLE 管理器：连接/帧协议/心跳/重连
│   ├── security.go       # RSA-2048 + AES-128-GCM 安全会话
│   ├── disk.go           # 硬盘组管理器：上下电/挂载/卸载
│   ├── diskinfo.go       # 磁盘信息全量缓存（SN 动态解析）
│   ├── thermal.go        # 温度采集/历史存储/自动调速
│   ├── scheduler.go      # 定时计划调度引擎
│   ├── scheduler_events.go # 事件型触发器（log/monitor）
│   ├── resources_store.go # 监控脚本/执行脚本/日志事件资源
│   ├── handlers*.go      # REST API 处理器
│   ├── router.go         # 路由（统一前缀 /app/mlnr/api）
│   ├── ws.go             # WebSocket Hub 实时推送
│   ├── store.go          # 设置持久化（settings.json）
│   └── demo_mode.go      # 演示模式（mlnr_BLE_MOCK=1）
├── ui-src/               # 前端源码（TypeScript + Vite + Tailwind）
│   └── src/views/        # 页面：总览/风扇/硬盘/传感器/计划/连接/日志/设置
└── app/                  # 构建产物（FPK 打包内容）
    ├── mlnr              # Go 二进制
    └── ui/               # 前端静态资源（Vite 构建输出）
```

## 权限模型

采用**特权与业务分离**的模型：

- **生命周期脚本（`cmd/*`）以 root 运行**（`config/privilege` 中 `run-as: root`）。只有 root 才能写 `/etc/sudoers.d/mlnr`、执行 `chmod u+s`、把数据目录属主改为包用户。`install_callback` / `upgrade_callback` 都会 `source grant_privileges`，`cmd/main` 每次启动也会重新 source 一次（自愈式授权，升级或 sudoers 被清理后无需重装）。
- **服务进程（`app/mlnr`）降权到包用户 `mlnr` 运行**，由 `cmd/main` 通过 `runuser -u "$TRIM_USERNAME"` 启动，业务进程不具备 root 能力。
- **服务进程的特权操作通过 `sudo -n` 提权**，由 `grant_privileges` 写入 NOPASSWD 白名单。

| 授权方式 | 命令 | 用途 |
|---|---|---|
| sudoers NOPASSWD 白名单 | `mount` / `umount` / `hdparm` / `fuser` | 挂载、卸载、硬盘停转、强制卸载 |
| setuid | `smartctl` / `nvme` | 非 root 读取磁盘温度 |

授权文件 `/etc/sudoers.d/mlnr` 由 root 生命周期脚本生成，权限固定 `root:root 0440`，写入前用 `visudo -cf` 校验、写入后用 `sudo -n -l -U mlnr` 复查；任一步失败都会写入 `TRIM_TEMP_LOGFILE` 让应用中心弹窗提示。卸载应用时 `uninstall_callback` 会自动删除该文件。

> 由于第三方应用默认 `run-as` 为 `package`，改用 root 生命周期脚本后不再走"安装脚本无权限、需人工 `visudo`"的路径；若仍需手动修复，可在 fnOS 上以 root 执行：

```bash
echo 'mlnr ALL=(root) NOPASSWD: /usr/bin/mount, /usr/bin/umount, /usr/bin/hdparm, /usr/bin/fuser, /usr/bin/smartctl' > /etc/sudoers.d/mlnr
chmod 0440 /etc/sudoers.d/mlnr && visudo -c
```

## 开发

### 环境要求

- Go ≥ 1.25（后端）
- Node.js + npm（前端）
- Linux（蓝牙与磁盘操作依赖 BlueZ / util-linux）

### 后端（开发模式监听 TCP）

```bash
cd server
go run . -dev          # 监听 127.0.0.1:8088，数据目录为当前目录
go test ./...          # 单元测试
```

环境变量：`mlnr_SOCK` / `mlnr_UIDIR` / `mlnr_DATADIR` / `mlnr_ADDR` / `mlnr_LOG_LEVEL` / `mlnr_BLE_MOCK`。

### 前端（Vite 开发服务器）

```bash
cd ui-src
npm install
npm run dev             # 端口 5180，API/WS 代理到 127.0.0.1:8088
npm run build           # 产物输出到 ../app/ui（base=/app/mlnr/）
```

### 演示模式

无真实硬件时设置 `mlnr_BLE_MOCK=1`，注入 BLE 与温度模拟数据，用于界面截图与演示。

### 生产构建

```bash
cd server && go build -o ../app/mlnr .    # 后端二进制
cd ui-src && npm run build                # 前端资源 → app/ui
```

随后按 fnos-fpk-dev 规范将 `manifest`、`config/`、`cmd/`、`app/` 打包为 FPK。

## REST API 概览

统一前缀 `/app/mlnr/api`（写操作需管理员身份，经 fnOS 网关用户头鉴权）：

- 连接管理：`POST /connect` `/disconnect` `/scan` `/handshake`
- 风扇：`POST /fan/speed` `/fan/mode` `/fan/config`
- 硬盘组：`POST /disk/group/:id/poweron|poweroff|forceoff|mount|unmount`，`GET /disks/list|partitions|logs`
- 定时计划：`GET|POST|PUT|DELETE /schedules`，`POST /schedules/:id/run`，`GET /schedule-logs`
- 计划资源：`/monitor-scripts` `/exec-scripts` `/log-events`（CRUD + test）
- 温度与历史：`GET /temperature` `/history`
- 设置：`GET|PUT /settings` `/global/config` `/ble/name`
- WebSocket：`/app/mlnr/ws`（实时状态/温度/转速推送）
