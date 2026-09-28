#include "nvs_store.h"
#include "fan_ctrl.h"
#include "switch_ctrl.h"
#include "app_state.h"
#include <nvs_flash.h>
#include <cstring>

// ===================== 全局配置实例 =====================
FanConfig g_fan_cfg[FAN_COUNT];
SwitchConfig g_sw_cfg[SW_COUNT];
SensorConfig g_sensor_cfg[SENSOR_CH_MAX];
uint16_t g_hb_timeout_sec = GLOBAL_HB_TIMEOUT_DEFAULT;
char g_ble_name[21] = "";        // 空字符串表示使用默认 MAC 名
uint8_t g_debug_mode = 0;         // 调试模式开关

// NVS 命名空间
static const char* FAN_NVS_NS[FAN_COUNT] = {"fan1_cfg", "fan2_cfg"};
static const char* SW_NVS_NS[SW_COUNT]   = {"switch1_cfg", "switch2_cfg", "switch3_cfg", "switch4_cfg"};
static const char* SENSOR_NVS_NS         = "sensor_cfg";
static const char* GLOBAL_NVS_NS         = "global_cfg";
static const char NVS_KEY[]              = "cfg";
static const char NVS_HB_KEY[]           = "hb_timeout";
static const char NVS_BLE_NAME_KEY[]     = "ble_name";
static const char NVS_DEBUG_MODE_KEY[]   = "debug_mode";

void fanConfigDefaults(FanConfig& c)
{
  // L11: 清零结构体（含 padding），避免 NVS 写入未初始化字节
  memset(&c, 0, sizeof(c));
  c.enabled         = 0;     // 默认不启用
  c.power_spd       = 30;
  c.hb_fallback_spd = 30;
  c.pwm_freq        = 25000;
}

void swConfigDefaults(SwitchConfig& c, uint8_t idx)
{
  // L11: 清零结构体（含 padding）
  memset(&c, 0, sizeof(c));
  static const uint32_t DEF_DELAY[SW_COUNT] = {500, 2500, 4500, 6500};
  c.enabled        = 1;                  // 默认启用
  c.power_on_state = 1;                  // 默认开机为高电平(开)
  c.power_on_delay = DEF_DELAY[idx];
  c.system         = 0;                  // 默认非系统开关
}

// 各通道默认型号与I2C地址(与上位机 DefaultSensorChannels 一致)
void sensorConfigDefaults(SensorConfig& c, uint8_t idx)
{
  static const uint8_t DEF_TYPE[SENSOR_CH_MAX] = {SENSOR_AHT20, SENSOR_BMP280, SENSOR_LM75, SENSOR_HTU21D};
  static const uint8_t DEF_ADDR[SENSOR_CH_MAX] = {0x38, 0x77, 0x48, 0x40};
  if(idx >= SENSOR_CH_MAX) idx = 0;
  c.type         = DEF_TYPE[idx];
  c.addr         = DEF_ADDR[idx];
  c.enabled      = 0;      // 默认不启用
  c.interval_sec = 2;      // 默认采集间隔2秒
}

static void loadGlobalConfig()
{
  g_hb_timeout_sec = GLOBAL_HB_TIMEOUT_DEFAULT;
  g_ble_name[0] = '\0';       // 空字符串表示使用默认 MAC 名
  g_debug_mode = 0;
  nvs_handle_t hnd;
  if(nvs_open(GLOBAL_NVS_NS, NVS_READWRITE, &hnd) == ESP_OK){
    uint16_t v = 0;
    if(nvs_get_u16(hnd, NVS_HB_KEY, &v) == ESP_OK){
      if(v >= GLOBAL_HB_TIMEOUT_MIN && v <= GLOBAL_HB_TIMEOUT_MAX){
        g_hb_timeout_sec = v;
      }
    }
    // BLE 广播名（字符串，空或不存在则保持空字符串→使用默认 MAC 名）
    size_t nameLen = sizeof(g_ble_name);
    if(nvs_get_str(hnd, NVS_BLE_NAME_KEY, g_ble_name, &nameLen) != ESP_OK){
      g_ble_name[0] = '\0';
    }
    // 调试模式（u8，范围 0/1 校验）
    uint8_t dbg = 0;
    if(nvs_get_u8(hnd, NVS_DEBUG_MODE_KEY, &dbg) == ESP_OK){
      if(dbg <= 1) g_debug_mode = dbg;
    }
    nvs_close(hnd);
  }
}

static void loadFanConfig(uint8_t idx)
{
  FanConfig& cfg = g_fan_cfg[idx];
  fanConfigDefaults(cfg);

  nvs_handle_t hnd;
  if(nvs_open(FAN_NVS_NS[idx], NVS_READWRITE, &hnd) == ESP_OK){
    size_t len = sizeof(FanConfig);
    // M7: 检查返回值和实际长度，不匹配则恢复默认
    if(nvs_get_blob(hnd, NVS_KEY, &cfg, &len) != ESP_OK || len != sizeof(FanConfig)){
      fanConfigDefaults(cfg);
    }
    nvs_close(hnd);
  }

  // 范围校验: 不合法则恢复默认(与CFG指令下发的取值范围一致)
  if(cfg.enabled > 1 ||
     cfg.power_spd > 100 ||
     cfg.hb_fallback_spd > 100 ||
     cfg.pwm_freq < 10000 || cfg.pwm_freq > 30000){
    fanConfigDefaults(cfg);
  }
}

static void loadSwitchConfig(uint8_t idx)
{
  SwitchConfig& cfg = g_sw_cfg[idx];
  swConfigDefaults(cfg, idx);

  nvs_handle_t hnd;
  if(nvs_open(SW_NVS_NS[idx], NVS_READWRITE, &hnd) == ESP_OK){
    size_t len = sizeof(SwitchConfig);
    // M7: 检查返回值和实际长度，不匹配则恢复默认
    if(nvs_get_blob(hnd, NVS_KEY, &cfg, &len) != ESP_OK || len != sizeof(SwitchConfig)){
      swConfigDefaults(cfg, idx);
    }
    nvs_close(hnd);
  }

  // 范围校验: 不合法则恢复默认
  if(cfg.enabled > 1 ||
     cfg.power_on_state > 1 ||
     cfg.system > 1 ||
     cfg.power_on_delay > SW_DELAY_MAX_MS){
    swConfigDefaults(cfg, idx);
  }
}

// L11: 单通道传感器配置范围校验, 非法则恢复默认(与CFG指令取值范围一致)
static void validateSensorChannel(uint8_t idx)
{
  SensorConfig& cfg = g_sensor_cfg[idx];
  if(cfg.type > SENSOR_HTU21D ||
     (cfg.type != SENSOR_NONE && (cfg.addr < 0x08 || cfg.addr > 0x77)) ||
     cfg.enabled > 1 ||
     cfg.interval_sec < 1 || cfg.interval_sec > 3600){
    sensorConfigDefaults(cfg, idx);
  }
}

void loadConfigFromNVS()
{
  loadGlobalConfig();
  for(uint8_t i = 0; i < FAN_COUNT; i++) loadFanConfig(i);
  for(uint8_t i = 0; i < SW_COUNT; i++)  loadSwitchConfig(i);

  // L11: 传感器配置 blob 只读取一次, 再逐通道校验。
  //     此前每轮循环都全量重读同一 blob, 会覆盖上一轮已校验/恢复默认的通道,
  //     导致仅最后一个通道被校验; 现改为整组读取 + 逐通道校验
  for(uint8_t i = 0; i < SENSOR_CH_MAX; i++) sensorConfigDefaults(g_sensor_cfg[i], i);
  nvs_handle_t hnd;
  if(nvs_open(SENSOR_NVS_NS, NVS_READONLY, &hnd) == ESP_OK){
    size_t len = sizeof(g_sensor_cfg);
    bool loaded = (nvs_get_blob(hnd, NVS_KEY, g_sensor_cfg, &len) == ESP_OK &&
                   len == sizeof(g_sensor_cfg));
    nvs_close(hnd);
    if(!loaded){
      // 读取失败/结构尺寸变化(旧版无addr字段): 保持默认
      for(uint8_t i = 0; i < SENSOR_CH_MAX; i++) sensorConfigDefaults(g_sensor_cfg[i], i);
    }
  }
  for(uint8_t i = 0; i < SENSOR_CH_MAX; i++) validateSensorChannel(i);
}

bool saveConfigToNVS()
{
  bool all_ok = true;

  for(uint8_t i = 0; i < FAN_COUNT; i++){
    nvs_handle_t hnd;
    esp_err_t err = nvs_open(FAN_NVS_NS[i], NVS_READWRITE, &hnd);
    if(err != ESP_OK){ all_ok = false; continue; }
    // L11: 保存前清零 padding，避免写入未初始化字节
    FanConfig tmp;
    memset(&tmp, 0, sizeof(tmp));
    tmp.enabled = g_fan_cfg[i].enabled;
    tmp.power_spd = g_fan_cfg[i].power_spd;
    tmp.hb_fallback_spd = g_fan_cfg[i].hb_fallback_spd;
    tmp.pwm_freq = g_fan_cfg[i].pwm_freq;
    err = nvs_set_blob(hnd, NVS_KEY, &tmp, sizeof(FanConfig));
    if(err == ESP_OK) err = nvs_commit(hnd);
    nvs_close(hnd);
    if(err != ESP_OK) all_ok = false;
  }

  for(uint8_t i = 0; i < SW_COUNT; i++){
    nvs_handle_t hnd;
    esp_err_t err = nvs_open(SW_NVS_NS[i], NVS_READWRITE, &hnd);
    if(err != ESP_OK){ all_ok = false; continue; }
    // L11: 保存前清零 padding
    SwitchConfig tmp;
    memset(&tmp, 0, sizeof(tmp));
    tmp.enabled = g_sw_cfg[i].enabled;
    tmp.power_on_state = g_sw_cfg[i].power_on_state;
    tmp.power_on_delay = g_sw_cfg[i].power_on_delay;
    tmp.system = g_sw_cfg[i].system;
    err = nvs_set_blob(hnd, NVS_KEY, &tmp, sizeof(SwitchConfig));
    if(err == ESP_OK) err = nvs_commit(hnd);
    nvs_close(hnd);
    if(err != ESP_OK) all_ok = false;
  }

  {
    nvs_handle_t hnd;
    esp_err_t err = nvs_open(SENSOR_NVS_NS, NVS_READWRITE, &hnd);
    if(err == ESP_OK){
      err = nvs_set_blob(hnd, NVS_KEY, g_sensor_cfg, sizeof(g_sensor_cfg));
      if(err == ESP_OK) err = nvs_commit(hnd);
      nvs_close(hnd);
    }
    if(err != ESP_OK) all_ok = false;
  }

  // 全局配置（心跳超时阈值 / BLE 广播名 / 调试模式）
  {
    nvs_handle_t hnd;
    esp_err_t err = nvs_open(GLOBAL_NVS_NS, NVS_READWRITE, &hnd);
    if(err == ESP_OK){
      err = nvs_set_u16(hnd, NVS_HB_KEY, g_hb_timeout_sec);
      if(err == ESP_OK) err = nvs_set_str(hnd, NVS_BLE_NAME_KEY, g_ble_name);
      if(err == ESP_OK) err = nvs_set_u8(hnd, NVS_DEBUG_MODE_KEY, g_debug_mode);
      if(err == ESP_OK) err = nvs_commit(hnd);
      nvs_close(hnd);
    }
    if(err != ESP_OK) all_ok = false;
  }

  return all_ok;
}

void applyRuntimeFromConfig()
{
  // 风扇: 重置PWM与转速
  for(uint8_t i = 0; i < FAN_COUNT; i++){
    // L24: 回填 ledcSetup 实际配置频率(可能因分频舍入与请求值不同), 保持 GETF 与硬件一致
    {
      uint32_t actual = updatePwmFreq(i, g_fan_cfg[i].pwm_freq);
      if(actual > 0) g_fan_cfg[i].pwm_freq = actual;
    }
    current_spd[i]      = 0;
    target_spd[i]       = g_fan_cfg[i].enabled ? g_fan_cfg[i].power_spd : 0;
    last_received_spd[i]= 0;
    setFanOutput(i, 0);
  }

  // 开关: 立即置低电平; 已启用的按各自延迟上电
  // FS.md 固件需求#2: UNINIT 状态开机时所有开关保持关闭，不调度上电
  for(uint8_t i = 0; i < SW_COUNT; i++){
    setSwitchOutput(i, false, SW_COND_CMD);     // UNINIT 下 setSwitchOutput 不推送不记录
    if(g_dev_state == STATE_UNINIT){
      switchBootCancel(i);                      // UNINIT: 开关不上电
    }else if(g_sw_cfg[i].enabled){
      switchBootSchedule(i, g_sw_cfg[i].power_on_delay);
    }else{
      switchBootCancel(i);                      // 停用的开关不自动上电
    }
  }
}

bool factoryResetNVS()
{
  bool all_ok = true;
  // 清除所有命名空间
  const char* ns_list[] = {
    FAN_NVS_NS[0], FAN_NVS_NS[1],
    SW_NVS_NS[0], SW_NVS_NS[1], SW_NVS_NS[2], SW_NVS_NS[3],
    SENSOR_NVS_NS,
    GLOBAL_NVS_NS
  };
  for(const char* ns : ns_list){
    nvs_handle_t hnd;
    // L10: 检查擦除结果，失败时标记
    if(nvs_open(ns, NVS_READWRITE, &hnd) == ESP_OK){
      esp_err_t e1 = nvs_erase_all(hnd);
      esp_err_t e2 = (e1 == ESP_OK) ? nvs_commit(hnd) : e1;
      nvs_close(hnd);
      if(e1 != ESP_OK || e2 != ESP_OK) all_ok = false;
    }else{
      all_ok = false;
    }
  }

  // 内存恢复默认
  for(uint8_t i = 0; i < FAN_COUNT; i++) fanConfigDefaults(g_fan_cfg[i]);
  for(uint8_t i = 0; i < SW_COUNT; i++)  swConfigDefaults(g_sw_cfg[i], i);
  for(uint8_t i = 0; i < SENSOR_CH_MAX; i++) sensorConfigDefaults(g_sensor_cfg[i], i);
  g_hb_timeout_sec = GLOBAL_HB_TIMEOUT_DEFAULT;
  g_ble_name[0] = '\0';       // 清空广播名→恢复默认 MAC 名
  g_debug_mode = 0;            // 关闭调试模式

  // 同步硬件运行状态到默认配置，避免 GET 报告与实际不一致
  applyRuntimeFromConfig();
  return all_ok;
}
