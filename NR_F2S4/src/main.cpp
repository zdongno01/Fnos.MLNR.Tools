#include <Arduino.h>
#include <esp_task_wdt.h>
#include <cstring>

#include "hw_config.h"
#include "nvs_store.h"
#include "fan_ctrl.h"
#include "switch_ctrl.h"
#include "button_ctrl.h"
#include "ble_service.h"
#include "app_state.h"
#include "security.h"
#include "sensor.h"
#include "binary_proto.h"

// ===================== SETUP =====================
void setup()
{
  Serial.begin(115200);
  // 启动横幅(含编译日期时间): 用于确认烧录的固件版本/是否最新构建
  Serial.printf("\n[BOOT] %s %s %s\n", VERSION, __DATE__, __TIME__);

  // 1.加载NVS配置
  loadConfigFromNVS();

  // 2.开关与按钮初始化
  switchInit();
  buttonInit();

  // 3.状态机初始化(依据NVS初始化标志决定UNINIT/WORK_WAIT)
  //    需在 applyRuntimeFromConfig 之前完成: UNINIT 状态开机时开关保持关闭(FS.md 固件#2)
  appStateInit();

  // 4.风扇PWM初始化 + 开机默认状态(UNINIT 下开关不上电, 其余按延迟上电)
  applyRuntimeFromConfig();

  // 4.1 BLE初始化并立即广播: 设备上电即被发现。固定密钥方案下无需RSA预生成,
  //     连接后立即启用固定密钥加密会话(见 ble_service ServerCb::onConnect)
  bleInit();

  // 5.看门狗 —— 必须在 secTaskStart 之前初始化:
  //    secTask 优先级2 高于 loop 任务, xTaskCreate 返回即抢占 setup,
  //    若 TWDT 未初始化, secTask 内的 esp_task_wdt_add(NULL) 返回
  //    ESP_ERR_INVALID_STATE, secTask 将永久脱离看门狗监控(死锁时无法复位)
  esp_task_wdt_init(TWDT_TIMEOUT_S, true);
  esp_task_wdt_add(NULL);

  // 4.2 启动加解密后台任务(24KB栈): btc回调仅3KB, AES-GCM加解密在其中执行会栈溢出
  //     (2026-09-05 实测 "A stack overflow in task BTC_TASK" 崩溃重启),
  //     故 onWrite 仅挂起接收帧, 由 secTask 在安全栈上解密+处理+加密回复
  secTaskStart();

  // 6.I2C传感器初始化(AHT20+BMP280)
  sensorInit();

  last_cmd_recv_ms = millis();
  Serial.println("TF-FAN2/SW4 Controller Ready");
}

// ===================== LOOP =====================
void loop()
{
  esp_task_wdt_reset(); // 喂狗
  uint32_t now = millis();

  // M3: 异步执行 onConnect 中延迟的 NVS 读取（btc 回调 3KB 栈不适合执行 NVS 操作）
  if(g_need_load_auth){
    g_need_load_auth = false;
    secLoadAuth();
  }

  // L7: 心跳超时仅在已连接状态下检测（未连接时不触发 fallback）
  // 心跳超时检测(全局心跳阈值, 无符号减法天然兼容millis溢出)
  if(ble_connected){
    uint32_t hb_limit = (uint32_t)g_hb_timeout_sec * 1000UL;
    bool timeout = ((now - last_cmd_recv_ms) > hb_limit);

    if(timeout && !heartbeat_timeout_flag){
      heartbeat_timeout_flag = true;
      // 调试模式: 打印心跳超时触发点
      if(g_debug_mode){
        Serial.printf("[DBG] HEARTBEAT TIMEOUT at %ums\n", (unsigned)now);
      }
      for(uint8_t i = 0; i < FAN_COUNT; i++){
        if(g_fan_cfg[i].enabled){
          target_spd[i] = g_fan_cfg[i].hb_fallback_spd;
          // 调试模式: 打印风扇切换到 fallback 转速
          if(g_debug_mode){
            Serial.printf("[DBG] FAN%u FALLBACK to %u at %ums\n",
                          (unsigned)(i + 1), (unsigned)g_fan_cfg[i].hb_fallback_spd, (unsigned)now);
          }
        }
      }
      // 心跳超时: 通知后断开静默客户端, WORK_RUN → WORK_WAIT, 重启广播
      // EVT_WARN 0x83 (WarnCode=1 心跳超时)
      bleSendWarnEvent(0x01);
      if(g_dev_state == STATE_WORK_RUN){
        bleForceDisconnect();
        appStateSet(STATE_WORK_WAIT);
      }
      startAdvertisingSafe();
    }
  }

  // 状态机任务(LED节奏 + WORK_WAIT→WORK_RUN判定)
  appStateTask();
  // 传感器周期采集与推送(2s间隔)
  sensorTask();
  // 平滑调速
  smoothSpeedTask();
  // 开关开机延迟上电
  switchBootTask();
  // 按钮扫描(含组合键强制重置检测)
  buttonTask();
  delay(10);
}
