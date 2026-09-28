#pragma once
#include <stdint.h>
#include "hw_config.h"

// ===================== 持久化参数结构体 =====================
struct FanConfig{
  uint8_t  enabled;         // 是否启用
  uint8_t  power_spd;       // 上电默认转速 0~100
  uint8_t  hb_fallback_spd; // 失联后备转速
  uint32_t pwm_freq;        // PWM频率 10000~30000
};

struct SwitchConfig{
  uint8_t  enabled;         // 是否启用
  uint8_t  power_on_state;  // 开机默认状态 1=高电平(开) 0=低电平(关)
  uint32_t power_on_delay;  // 开机延迟(毫秒)
  uint8_t  system;          // 系统开关标志（1=禁止二进制指令关闭）
};

// I2C设备类别(与上位机 SensorKind 枚举一致)
enum SensorType : uint8_t {
  SENSOR_NONE   = 0,   // 未配置
  SENSOR_AHT20  = 1,   // 温湿度 默认0x38
  SENSOR_BMP280 = 2,   // 气压+海拔 默认0x77
  SENSOR_LM75   = 3,   // 温度 默认0x48
  SENSOR_HTU21D = 4,   // 温湿度 默认0x40(与SI7021同族)
};

struct SensorConfig{
  uint8_t  type;           // I2C设备类别(SensorType), 1~4有效, 0=停用该通道
  uint8_t  addr;           // I2C地址(十进制, 范围0x08~0x77)
  uint8_t  enabled;        // 是否启用采集(默认0不启用)
  uint16_t interval_sec;   // 数据采集间隔(秒, 默认2, 范围1~3600)
};

// ===================== 全局配置（FS.md 上位机需求#7：心跳超时为全局配置） =====================
#define GLOBAL_HB_TIMEOUT_DEFAULT  15
#define GLOBAL_HB_TIMEOUT_MIN      3
#define GLOBAL_HB_TIMEOUT_MAX      60

// 全局心跳超时阈值(秒)，定义于 nvs_store.cpp
extern uint16_t g_hb_timeout_sec;

// BLE 广播名（空字符串表示使用默认 MAC 名），定义于 nvs_store.cpp
extern char g_ble_name[21];

// 调试模式开关（0/1），定义于 nvs_store.cpp
extern uint8_t g_debug_mode;

// 全局配置实例(定义于nvs_store.cpp)
extern FanConfig g_fan_cfg[FAN_COUNT];
extern SwitchConfig g_sw_cfg[SW_COUNT];
extern SensorConfig g_sensor_cfg[SENSOR_CH_MAX];

// 恢复默认配置
void fanConfigDefaults(FanConfig& c);
void swConfigDefaults(SwitchConfig& c, uint8_t idx);
void sensorConfigDefaults(SensorConfig& c, uint8_t idx);

// NVS 读写
void loadConfigFromNVS();
bool saveConfigToNVS();
bool factoryResetNVS();

// 将硬件运行状态同步到当前配置(上电/出厂复位共用)
void applyRuntimeFromConfig();
