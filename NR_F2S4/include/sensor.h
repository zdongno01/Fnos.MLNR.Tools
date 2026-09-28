#pragma once
#include <stdint.h>
// 必须先包含 FreeRTOS.h 再包含 semphr.h：task.h 内部使用 PRIVILEGED_FUNCTION /
// configSTACK_DEPTH_TYPE / portDONT_DISCARD 等宏，这些宏在 FreeRTOS.h 中定义。
// 若直接包含 semphr.h，task.h 会先于 FreeRTOS.h 被引入，导致上述宏未定义编译失败。
#include <freertos/FreeRTOS.h>
#include <freertos/semphr.h>
#include "hw_config.h"

// ===================== I2C传感器数据 =====================
// 传感器载荷结构体(16字节, 小端), 通过BLE NOTIFY推送。
// 线上明文帧 = [1B版本][1B CH_ID][1B CH_TYPE][SensorData 16B] = 19B, 见 bleSendSensorChannelData。
// 某型号不提供的字段填 NaN(0x7FC00000), 主机解析后归零展示。
typedef struct {
  float temperature; // 温度 ℃  4字节
  float humidity;    // 湿度(%) 4字节
  float pressure;    // 压力(Pa) 4字节
  float altitude;    // 海拔(m) 4字节
} SensorData;

// 每通道最新的传感器数据(定义于sensor.cpp)
extern SensorData g_sensor_ch[SENSOR_CH_MAX];
// 每通道驱动就绪状态
extern bool g_sensor_ok[SENSOR_CH_MAX];

// 每通道最近一次成功初始化的 type/addr（运行期配置变更时用于判定是否需要重初始化）
extern uint8_t g_sensor_init_type[SENSOR_CH_MAX];
extern uint8_t g_sensor_init_addr[SENSOR_CH_MAX];

// S3: I2C 总线互斥锁——secTask 线程(sensorReinitChannel)与 loop 线程(sensorChannelRead)
// 并发访问 Wire 总线时须获取此锁，防止总线卡死不可恢复
extern SemaphoreHandle_t g_i2c_mutex;

// L8: 原子读取单通道数据快照(secTask GETSR 线程用, 避免撕裂读 16B 结构体)
SensorData sensorGetChannelSnapshot(uint8_t ch);

// I2C总线与各通道传感器初始化, 返回true=至少一个通道就绪
bool sensorInit();

// 单通道重初始化(不重开 I2C 总线, 不影响其他通道)
// 返回 true=该通道就绪; 用于运行期 CFG ENABLED=1 时单独启用某通道
bool sensorReinitChannel(uint8_t ch);

// 周期采集任务(内部按各通道 interval_sec 独立节流)
void sensorTask();
