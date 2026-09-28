#include "ble_service.h"
#include "cmd_parser.h"
#include "security.h"
#include "app_state.h"
#include "sensor.h"
#include "nvs_store.h"
#include "binary_proto.h"
#include "link_quality.h"
#include <freertos/FreeRTOS.h>
#include <freertos/semphr.h>
#include <esp_mac.h>
#include <esp_gap_ble_api.h>
#include <esp_task_wdt.h>

// ===================== BLE 运行时状态 =====================
// L1: 跨线程读写的变量加 volatile
volatile bool ble_connected          = false;
volatile bool ble_authorized         = false;
volatile bool heartbeat_timeout_flag = false;
bool     adv_running            = false;
volatile uint32_t last_cmd_recv_ms   = 0;
// M3: onConnect 中置标志，由 loop 异步执行 secLoadAuth
volatile bool g_need_load_auth       = false;

static BLEServer* pServer = nullptr;
// 收发分离(2026-09-21, 根除自回环竞态): Bluedroid 特征值是收发共用的存储槽,
// 单特征时远程写入(BTC 任务 setValue)与固件 setValue/notify 并发竞争互相覆盖,
// 导致主机偶发收到自己发出帧的回传。拆分后指令只写指令特征, 通知只发通知特征。
static BLECharacteristic* pCharWrite  = nullptr;   // 指令接收特征(仅 WRITE, 上位机→固件)
static BLECharacteristic* pCharNotify = nullptr;   // 通知发送特征(仅 NOTIFY, 固件→上位机)

static BLEAdvertising*    pAdv     = nullptr;

// M2: 广播启停临界区 spinlock（ESP32-C3 RISC-V 单核，portENTER_CRITICAL 仍需 spinlock 参数）
static portMUX_TYPE g_adv_spinlock = portMUX_INITIALIZER_UNLOCKED;

// L14: 跨线程读写加 volatile —— onConnect(btc 回调线程)写, bleForceDisconnect(loop/secTask)读,
//      快速断开/重连交替时避免读到陈旧连接 ID
static volatile uint16_t cur_conn_id = 0;       // 当前连接ID

// 默认广播名（基于 BLE MAC 后6位），bleInit 中一次性构造；
// g_ble_name 非空时优先使用 g_ble_name，否则使用本默认名
static char s_defaultDevName[21] = "NR_F2S4_000000";

// 获取当前生效的广播名（g_ble_name 非空则用之，否则用默认 MAC 名）
static const char* effectiveDevName()
{
  return (g_ble_name[0] != '\0') ? g_ble_name : s_defaultDevName;
}

static uint8_t encBuf[ENC_BUF_LEN];             // 加密发送缓冲区

// FS5-6: 通信互斥(递归锁)。所有 notify 发送共用encBuf与pCharNotify->notify,
// 指令回复(BLE回调线程)与心跳/按钮/传感器帧(loop线程)并发调用; 递归锁允许
// 外层持锁构建帧后内层发送重入
static SemaphoreHandle_t commMutex = nullptr;

// ===================== 接收挂起队列(btc回调 → secTask) =====================
// btc 任务栈仅 3KB, 而 mbedtls AES-GCM 加解密(setkey 需约 4KB 栈) 在其中执行会
// 栈溢出导致设备崩溃重启(2026-09-05 实测 "A stack overflow in task BTC_TASK")。
// 故 CharCb::onWrite 仅把接收帧拷贝进该环形队列并置信号量, 立即返回;
// 由 secTask(8KB栈) 异步完成 解密→帧解析→指令处理→加密回复。
#define RX_QUEUE_DEPTH 4
static uint8_t  rxQueue[RX_QUEUE_DEPTH][CMD_BUF_MAX_LEN];
static volatile uint16_t rxQueueLen[RX_QUEUE_DEPTH];
static volatile uint8_t rxHead = 0, rxTail = 0;
static SemaphoreHandle_t rxSem = nullptr;

// 通信加锁(超时250ms, 失败返回false应放弃本次发送/解析, 避免长时间阻塞BLE回调)
bool bleCommLock()
{
  if(commMutex == nullptr) return true; // bleInit完成前为单线程阶段
  return (xSemaphoreTakeRecursive(commMutex, pdMS_TO_TICKS(250)) == pdTRUE);
}

void bleCommUnlock()
{
  if(commMutex != nullptr) xSemaphoreGiveRecursive(commMutex);
}

// 加密或明文单条 notify 发送(调用方已持有 commMutex), 走专用通知特征
static void bleNotifyRaw(uint8_t* data, size_t len)
{
  if(len == 0 || pCharNotify == nullptr) return;
  pCharNotify->setValue(data, len);
  pCharNotify->notify();
}

// ===================== 二进制帧发送（FS.md） =====================
// 发送缓冲: 密钥交换应答（PUBKEY ACK 载荷 257B + 帧头6 + CRC2 = 265）需超过 BP_MAX_PAYLOAD
void sendReplyFrame(uint8_t seq, uint8_t cmdId, const uint8_t* payload, uint8_t payloadLen)
{
  if(!ble_connected || pCharNotify == nullptr) return;
  if(!bleCommLock()) return;

  uint8_t frame[CMD_BUF_MAX_LEN];
  uint16_t flen = bpBuildFrame(seq, cmdId, payload, payloadLen, frame, sizeof(frame));
  if(flen == 0){
    bleCommUnlock();
    return;
  }

  // 调试模式: 打印发送的完整二进制帧（含 Magic/Seq/Cmd/Len/Payload/CRC）
  if(g_debug_mode){
    Serial.printf("[DBG] TX:");
    for(uint16_t i = 0; i < flen; i++) Serial.printf(" %02X", frame[i]);
    Serial.printf("\n");
  }

  // 会话加密开启: 完整二进制帧经 AES-GCM 加密后通知 [IV12][CT][TAG16]
  if(secSessionEnabled()){
    size_t elen = 0;
    if(secEncrypt(frame, flen, encBuf, sizeof(encBuf), &elen)){
      bleNotifyRaw(encBuf, elen);
    }
  }else{
    bleNotifyRaw(frame, flen);
  }
  bleCommUnlock();
}

// 强制明文发送（密钥交换 HELLO 探测应答；即使会话加密已开启也发明文，供上位机无密钥时识别）
void sendReplyFramePlain(uint8_t seq, uint8_t cmdId, const uint8_t* payload, uint8_t payloadLen)
{
  if(!ble_connected || pCharNotify == nullptr) return;
  if(!bleCommLock()) return;

  uint8_t frame[CMD_BUF_MAX_LEN];
  uint16_t flen = bpBuildFrame(seq, cmdId, payload, payloadLen, frame, sizeof(frame));
  if(flen > 0) bleNotifyRaw(frame, flen);
  bleCommUnlock();
}

// ---- 事件帧（SeqID=0） ----
void bleSendButtonEvent(uint8_t swId, uint8_t state, uint8_t cond)
{
  const uint8_t p[3] = { swId, state, cond };   // SwId:1~SW_COUNT, State:0/1, Cond:SwEventCond
  sendReplyFrame(0, BP_EVT_BTN, p, 3);
}

void bleSendWarnEvent(uint8_t warnCode)
{
  const uint8_t p[1] = { warnCode };      // 1=心跳超时
  sendReplyFrame(0, BP_EVT_WARN, p, 1);
}

void bleSendSensorChannelData(uint8_t chIdx)
{
  // 仅在初始化成功且与授权设备建立加密会话后推送,
  // 未初始化或未建立会话时不推送任何数据
  if(!secSessionEnabled()) return;
  if(!ble_connected || pCharNotify == nullptr || chIdx >= SENSOR_CH_MAX) return;
  if(g_sensor_cfg[chIdx].type == SENSOR_NONE) return;

  // FS5-6: 与指令回复共用encBuf/pCharNotify, 须持通信锁互斥; 先取锁再加密,
  // 避免并发写坏encBuf
  if(!bleCommLock()) return;

  // EVT_SENSOR 0x82 帧载荷: [ChId][Kind][Temp f32][Hum f32][Press f32][Alt f32] = 18B
  uint8_t payload[BP_EVT_SENSOR_LEN];
  payload[0] = chIdx;
  payload[1] = g_sensor_cfg[chIdx].type;
  // L8: 通过 sensorGetChannelSnapshot 原子读取, 避免 loop 写/secTask 读并发导致 16B 结构体撕裂
  SensorData snap = sensorGetChannelSnapshot(chIdx);
  memcpy(payload + 2, (const uint8_t*)&snap, sizeof(SensorData)); // Temp/Hum/Press/Alt

  uint8_t frame[BP_MIN_FRAME + BP_EVT_SENSOR_LEN];
  uint16_t flen = bpBuildFrame(0, BP_EVT_SENSOR, payload, BP_EVT_SENSOR_LEN, frame, sizeof(frame));
  if(flen > 0){
    size_t elen = 0;
    if(secEncrypt(frame, flen, encBuf, sizeof(encBuf), &elen)){
      pCharNotify->setValue(encBuf, elen);
      pCharNotify->notify();
      // FS5: 传感器推送成功视为一次心跳(会话活跃), 重置心跳超时计时
      last_cmd_recv_ms = millis();
      heartbeat_timeout_flag = false;
    }
  }
  bleCommUnlock();
}

void stopAdvertisingSafe()
{
  // M2: 临界区保护 adv_running 检查-执行原子性（ESP32-C3 单核，仅禁中断极短）
  taskENTER_CRITICAL(&g_adv_spinlock);
  if(pAdv != nullptr && adv_running){
    pAdv->stop();
    adv_running = false;
  }
  taskEXIT_CRITICAL(&g_adv_spinlock);
}

void startAdvertisingSafe()
{
  // M2: 临界区保护 adv_running 检查-执行原子性
  taskENTER_CRITICAL(&g_adv_spinlock);
  if(pAdv != nullptr && !adv_running){
    pAdv->start();
    adv_running = true;
  }
  taskEXIT_CRITICAL(&g_adv_spinlock);
}

void bleForceDisconnect()
{
  if(ble_connected && pServer != nullptr){
    pServer->disconnect(cur_conn_id);
  }
}

// 运行时更新 BLE 广播名
void bleUpdateDeviceName()
{
  const char* name = effectiveDevName();
  // 已连接: 仅更新内存（esp_ble_gap_set_device_name 改变 GAP 设备名），
  // 下次断开重启广播时新名字生效
  esp_ble_gap_set_device_name(name);
  if(!ble_connected && adv_running){
    // 未连接且正在广播: 停广播→改设备名→重启广播，使新名字立即生效
    stopAdvertisingSafe();
    startAdvertisingSafe();
  }
  Serial.printf("[BLE] device name updated: %s\n", name);
}

// ===================== BLE 连接回调 =====================
class ServerCb : public BLEServerCallbacks{
  void onConnect(BLEServer* srv, esp_ble_gatts_cb_param_t* param) override{
    cur_conn_id = param->connect.conn_id;

    // M3: 不在 btc 回调(3KB栈)中执行 NVS 读取，改为置标志由 loop/secTask 异步处理
    g_need_load_auth = true;

    ble_connected  = true;
    // M4: 连接不自动授权，须等 secTask 成功解密第一帧后才置 true
    ble_authorized = false;
    last_cmd_recv_ms = millis();
    heartbeat_timeout_flag = false;
    // 新会话：重置链路质量统计（SeqID 基线从 0 重新开始，避免误判）
    lqInit();
    // 密钥交换分片状态重置
    keReset();
    // 连接成功：停止广播，仅维持当前设备连接
    stopAdvertisingSafe();
  }
  void onDisconnect(BLEServer* srv) override{
    ble_connected  = false;
    ble_authorized = false;
    heartbeat_timeout_flag = false;
    // 断开只清内存会话密钥（NVS 密钥保留，重连直接恢复加密会话）
    secClearSessionKeyMemory();
    keReset();
    // WORK_RUN下连接断开 → 回到WORK_WAIT等待授权设备
    if(g_dev_state == STATE_WORK_RUN) appStateSet(STATE_WORK_WAIT);
    // 断开连接：重启广播等待授权设备
    startAdvertisingSafe();
  }
};

// ===================== 业务特征读写回调 =====================
class CharCb : public BLECharacteristicCallbacks{
  void onWrite(BLECharacteristic* ch) override{
    size_t dataLen = ch->getLength();
    const uint8_t* data = ch->getData();
    if(dataLen == 0 || dataLen >= CMD_BUF_MAX_LEN) return;

    // 固定密钥方案: 会话始终加密。btc任务栈仅3KB, 严禁在其中执行 AES-GCM
    // 加解密(mbedtls AES setkey 需约4KB栈 → 3KB必溢出 → 设备崩溃重启)。
    // 故此处仅拷贝接收帧到挂起队列并通知 secTask(8KB栈) 异步处理。
    if(rxSem != nullptr){
      uint8_t next = (uint8_t)((rxHead + 1) % RX_QUEUE_DEPTH);
      if(next == rxTail) return; // 队列满, 丢弃本次(上位机指令为同步等待, 极少发生)
      memcpy(rxQueue[rxHead], data, dataLen);
      rxQueueLen[rxHead] = (uint16_t)dataLen;
      rxHead = next;
      xSemaphoreGive(rxSem);
    }
  }
};

// secTask: 8KB栈后台任务, 消费挂起队列中的接收帧。
// 在安全栈上执行: 解密(固定密钥 AES-GCM) → 二进制帧解析 → 指令处理 → 加密回复。
static void secTask(void* arg)
{
  // L18: 纳入 TWDT 监控，用超时等待以便定期喂狗
  esp_task_wdt_add(NULL);

  for(;;){
    // L18: 用超时等待（2s），无帧时也能复位看门狗
    if(!xSemaphoreTake(rxSem, pdMS_TO_TICKS(2000))){
      esp_task_wdt_reset();
      continue;
    }
    esp_task_wdt_reset();

    // 取一条挂起帧
    uint8_t buf[CMD_BUF_MAX_LEN];
    uint16_t len = rxQueueLen[rxTail];
    memcpy(buf, rxQueue[rxTail], len);
    rxTail = (uint8_t)((rxTail + 1) % RX_QUEUE_DEPTH);

    // 指令处理全程持通信锁, 保护发送不被 loop 线程的心跳/传感器帧打断;
    // 递归锁允许内部 sendReplyFrame 重入
    if(!bleCommLock()) continue;

    // 会话密钥方案:
    //   - 无会话密钥(UNINIT 设备, 明文阶段): 直接按明文帧解析(HELLO/PUBKEY 密钥交换)
    //   - 有会话密钥: 先解密, 解密失败时兜底尝试明文 HELLO(上位机探测未初始化设备)
    uint8_t pt[CMD_BUF_MAX_LEN];
    size_t ptLen = 0;
    const uint8_t* frame; size_t frameLen;
    if(secSessionEnabled()){
      if(!secDecrypt(buf, len, pt, sizeof(pt), &ptLen)){
        // 解密失败: 兜底尝试明文 HELLO（上位机在无本地密钥时以明文探测设备状态）
        uint8_t s2 = 0, c2 = 0, pl2 = 0;
        const uint8_t* p2 = nullptr;
        if(bpParseFrame(buf, len, &s2, &c2, &p2, &pl2) && c2 == BP_CMD_HELLO){
          lqNoteFrame(s2, true);
          if(g_debug_mode) Serial.printf("[SEC] plain HELLO (decrypt fallback) seq=%u\n", s2);
          handleHelloCmdPlain(s2);
          bleCommUnlock();
          continue;
        }
        // 真解密失败: 回复 DECRYPT FAIL 错误帧
        const uint8_t e[1] = { BP_ERR_DECRYPT_FAIL };
        sendReplyFrame(0, BP_RESP_ACK, e, 1);
        // 链路质量: 记一帧异常（链路干扰/帧损坏）
        lqNoteFrame(0, false);
        bleCommUnlock();
        continue;
      }
      // M4: 解密成功证明双方密钥一致，授权该连接
      ble_authorized = true;
      frame = pt; frameLen = ptLen;
    }else{
      // 无会话密钥：仅 UNINIT 状态允许明文帧（密钥交换阶段）
      // S8: WORK_WAIT 下 secSessionEnabled=false 属重连竞态窗口（onConnect 置
      // g_need_load_auth 后 loop 尚未执行 secLoadAuth）或 NVS 密钥损坏态，
      // 若走明文分支则任意邻近攻击者可绕过加密直接执行 SPD/SW/CFG/RESET。
      // 此窗口仅放行明文 HELLO（上位机探测状态），其余回 SEC_REQUIRED。
      if(g_dev_state != STATE_UNINIT){
        uint8_t s2 = 0, c2 = 0, pl2 = 0;
        const uint8_t* p2 = nullptr;
        if(bpParseFrame(buf, len, &s2, &c2, &p2, &pl2) && c2 == BP_CMD_HELLO){
          lqNoteFrame(s2, true);
          if(g_debug_mode) Serial.printf("[SEC] plain HELLO (key not loaded) seq=%u\n", s2);
          handleHelloCmdPlain(s2);
        }else{
          const uint8_t e[1] = { BP_ERR_SEC_REQUIRED };
          sendReplyFrame(0, BP_RESP_ACK, e, 1);
        }
        bleCommUnlock();
        continue;
      }
      // 明文阶段(密钥交换): 直接解析
      frame = buf; frameLen = len;
    }

    // 解析二进制帧 → 分发指令处理
    uint8_t seq = 0, cmdId = 0, plen = 0;
    const uint8_t* payload = nullptr;
    if(bpParseFrame(frame, frameLen, &seq, &cmdId, &payload, &plen)){
      // 链路质量: 帧解析成功, 按 seq 连续性检查重复/跳变
      lqNoteFrame(seq, true);
      // 调试模式: 打印解密后的完整二进制帧
      if(g_debug_mode){
        Serial.printf("[DBG] RX:");
        for(size_t i = 0; i < frameLen; i++) Serial.printf(" %02X", frame[i]);
        Serial.printf("\n");
      }
      // 诊断: 打印设备端实际收到的指令(DEBUG级)
      if(g_debug_mode) Serial.printf("[SEC] recv cmd=0x%02X seq=%u len=%u\n", cmdId, seq, plen);
      parseBinaryCommand(seq, cmdId, payload, plen);
    }else{
      // L14: 帧解析失败(CRC/帧结构损坏): 回错误帧，避免静默丢弃
      lqNoteFrame(0, false);
      const uint8_t e[1] = { BP_ERR_UNKNOWN_CMD };
      sendReplyFrame(0, BP_RESP_ACK, e, 1);
    }
    bleCommUnlock();
  }
}

void secTaskStart()
{
  if(rxSem != nullptr) return;
  rxSem = xSemaphoreCreateCounting(RX_QUEUE_DEPTH, 0);
  if(rxSem != nullptr){
    // 栈 24KB: mbedtls AES-GCM setkey 需约 4~5KB, 加调用链(buf/pt 128+128、
    // sendReplyFrame frame[246]、GETSR p[98]) 后 16KB 已足够,
    // 提升至 24KB 进一步留足余量, 避免长会话下栈溢出崩溃
    xTaskCreate(secTask, "secTask", 24576, nullptr, 2, nullptr);
    Serial.println("[SEC] aes task started (24KB stack)");
  }
}

void bleInit()
{
  // FS5-6: 通信互斥递归锁
  commMutex = xSemaphoreCreateRecursiveMutex();

  // FS.md 固件需求#1: 广播名 NR_F2S4 → NR_F2S4_{BLE MAC后6位大写}
  // 构造默认 MAC 名（g_ble_name 非空时优先使用 g_ble_name）
  {
    uint8_t mac[6] = {0};
    if(esp_read_mac(mac, ESP_MAC_BT) == ESP_OK){
      snprintf(s_defaultDevName, sizeof(s_defaultDevName), "NR_F2S4_%02X%02X%02X", mac[3], mac[4], mac[5]);
    }else{
      snprintf(s_defaultDevName, sizeof(s_defaultDevName), "NR_F2S4_000000");
    }
  }

  BLEDevice::init(effectiveDevName());
  pServer = BLEDevice::createServer();
  pServer->setCallbacks(new ServerCb());
  BLEService* pSvc = pServer->createService(SERVICE_UUID);

  // 业务特征收发分离(根除自回环竞态, 详见文件头 pCharWrite/pCharNotify 注释):
  //   - 指令特征 CHAR_UUID(仅 WRITE): 上位机写入, 挂 onWrite 回调入 RX 队列
  //   - 通知特征 CHAR_NOTIFY_UUID(仅 NOTIFY): 固件应答/事件推送, 挂 CCCD(BLE2902)
  // 远程写入只更新指令特征值, notify 只读通知特征值, 二者物理隔离, 竞态不可能发生
  pCharWrite = pSvc->createCharacteristic(
    CHAR_UUID,
    BLECharacteristic::PROPERTY_WRITE
  );
  pCharWrite->setCallbacks(new CharCb());

  pCharNotify = pSvc->createCharacteristic(
    CHAR_NOTIFY_UUID,
    BLECharacteristic::PROPERTY_NOTIFY
  );
  pCharNotify->addDescriptor(new BLE2902());

  pSvc->start();

  pAdv = BLEDevice::getAdvertising();
  pAdv->addServiceUUID(SERVICE_UUID);
  pAdv->start();
  adv_running = true;
  Serial.printf("[BLE] device name: %s\n", effectiveDevName());
}
