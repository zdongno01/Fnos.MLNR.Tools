#include "app_state.h"
#include "hw_config.h"
#include "ble_service.h"
#include "security.h"
#include "nvs_store.h"
#include <Arduino.h>

// ===================== 设备状态机 =====================
volatile uint8_t g_dev_state = STATE_UNINIT;
volatile bool g_led_held_on = false;

void appStateInit()
{
  // 启动时加载授权数据(MAC/密钥), 依据NVS记录决定初始状态
  secLoadAuth();
  g_dev_state = secIsInitialized() ? STATE_WORK_WAIT : STATE_UNINIT;
  Serial.printf("[STATE] init -> %s\n",
                g_dev_state == STATE_UNINIT ? "UNINIT" :
                (g_dev_state == STATE_WORK_WAIT ? "WORK_WAIT" : "WORK_RUN"));
}

void appStateSet(uint8_t s)
{
  // L17: 拒绝非法状态值(>STATE_WORK_RUN), 防止误传进入未知状态
  if(s > STATE_WORK_RUN) return;
  if(g_dev_state == s) return;
  g_dev_state = s;
  const char* name = (s == STATE_UNINIT) ? "UNINIT" :
                     (s == STATE_WORK_WAIT) ? "WORK_WAIT" : "WORK_RUN";
  Serial.printf("[STATE] -> %s\n", name);
  // FS5-4: 规格要求WORK_RUN下LED熄灭; 若切换发生在闪烁周期"亮"的半段,
  // 无人熄灭会导致LED常亮, 故显式置灭(WORK_RUN下按键触发的短亮仍由button_ctrl管理)
  if(s == STATE_WORK_RUN) digitalWrite(LED_PIN, HIGH);
}

void appStateTask()
{
  static uint32_t last_ms = 0;
  uint32_t now = millis();
  if(now - last_ms < LED_STATE_TASK_MS) return;
  last_ms = now;

  // LED节奏: UNINIT闪2次停1s循环; WORK_WAIT每秒闪1次 (低电平亮)
  // WORK_RUN下LED由button_ctrl管理(按键触发亮500ms), 此处不干预
  // 上电重置计时中LED常亮(由button_ctrl驱动), 此处跳过节奏, 避免互相覆盖
  if(!g_led_held_on){
    if(g_dev_state == STATE_UNINIT){
      // 周期 = 2次闪烁(250亮/250灭 ×2) + 暂停1s = 2000ms
      // 亮: [0,250) 与 [500,750); 其余灭(含最后1s暂停)
      uint32_t cyc = 4 * LED_UNINIT_BLINK_MS + LED_UNINIT_PAUSE_MS;
      uint32_t ph  = now % cyc;
      bool on = (ph < LED_UNINIT_BLINK_MS) ||
                (ph >= 2 * LED_UNINIT_BLINK_MS && ph < 3 * LED_UNINIT_BLINK_MS);
      digitalWrite(LED_PIN, on ? LOW : HIGH);
    }else if(g_dev_state == STATE_WORK_WAIT){
      uint32_t period = LED_WORK_WAIT_PERIOD_MS;
      digitalWrite(LED_PIN, ((now % period) < period / 2) ? LOW : HIGH);
    }
  }

  // WORK_WAIT下授权设备已连接 → 进入WORK_RUN
  if(g_dev_state == STATE_WORK_WAIT && ble_connected && ble_authorized){
    appStateSet(STATE_WORK_RUN);
  }
}

void appStateForceReset()
{
  Serial.println("[STATE] FORCE RESET -> UNINIT");
  // 1.断开当前连接
  bleForceDisconnect();
  // 2.清除NVS初始化标志与会话密钥（密钥作废：再连必须重新走初始化密钥交换）
  secClearAuth();
  // 3.先切 UNINIT 状态 — applyRuntimeFromConfig(由 factoryResetNVS 内部调用)
  //    会根据 g_dev_state 决定是否取消开关开机延迟调度。
  //    必须在 factoryResetNVS 之前切状态, 否则开关会按默认启用 + 延迟时间调度上电,
  //    违反"强制重置不动开关、UNINIT 状态保持开关关闭"的语义
  appStateSet(STATE_UNINIT);
  // 4.业务配置恢复默认(内部调用 applyRuntimeFromConfig, 此时看到 UNINIT → 取消所有 boot 调度)
  factoryResetNVS();
  // 5.重新广播(允许任意设备连接)
  startAdvertisingSafe();
}
