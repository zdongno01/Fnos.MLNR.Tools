#pragma once
#include <Arduino.h>

// ===================== 硬件规模 =====================
// L16: ESP32-C3 strapping 引脚提醒——GPIO2/8/9 为启动模式选择引脚，
//   上电瞬间芯片采样这些引脚电平决定启动模式(下载/正常)。
//   本固件中 I2C_SDA=GPIO2、LED=GPIO8、I2C_SCL=GPIO9 均为 strapping 引脚，
//   若外设在复位时拉低/拉高这些引脚可能导致启动模式异常。
//   建议确保这些引脚在复位期间电平不受外设干扰（加串联电阻/确保上拉）。
#define FAN_COUNT           2
#define SW_COUNT            4
#define BTN_COUNT           4

// 风扇: FAN1=GPIO20, FAN2=GPIO21
// (2026-09-06 交换: 原 FAN1=GPIO21/FAN2=GPIO20, 因 GPIO20 路硬件调试需要对调)
// PWM通道使用0和2, 分属不同LEDC定时器, 保证两风扇频率可独立设置
const gpio_num_t FAN_PINS[FAN_COUNT]   = {GPIO_NUM_20, GPIO_NUM_21};
const uint8_t    FAN_PWM_CH[FAN_COUNT] = {0, 2};

// 开关: 开关1=GPIO5, 开关2=GPIO6, 开关3=GPIO7, 开关4=GPIO10
const gpio_num_t SW_PINS[SW_COUNT] = {GPIO_NUM_5, GPIO_NUM_6, GPIO_NUM_7, GPIO_NUM_10};

// 按钮: 按钮1=GPIO4, 按钮2=GPIO3, 按钮3=GPIO1, 按钮4=GPIO0 (无外部上拉, 固件启用内部上拉, 按下为低电平)
const gpio_num_t BTN_PINS[BTN_COUNT] = {GPIO_NUM_4, GPIO_NUM_3, GPIO_NUM_1, GPIO_NUM_0};

// ===================== 常量参数 =====================
#define VERSION             "NR_F2S4 0.1.0"
#define PWM_RESOLUTION      8       // 0~255
// 广播名 = "NR_F2S4_{BLE MAC后6位大写}"，运行时在 bleInit 中动态构造（FS.md 固件#1）
// BLE 特征收发分离(2026-09-21): 单特征 RW+Notify 时 Bluedroid 的特征值是收发
// 共用的存储槽(远程写入与固件 setValue/notify 均写同一槽), 二者并发竞争会互相
// 覆盖——主机偶发收到自己发出帧的回传(上位机日志 "unknown cmd 0x02/0x06" 自回环)。
// 拆分后写入只进指令特征, 永远不会再覆盖通知发送缓冲, 竞态根除。
// 注意: 指令特征沿用原 UUID(旧上位机仍能发现); 通知特征为新增 UUID,
// 新旧固件与上位机需配套升级(新上位机找不到通知特征时自动回退单特征模式)。
#define SERVICE_UUID        "4fafc201-1fb5-459e-8fcc-c5c9c331914b"
#define CHAR_UUID           "beb5483e-36e1-4688-b7f5-ea07361b26a8" // 指令特征(仅 WRITE, 上位机→固件)
#define CHAR_NOTIFY_UUID    "3a1c6f4d-8b2e-4d79-a5f0-c9e7b1d4a286" // 通知特征(仅 NOTIFY, 固件→上位机)
#define TWDT_TIMEOUT_S      10UL
#define SMOOTH_STEP_MS      80UL
#define CMD_THROTTLE_MS     350UL
// BLE 指令最大长度限制：
//   - 密钥交换明文帧（PUBKEY 分片 payload≤240 + 帧头6 + CRC2 ≤ 250）
//   - 加密帧 [IV12][明文帧][TAG16]：明文帧 ≤ 6+240+2=248 → 加密后 ≤ 276
//   128 原值仅够固定密钥下的加密帧；密钥交换（RSA-2048 公钥分片/密文 256B）需要 320。
#define CMD_BUF_MAX_LEN     320U    // BLE指令最大长度限制（密钥交换明文帧 + 加密帧余量）
#define BTN_DEBOUNCE_MS     40UL    // 按钮消抖时间
#define SW_DELAY_MAX_MS     600000UL // 开机延迟上限(10分钟)

// LED: GPIO8, 高电平灭灯, 低电平亮灯, 默认高电平
const gpio_num_t LED_PIN            = GPIO_NUM_8;
#define LED_FLASH_MS        500UL   // 按键触发LED亮灯时长

// ===================== 状态LED节奏 =====================
// UNINIT: 每秒闪2次(250ms亮/250ms灭) 然后暂停1s, 如此循环, 直至退出UNINIT
// WORK_WAIT: 每秒闪1次(周期1000ms) / WORK_RUN: 熄灭(按键触发短亮由button_ctrl管理)
#define LED_UNINIT_BLINK_MS      250UL   // 单次闪烁半周期(亮或灭)
#define LED_UNINIT_PAUSE_MS      1000UL  // 两次闪烁后的暂停时长
#define LED_WORK_WAIT_PERIOD_MS  1000UL
#define LED_STATE_TASK_MS        10UL    // LED状态任务扫描周期

// ===================== 上电重置(回UNINIT) =====================
// 上电瞬间按钮1+按钮3同时按住并持续5s → 强制重置回UNINIT。
// 按住计时期间LED常亮作为5s倒计时提示(松开任一键取消并恢复状态节奏)。
// 仅上电瞬间采样武装; 正常运行期间同时按下两键不触发重置。
// 按钮数组下标: BTN1=GPIO4(下标0), BTN3=GPIO1(下标2)
#define RESET_BTN_A_IDX    0       // GPIO4
#define RESET_BTN_B_IDX    2       // GPIO1
#define RESET_HOLD_MS      5000UL  // 上电时两键同按持续5s触发重置

// ===================== FS4: 应用层加密参数 =====================
#define AES_KEY_LEN        16U     // AES-128 密钥长度
#define GCM_IV_LEN         12U     // GCM IV长度
#define GCM_TAG_LEN        16U     // GCM 认证标签长度
#define ENC_BUF_LEN        600U    // 加密发送缓冲区长度

// 二进制帧协议下最大加密帧 = 帧(6+240) + IV12 + TAG16 = 274 < ENC_BUF_LEN(600)，
// 全部指令（最大 GETSR 帧 103B + 28 = 131B）均低于常见协商 MTU（≥244），单条 notify 发送。

// ===================== I2C 传感器 =====================
// I2C总线: SCL=GPIO9, SDA=GPIO2
// (2026-09-06 实测: 原定义 SCL=GPIO2/SDA=GPIO9 与扩展板 I2C 口实际走线相反,
//  导致按丝印正常接线时全地址 NAK、需交叉接线才通; 已对调引脚定义, 按丝印 SCL→SCL/SDA→SDA 正常接线即可)
const gpio_num_t I2C_SCL_PIN = GPIO_NUM_9;
const gpio_num_t I2C_SDA_PIN = GPIO_NUM_2;
#define I2C_FREQ_HZ        100000UL  // I2C频率: 联调暂用100kHz(与多数设备/库默认一致)。
                                      // 400kHz下若模块无板载上拉, 仅靠ESP32内部弱上拉(约45kΩ)
                                      // 上升沿过慢会导致地址NAK; 100kHz可读后再决定是否恢复400k
                                      // 并补外部上拉(4.7k~10kΩ)
#define SENSOR_INTERVAL_MS 2000UL    // 默认数据采集间隔2s
#define SENSOR_CH_MAX      4U        // I2C传感器通道数 CH0~CH3(与上位机MaxSensorChannels一致)

// AHT20寄存器/命令
#define AHT20_I2C_ADDR     0x38
#define AHT20_CMD_INIT     0xBE    // 初始化(软复位后)
#define AHT20_CMD_TRIGGER  0xAC    // 触发测量
#define AHT20_CMD_SOFTRESET 0xBA
#define AHT20_CAL_ENABLE   0x08    // 校准使能位(status bit3)
#define AHT20_BUSY_BIT     0x80    // 忙标志(status bit7)

// BMP280寄存器
#define BMP280_I2C_ADDR    0x77
#define BMP280_REG_CALIB   0x88    // 校准参数起始寄存器(0x88~0xA1, 24字节)
#define BMP280_REG_ID      0xD0
#define BMP280_CHIP_ID     0x58
#define BMP280_REG_RESET   0xE0
#define BMP280_REG_CONFIG  0xF5
#define BMP280_REG_CTRL    0xF4
#define BMP280_REG_PRESS   0xF7    // 压力/温度数据起始寄存器
#define BMP280_RESET_VAL   0xB6

// LM75 温度传感器寄存器
#define LM75_I2C_ADDR      0x48    // 默认地址 A2A1A0=000(0x48~0x4F可配)
#define LM75_REG_TEMP      0x00    // 温度寄存器(2字节大端, 0.5℃分辨率, 11bit有符号)

// HTU21D 温湿度传感器命令(0x40, 与SI7021同族)
#define HTU21D_I2C_ADDR    0x40
#define HTU21D_CMD_TEMP    0xE3    // 触发温度测量(hold master, 需配合时钟拉伸等待)
#define HTU21D_CMD_HUMI    0xE5    // 触发湿度测量(hold master)
#define HTU21D_CMD_TEMP_NOHOLD 0xF3 // 触发温度测量(no hold): 命令+STOP后固定延时再读, 推荐
#define HTU21D_CMD_HUMI_NOHOLD 0xF5 // 触发湿度测量(no hold)
#define HTU21D_CMD_RESET   0xFE    // 软复位(复位耗时最长15ms, 固件预留50ms等待)
#define HTU21D_REG_USER    0xE7    // 用户寄存器(读1字节, 用于在线检测)

// SensorData 载荷为 16 字节（见 sensor.h），经 EVT_SENSOR 0x82 二进制帧推送
// （FS.md 二进制协议，替代原 47B 独立特殊帧）
