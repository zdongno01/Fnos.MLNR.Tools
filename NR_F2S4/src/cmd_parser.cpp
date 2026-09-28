#include "cmd_parser.h"
#include "hw_config.h"
#include "ble_service.h"
#include "nvs_store.h"
#include "fan_ctrl.h"
#include "switch_ctrl.h"
#include "security.h"
#include "app_state.h"
#include "sensor.h"
#include "binary_proto.h"
#include "link_quality.h"
#include "offline_log.h"
#include <Arduino.h>
#include <cstdio>
#include <cstring>
#include <mbedtls/platform_util.h>

// ===================== 小端读取辅助 =====================
static inline uint16_t rdU16(const uint8_t* p){ return (uint16_t)(p[0] | (p[1] << 8)); }
static inline uint32_t rdU32(const uint8_t* p){
  return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}
static inline void wrU16(uint8_t* p, uint16_t v){ p[0] = v & 0xFF; p[1] = (v >> 8) & 0xFF; }
static inline void wrU32(uint8_t* p, uint32_t v){
  p[0] = v & 0xFF; p[1] = (v >> 8) & 0xFF; p[2] = (v >> 16) & 0xFF; p[3] = (v >> 24) & 0xFF;
}
static inline void wrF32(uint8_t* p, float f){
  uint32_t u; memcpy(&u, &f, 4); wrU32(p, u);
}

// ===================== 应答辅助 =====================
static void sendOkAck(uint8_t seq){
  const uint8_t p[1] = { BP_ERR_OK };
  sendReplyFrame(seq, BP_RESP_ACK, p, 1);
}
static void sendErrAck(uint8_t seq, uint8_t err){
  const uint8_t p[1] = { err };
  sendReplyFrame(seq, BP_RESP_ACK, p, 1);
}

// ===================== 密钥交换分片状态 =====================
static uint8_t  kePubBuf[SEC_PUBKEY_BUF_LEN];  // RSA 公钥 DER 分片拼接缓冲
static uint16_t keGot        = 0;              // 已拼接字节数
static uint8_t  keTotal      = 0;              // 期望总字节数
static uint8_t  kePieceCount = 0;              // 已收分片数（片序号必须连续）

void keReset()
{
  keGot        = 0;
  keTotal      = 0;
  kePieceCount = 0;
}

// 明文 HELLO 应答: ACK[0][State]，强制明文发送
void handleHelloCmdPlain(uint8_t seq)
{
  const uint8_t p[2] = { BP_ERR_OK, (uint8_t)((g_dev_state == STATE_UNINIT) ? 0 : 1) };
  sendReplyFramePlain(seq, BP_RESP_ACK, p, sizeof(p));
}

// ===================== 配置项应用（按 CfgKeyId） =====================
// 返回错误码（BP_ERR_OK=0 表示成功）

// 仅校验配置项合法性(不修改任何状态)。校验规则与 applyFanCfgKey 保持一致,
// 供广播 CFG(目标=0)两轮事务的第一轮使用——保证全部目标通道校验通过后才应用,
// 避免"部分通道已应用、但回复错误"的部分提交不一致
static uint8_t validateFanCfgKey(uint8_t idx, uint8_t keyId, long val)
{
  (void)idx;
  switch(keyId){
    case BP_KEY_FN_ENABLED:
      return (val != 0 && val != 1) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    case BP_KEY_FN_POWER_SPD:
      return (val < 0 || val > 100) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    case BP_KEY_FN_HB_FALLBACK:
      return (val < 0 || val > 100) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    case BP_KEY_FN_PWM_FREQ:
      return (val < 10000 || val > 30000) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    default:
      return BP_ERR_CFG_KEY_UNKNOWN;
  }
}

static uint8_t applyFanCfgKey(uint8_t idx, uint8_t keyId, long val)
{
  FanConfig& c = g_fan_cfg[idx];
  switch(keyId){
    case BP_KEY_FN_ENABLED:
      if(val != 0 && val != 1) return BP_ERR_VALUE_OUTOFRANGE;
      c.enabled = (uint8_t)val;
      if(c.enabled){
        // 运行时启用: 按上电默认转速启动风扇(与 applyRuntimeFromConfig 开机语义一致:
        // target = enabled ? power_spd : 0)。启用前 SPD 会被 FAN_DISABLED 拒绝,
        // last_received_spd 必为 0, 故 target 直接取 power_spd 即可启动。
        target_spd[idx] = c.power_spd;
      }else{
        target_spd[idx]        = 0;
        current_spd[idx]       = 0;
        last_received_spd[idx] = 0;
        setFanOutput(idx, 0);
      }
      return BP_ERR_OK;
    case BP_KEY_FN_POWER_SPD:
      if(val < 0 || val > 100) return BP_ERR_VALUE_OUTOFRANGE;
      c.power_spd = (uint8_t)val;
      return BP_ERR_OK;
    case BP_KEY_FN_HB_FALLBACK:
      if(val < 0 || val > 100) return BP_ERR_VALUE_OUTOFRANGE;
      c.hb_fallback_spd = (uint8_t)val;
      return BP_ERR_OK;
    case BP_KEY_FN_PWM_FREQ:
      if(val < 10000 || val > 30000) return BP_ERR_VALUE_OUTOFRANGE;
      c.pwm_freq = (uint32_t)val;
      // L24: 回填 ledcSetup 实际配置频率(可能因分频舍入与请求值不同)
      {
        uint32_t actual = updatePwmFreq(idx, c.pwm_freq);
        if(actual > 0) c.pwm_freq = actual;
      }
      return BP_ERR_OK;
    default:
      return BP_ERR_CFG_KEY_UNKNOWN;
  }
}

// 仅校验配置项合法性(不修改任何状态); 与 applySwCfgKey 校验规则一致
static uint8_t validateSwCfgKey(uint8_t idx, uint8_t keyId, long val)
{
  switch(keyId){
    case BP_KEY_SW_ENABLED:
      if(val != 0 && val != 1) return BP_ERR_VALUE_OUTOFRANGE;
      // 与 applySwCfgKey 一致: 当前处于开启状态的开关不允许直接禁用
      if(val == 0 && sw_state[idx]) return BP_ERR_SW_MUST_TURN_OFF;
      return BP_ERR_OK;
    case BP_KEY_SW_POWER_ON_STATE:
      return (val != 0 && val != 1) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    case BP_KEY_SW_POWER_ON_DELAY:
      return (val < 0 || (uint32_t)val > SW_DELAY_MAX_MS) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    case BP_KEY_SW_SYSTEM:
      return (val != 0 && val != 1) ? BP_ERR_VALUE_OUTOFRANGE : BP_ERR_OK;
    default:
      return BP_ERR_CFG_KEY_UNKNOWN;
  }
}

static uint8_t applySwCfgKey(uint8_t idx, uint8_t keyId, long val)
{
  SwitchConfig& c = g_sw_cfg[idx];
  switch(keyId){
    case BP_KEY_SW_ENABLED:
      if(val != 0 && val != 1) return BP_ERR_VALUE_OUTOFRANGE;
      if(val == 0 && sw_state[idx]) return BP_ERR_SW_MUST_TURN_OFF;
      c.enabled = (uint8_t)val;
      if(!c.enabled) switchBootCancel(idx);
      return BP_ERR_OK;
    case BP_KEY_SW_POWER_ON_STATE:
      if(val != 0 && val != 1) return BP_ERR_VALUE_OUTOFRANGE;
      c.power_on_state = (uint8_t)val;
      return BP_ERR_OK;
    case BP_KEY_SW_POWER_ON_DELAY:
      if(val < 0 || (uint32_t)val > SW_DELAY_MAX_MS) return BP_ERR_VALUE_OUTOFRANGE;
      c.power_on_delay = (uint32_t)val;
      return BP_ERR_OK;
    case BP_KEY_SW_SYSTEM:
      if(val != 0 && val != 1) return BP_ERR_VALUE_OUTOFRANGE;
      c.system = (uint8_t)val;
      return BP_ERR_OK;
    default:
      return BP_ERR_CFG_KEY_UNKNOWN;
  }
}

static uint8_t applySensorCfgKey(uint8_t chIdx, uint8_t keyId, long val)
{
  if(chIdx >= SENSOR_CH_MAX) return BP_ERR_SENSOR_CH_RANGE;
  SensorConfig& c = g_sensor_cfg[chIdx];
  switch(keyId){
    case BP_KEY_SENSOR_KIND:
      if(val < 0 || val > (long)SENSOR_HTU21D) return BP_ERR_VALUE_OUTOFRANGE;
      c.type = (uint8_t)val;
      // L18: 类型变更后旧驱动初始化状态失效，标记需重初始化
      if(g_sensor_ok[chIdx] && g_sensor_init_type[chIdx] != c.type){
        g_sensor_ok[chIdx] = false;
      }
      return BP_ERR_OK;
    case BP_KEY_SENSOR_ADDR:
      if(val < 0x08 || val > 0x77) return BP_ERR_VALUE_OUTOFRANGE;
      c.addr = (uint8_t)val;
      // L18: 地址变更同理置失效
      if(g_sensor_ok[chIdx] && g_sensor_init_addr[chIdx] != c.addr){
        g_sensor_ok[chIdx] = false;
      }
      return BP_ERR_OK;
    case BP_KEY_SENSOR_ENABLED:
      if(val != 0 && val != 1) return BP_ERR_VALUE_OUTOFRANGE;
      if(val == 1){
        if(c.type == SENSOR_NONE) return BP_ERR_SENSOR_TYPE_NONE;
        // 只重初始化目标通道, 避免 sensorInit() 重开 I2C 总线干扰其他正常工作的通道。
        // L18: 除 g_sensor_ok=false 外，(type,addr) 与最近一次成功初始化参数不一致也强制 reinit，
        //      修复"旧类型(如AHT20)已OK后热切换为BMP280"时校准表未加载导致读数异常/通道静默停采
        bool params_changed = (g_sensor_init_type[chIdx] != c.type ||
                               g_sensor_init_addr[chIdx] != c.addr);
        if(!g_sensor_ok[chIdx] || params_changed){
          g_sensor_ok[chIdx] = sensorReinitChannel(chIdx);
          if(!g_sensor_ok[chIdx]) return BP_ERR_SENSOR_NOT_READY;
        }
      }
      c.enabled = (uint8_t)val;
      return BP_ERR_OK;
    case BP_KEY_SENSOR_INTERVAL:
      if(val < 1 || val > 3600) return BP_ERR_VALUE_OUTOFRANGE;
      c.interval_sec = (uint16_t)val;
      return BP_ERR_OK;
    default:
      return BP_ERR_CFG_KEY_UNKNOWN;
  }
}

// ===================== 指令解析核心（二进制帧） =====================
void parseBinaryCommand(uint8_t seq, uint8_t cmdId, const uint8_t* pl, uint8_t plen)
{
  uint32_t now = millis();
  last_cmd_recv_ms = now;
  bool prev_timeout = heartbeat_timeout_flag;
  heartbeat_timeout_flag = false;
  if(prev_timeout) stopAdvertisingSafe();

  // 未初始化(UNINIT)时仅放行 探测/版本协商/密钥交换 指令
  // （HELLO/KE_PUBKEY 为明文阶段密钥交换命令；KE_CONFIRM 为加密确认，此时内存密钥已装载）
  if(g_dev_state == STATE_UNINIT){
    // S2: BP_CMD_INIT 已删除（RSA 方案下密钥交换已取代明文 INIT）
    // S7: 白名单收紧至密钥交换所需最小集（PING/GETV/HELLO/KE_*）。
    //     GETF/GETS/GETSR/GETG 在密钥建立前无功能用途，且明文暴露硬件配置、
    //     开关状态与传感器数据（邻近攻击者可读取），移出白名单。
    bool allowed = (cmdId == BP_CMD_PING) ||
                   (cmdId == BP_CMD_GETV)  || (cmdId == BP_CMD_HELLO) ||
                   (cmdId == BP_CMD_KE_PUBKEY) || (cmdId == BP_CMD_KE_CONFIRM);
    if(!allowed){
      sendErrAck(seq, BP_ERR_SEC_REQUIRED);
      return;
    }
  }

  switch(cmdId){
    // S2: BP_CMD_INIT(0x01) 已删除——RSA 密钥交换方案下 INIT 无存在必要，
    //     明文 INIT 可绕过密钥交换直接进入 WORK_WAIT，构成后门。删除后 BP_CMD_INIT
    //     命中 default 分支返回 BP_ERR_UNKNOWN_CMD。

    // ---- SPD 0x02：风扇调速 [FanId(1)][Spd(1)] ----
    case BP_CMD_SPD:{
      if(plen < 2){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      uint8_t fanSel = pl[0];
      uint8_t spd    = pl[1];
      if(fanSel > FAN_COUNT){ sendErrAck(seq, BP_ERR_FAN_ID_RANGE); return; }
      if(spd > 100){ sendErrAck(seq, BP_ERR_FAN_SPD_RANGE); return; }
      // L4: 两轮处理——先校验全部目标通道可用, 再应用。
      //     广播(FanId=0)时若任一通道 disabled 则整体失败且不应用任何修改,
      //     避免"部分通道已改、但回复错误"的状态与应答不一致
      for(uint8_t i = 0; i < FAN_COUNT; i++){
        if(fanSel != 0 && fanSel != (uint8_t)(i + 1)) continue;
        if(!g_fan_cfg[i].enabled){ sendErrAck(seq, BP_ERR_FAN_DISABLED); return; }
      }
      for(uint8_t i = 0; i < FAN_COUNT; i++){
        if(fanSel != 0 && fanSel != (uint8_t)(i + 1)) continue;
        if(spd != last_received_spd[i] || (now - last_spd_cmd_ms[i]) >= CMD_THROTTLE_MS){
          last_received_spd[i] = spd;
          last_spd_cmd_ms[i]   = now;
          target_spd[i]        = spd;
        }
      }
      sendOkAck(seq);
      return;
    }

    // ---- SW 0x03：开关控制 [SwId(1)][State(1)] ----
    case BP_CMD_SW:{
      if(plen < 2){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      uint8_t swSel = pl[0];
      uint8_t st    = pl[1];
      if(swSel > SW_COUNT){ sendErrAck(seq, BP_ERR_SW_ID_RANGE); return; }
      if(st != 0 && st != 1){ sendErrAck(seq, BP_ERR_SW_STATE_INVALID); return; }
      // L4: 两轮处理——先校验全部目标通道(enabled/system), 再应用
      for(uint8_t i = 0; i < SW_COUNT; i++){
        if(swSel != 0 && swSel != (uint8_t)(i + 1)) continue;
        if(!g_sw_cfg[i].enabled){ sendErrAck(seq, BP_ERR_SW_DISABLED); return; }
        // 系统开关禁止被二进制指令关闭（物理按键不受此限制）
        if(st == 0 && g_sw_cfg[i].system == 1){
          sendErrAck(seq, BP_ERR_SW_SYSTEM_LOCKED);
          return;
        }
      }
      for(uint8_t i = 0; i < SW_COUNT; i++){
        if(swSel != 0 && swSel != (uint8_t)(i + 1)) continue;
        setSwitchOutput(i, st == 1, SW_COND_CMD);
      }
      sendOkAck(seq);
      return;
    }

    // ---- GET_OFFLINE 0x10：读取离线事件（加密） ----
    // 应答 [ErrCode][固件当前ms u32 LE][条数 u8][条目×N]；条目=7B [time_ms u32 LE][swId][state][cond]；
    // 发送后固件清空缓冲（上位机以固件当前ms 与事件ms 反推真实发生时间）
    case BP_CMD_GET_OFFLINE:{
      // M1: 限制单次返回最多 24 条，防止加密帧超 MTU 被截断
      //   加密帧 = 6 + payload + 12(IV) + 16(TAG) = payload + 34
      //   MTU 有效载荷上限约 244，payload ≤ 210
      //   payload = 1(err) + 4(ms) + 1(count) + 7*N = 6 + 7N → 7N ≤ 204 → N ≤ 29，保守取 24
      const uint8_t MAX_OFFLINE_PER_RSP = 24;
      uint8_t n = offlineLogCount();
      uint8_t send_n = (n > MAX_OFFLINE_PER_RSP) ? MAX_OFFLINE_PER_RSP : n;
      uint8_t p[1 + 4 + 1 + MAX_OFFLINE_PER_RSP * 7];
      p[0] = BP_ERR_OK;
      wrU32(p + 1, millis());   // 固件当前时间(毫秒)
      p[5] = send_n;
      uint16_t off = 6;
      uint8_t got = 0;
      if(send_n > 0){
        uint8_t buf[MAX_OFFLINE_PER_RSP * 7];
        // L3: drain 不清空缓冲，发送成功后再 commit
        got = offlineLogDrain(buf, sizeof(buf), send_n);
        memcpy(p + 6, buf, (size_t)got * 7);
        off += (uint16_t)got * 7;
      }
      sendReplyFrame(seq, BP_RESP_ACK, p, off);
      // L3: 发送成功后才清空已读取的条目（连接断开则数据保留供下次读取）
      if(got > 0 && ble_connected) offlineLogCommit(got);
      return;
    }

    // ---- CFG 0x04：配置 [TargetType(1)][TargetId(1)][CfgKeyId(1)][Value(N)] ----
    case BP_CMD_CFG:{
      if(plen < 4){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      uint8_t tt   = pl[0];
      uint8_t tId  = pl[1];
      uint8_t key  = pl[2];
      const uint8_t* vp = pl + 3;
      uint8_t vlen = plen - 3;

      // 按 key 决定值长度
      uint8_t need = 0;
      switch(tt){
        case BP_CFG_TARGET_FN:
          need = (key == BP_KEY_FN_PWM_FREQ) ? 4 : 1;
          break;
        case BP_CFG_TARGET_SW:
          need = (key == BP_KEY_SW_POWER_ON_DELAY) ? 4 : 1;
          break;
        case BP_CFG_TARGET_SENSOR:
          need = (key == BP_KEY_SENSOR_INTERVAL) ? 2 : 1;
          break;
        case BP_CFG_TARGET_GLOBAL:
          // GLOBAL 下按 key 决定值长度: HB_TIMEOUT=u16(2), DEBUG_MODE=u8(1),
          // BLE_NAME=字符串(特殊处理, need=1 仅作最小长度校验)
          if(key == BP_KEY_GLOBAL_HB_TIMEOUT) need = 2;
          else if(key == BP_KEY_GLOBAL_DEBUG_MODE) need = 1;
          else need = 1; // BLE_NAME 及未知 key: 最小1字节, 具体在分支中校验
          break;
        default:
          sendErrAck(seq, BP_ERR_CFG_TARGET_UNKNOWN);
          return;
      }
      if(vlen < need){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      long val = 0;
      if(need == 1)      val = (long)vp[0];
      else if(need == 2) val = (long)rdU16(vp);
      else               val = (long)rdU32(vp);

      if(tt == BP_CFG_TARGET_GLOBAL){
        // 全局配置
        if(key == BP_KEY_GLOBAL_HB_TIMEOUT){
          if(val < 3 || val > 60){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
          g_hb_timeout_sec = (uint16_t)val;
          sendOkAck(seq);
          return;
        }
        if(key == BP_KEY_GLOBAL_DEBUG_MODE){
          if(val != 0 && val != 1){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
          g_debug_mode = (uint8_t)val;  // 立即生效，SAVE 时持久化
          sendOkAck(seq);
          return;
        }
        if(key == BP_KEY_GLOBAL_BLE_NAME){
          // BLE_NAME 是唯一的字符串值: vlen 字节，不经过数字 val 解析
          // 校验: 长度 1~20 且全部为可打印 ASCII (0x20~0x7E)
          if(vlen < 1 || vlen > 20){ sendErrAck(seq, BP_ERR_BLE_NAME_INVALID); return; }
          for(uint8_t i = 0; i < vlen; i++){
            if(vp[i] < 0x20 || vp[i] > 0x7E){
              sendErrAck(seq, BP_ERR_BLE_NAME_INVALID);
              return;
            }
          }
          memcpy(g_ble_name, vp, vlen);
          g_ble_name[vlen] = '\0';
          // L12: 统一持久化策略——仅改内存, 由 SAVE 指令统一落盘(与 HB_TIMEOUT/
          //      DEBUG_MODE 一致), 不再在此处硬编码 NVS 命名空间立即写盘。
          //      广播名立即生效(不落盘也不丢失内存态)
          bleUpdateDeviceName();
          sendOkAck(seq);
          return;
        }
        sendErrAck(seq, BP_ERR_CFG_KEY_UNKNOWN);
        return;
      }

      if(tt == BP_CFG_TARGET_SENSOR){
        if(tId >= SENSOR_CH_MAX){ sendErrAck(seq, BP_ERR_SENSOR_CH_RANGE); return; }
        uint8_t e = applySensorCfgKey(tId, key, val);
        if(e != BP_ERR_OK){ sendErrAck(seq, e); return; }
        sendOkAck(seq);
        return;
      }

      if(tt == BP_CFG_TARGET_FN){
        if(tId > FAN_COUNT){ sendErrAck(seq, BP_ERR_FAN_ID_RANGE); return; }
        // L4: 两轮处理——先校验全部目标通道的 key 合法性, 再应用
        for(uint8_t i = 0; i < FAN_COUNT; i++){
          if(tId != 0 && tId != (uint8_t)(i + 1)) continue;
          uint8_t e = validateFanCfgKey(i, key, val);
          if(e != BP_ERR_OK){ sendErrAck(seq, e); return; }
        }
        for(uint8_t i = 0; i < FAN_COUNT; i++){
          if(tId != 0 && tId != (uint8_t)(i + 1)) continue;
          applyFanCfgKey(i, key, val);
        }
        sendOkAck(seq);
        return;
      }

      if(tt == BP_CFG_TARGET_SW){
        if(tId > SW_COUNT){ sendErrAck(seq, BP_ERR_SW_ID_RANGE); return; }
        // L4: 两轮处理——先校验全部目标通道的 key 合法性, 再应用
        for(uint8_t i = 0; i < SW_COUNT; i++){
          if(tId != 0 && tId != (uint8_t)(i + 1)) continue;
          uint8_t e = validateSwCfgKey(i, key, val);
          if(e != BP_ERR_OK){ sendErrAck(seq, e); return; }
        }
        for(uint8_t i = 0; i < SW_COUNT; i++){
          if(tId != 0 && tId != (uint8_t)(i + 1)) continue;
          applySwCfgKey(i, key, val);
        }
        sendOkAck(seq);
        return;
      }

      sendErrAck(seq, BP_ERR_CFG_TARGET_UNKNOWN);
      return;
    }

    // ---- GETV 0x05：版本 ----
    case BP_CMD_GETV:{
      uint8_t p[BP_MAX_PAYLOAD];
      p[0] = BP_ERR_OK;
      uint8_t vlen = (uint8_t)strlen(VERSION);
      if(1 + vlen > sizeof(p)) vlen = sizeof(p) - 1;
      memcpy(p + 1, VERSION, vlen);
      sendReplyFrame(seq, BP_RESP_ACK, p, 1 + vlen);
      return;
    }

    // ---- GETF 0x06：风扇查询 [FanId(1)] — 1-based, 与 SPD/CFG/GETS 保持一致 ----
    case BP_CMD_GETF:{
      if(plen < 1){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      uint8_t n = pl[0];
      if(n < 1 || n > FAN_COUNT){ sendErrAck(seq, BP_ERR_FAN_ID_RANGE); return; }
      uint8_t i = (uint8_t)(n - 1);
      uint8_t p[16];
      p[0] = BP_ERR_OK;
      p[1] = g_fan_cfg[i].enabled;
      wrU32(p + 2, g_fan_cfg[i].pwm_freq);
      p[6] = g_fan_cfg[i].power_spd;
      p[7] = g_fan_cfg[i].hb_fallback_spd;
      p[8] = target_spd[i];
      p[9] = current_spd[i];
      sendReplyFrame(seq, BP_RESP_ACK, p, 10);
      return;
    }

    // ---- GETS 0x07：开关查询 [SwId(1)] ----
    // 应答 9 字节: [Err][Enabled][PowerOnState][PowerOnDelay u32][CurrentState][System]
    case BP_CMD_GETS:{
      if(plen < 1){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      uint8_t n = pl[0];
      if(n < 1 || n > SW_COUNT){ sendErrAck(seq, BP_ERR_SW_ID_RANGE); return; }
      uint8_t i = (uint8_t)(n - 1);
      uint8_t p[12];
      p[0] = BP_ERR_OK;
      p[1] = g_sw_cfg[i].enabled;
      p[2] = g_sw_cfg[i].power_on_state;
      wrU32(p + 3, g_sw_cfg[i].power_on_delay);
      p[7] = sw_state[i] ? 1 : 0;
      p[8] = g_sw_cfg[i].system;
      sendReplyFrame(seq, BP_RESP_ACK, p, 9);
      return;
    }

    // ---- GETSR 0x08：传感器全量查询 ----
    case BP_CMD_GETSR:{
      uint8_t p[BP_GETSR_PAYLOAD];
      memset(p, 0, sizeof(p));
      p[0] = BP_ERR_OK;
      bool any_enabled = false;
      for(uint8_t i = 0; i < SENSOR_CH_MAX; i++){
        if(g_sensor_cfg[i].enabled){ any_enabled = true; break; }
      }
      p[1] = any_enabled ? 1 : 0;
      for(uint8_t i = 0; i < SENSOR_CH_MAX; i++){
        uint8_t* b = p + 2 + (size_t)i * BP_GETSR_BLOCK_LEN;
        const SensorConfig&  c = g_sensor_cfg[i];
        // L8: 原子快照读取, 避免读到"温度已更新、湿度未更新"的撕裂数据
        const SensorData     d = sensorGetChannelSnapshot(i);
        b[0] = c.type;
        b[1] = c.addr;
        b[2] = c.enabled;
        wrU16(b + 3, c.interval_sec);
        b[5] = g_sensor_ok[i] ? 1 : 0;   // State: 1=OK 0=FAIL
        wrF32(b + 6,  d.temperature);
        wrF32(b + 10, d.humidity);
        wrF32(b + 14, d.pressure);
        wrF32(b + 18, d.altitude);
        // b[22..23] 保留字节，保持 0
      }
      sendReplyFrame(seq, BP_RESP_ACK, p, sizeof(p));
      return;
    }

    // ---- PING 0x09：心跳 ----
    // 应答负载: [ErrorCode(1)][SignalQuality(1, 0~100)]
    // 信号质量由链路质量模块按接收帧异常率(重发率近似)推算；
    // 旧版上位机只读 ErrorCode 首字节，追加负载字节向后兼容。
    case BP_CMD_PING:{
      const uint8_t p[2] = { BP_ERR_OK, lqGetQuality() };
      sendReplyFrame(seq, BP_RESP_ACK, p, sizeof(p));
      return;
    }

    // ---- SAVE 0x0A：配置落盘 ----
    case BP_CMD_SAVE:
      if(saveConfigToNVS()) sendOkAck(seq);
      else                  sendErrAck(seq, BP_ERR_SAVE_FAILED);
      return;

    // ---- RESET 0x0B：恢复出厂（完整重置：清配置 + 清授权 + 切 UNINIT）----
    case BP_CMD_RESET:{
      // M5: 先回 ACK（此时仍有密钥，回加密 ACK），再清配置/授权/状态
      sendOkAck(seq);
      // 1.清业务配置(fan/sw/sensor/global) + 恢复默认硬件运行状态
      factoryResetNVS();
      // 2.清初始化标志 + 切 UNINIT(等价于 appStateForceReset 的后半段, 不动开关)
      secClearAuth();
      appStateSet(STATE_UNINIT);
      return;
    }

    // ---- GETG 0x0C：查询全局配置 ----
    // 应答载荷: [Err(1)][HbTimeout u16(2)][DebugMode u8(1)][BleNameLen u8(1)][BleName(N)]
    // 总长 5+N（N≤20），BleName 为空时 BleNameLen=0 不跟名字节
    case BP_CMD_GETG:{
      uint8_t p[32]; // 5 + 20 = 25 最大，留余量
      p[0] = BP_ERR_OK;
      wrU16(p + 1, g_hb_timeout_sec);
      p[3] = g_debug_mode;
      uint8_t nameLen = (uint8_t)strlen(g_ble_name);
      if(nameLen > 20) nameLen = 20;
      p[4] = nameLen;
      if(nameLen > 0){
        memcpy(p + 5, g_ble_name, nameLen);
      }
      sendReplyFrame(seq, BP_RESP_ACK, p, (uint8_t)(5 + nameLen));
      return;
    }

    // ---- HELLO 0x0D：明文探测（上位机连接后先以明文探测设备状态） ----
    // 明文阶段直接进入；已初始化设备加密解密失败时由 secTask 兜底调用 handleHelloCmdPlain
    case BP_CMD_HELLO:
      handleHelloCmdPlain(seq);
      return;

    // ---- KE_PUBKEY 0x0E：明文 RSA 公钥分片（密钥交换） ----
    // 载荷 [ChunkIdx][ChunkCount][ChunkData]；全部拼完后解析公钥、生成对称密钥、
    // OAEP-SHA256 加密，密文(256B)分 2 片经 EVT_KE_CT 明文事件回传，末片回明文 ACK[0]
    case BP_CMD_KE_PUBKEY:{
      if(plen < 3){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      // L13: UNINIT 状态下，若收到首片(idx=0)且已有未完成的密钥交换(keGot>0)
      // 或已装载会话密钥(上次 KE_PUBKEY 成功但 KE_CONFIRM 未到)，允许重新开始
      if(g_dev_state == STATE_UNINIT && pl[0] == 0 && (keGot > 0 || secHasSessionKey())){
        keReset();
        secClearSessionKeyMemory();
      }
      uint8_t idx   = pl[0];
      uint8_t total = pl[1];
      const uint8_t* chunk = pl + 2;
      uint8_t clen = plen - 2;
      if(total == 0 || idx >= total || clen == 0){
        sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return;
      }
      if(idx == 0){ keGot = 0; keTotal = total; kePieceCount = 0; }
      // 分片连续性校验：idx 必须等于已收片数
      if(keTotal != total || idx != kePieceCount || (uint16_t)keGot + clen > SEC_PUBKEY_BUF_LEN){
        keReset();
        sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE);
        return;
      }
      memcpy(kePubBuf + keGot, chunk, clen);
      keGot += clen;
      kePieceCount++;
      if(kePieceCount < keTotal){
        // 未拼完：明文 ACK（此时无会话密钥，固件仍处于明文模式）
        const uint8_t okP[1] = { BP_ERR_OK };
        sendReplyFramePlain(seq, BP_RESP_ACK, okP, sizeof(okP));
        return;
      }
      // 拼完：解析公钥 → 生成 16B 对称密钥 → OAEP 加密 → 密文经 EVT_KE_CT 分片回传
      uint8_t aesKey[SEC_KEY_LEN];
      esp_fill_random(aesKey, sizeof(aesKey));
      uint8_t cipher[SEC_RSA_CIPHER_LEN];
      size_t olen = 0;
      if(!secRsaPubEncrypt(kePubBuf, keGot, aesKey, sizeof(aesKey), cipher, sizeof(cipher), &olen)){
        keReset();
        sendErrAck(seq, BP_ERR_KE_PUBKEY_INVALID);
        return;
      }
      // 内存装载会话密钥（不落 NVS，等待 KE_CONFIRM 持久化）；清零临时密钥
      secSetSessionKey(aesKey);
      mbedtls_platform_zeroize(aesKey, sizeof(aesKey));
      keReset();
      // 密文 256B 分 2 片（每片 128B）经明文事件回传
      const uint8_t evtCount = 2;
      const uint8_t sliceLen = SEC_RSA_CIPHER_LEN / evtCount;
      for(uint8_t i = 0; i < evtCount; i++){
        uint8_t p[2 + sliceLen];
        p[0] = i;
        p[1] = evtCount;
        memcpy(p + 2, cipher + (size_t)i * sliceLen, sliceLen);
        sendReplyFramePlain(0, BP_EVT_KE_CT, p, sizeof(p));
      }
      mbedtls_platform_zeroize(cipher, sizeof(cipher));
      // 完成 ACK 必须明文：上位机此时尚未拿到对称密钥（HasKey=false），按明文解析
      {
        const uint8_t okP[1] = { BP_ERR_OK };
        sendReplyFramePlain(seq, BP_RESP_ACK, okP, sizeof(okP));
      }
      return;
    }

    // ---- KE_CONFIRM 0x0F：加密确认（对称密钥建立完成） ----
    // 载荷 [Nonce(16)]；固件解密成功（证明双方密钥一致）后持久化**内存中已有的会话密钥**
    // （KE_PUBKEY 阶段生成的 AES 密钥，非 nonce！），置 WORK_WAIT，回加密 ACK[0][Nonce] 回显
    case BP_CMD_KE_CONFIRM:{
      // M6: 仅 UNINIT 状态下允许密钥确认，防止 WORK_RUN 下误发导致状态倒退
      if(g_dev_state != STATE_UNINIT){ sendErrAck(seq, BP_ERR_STATE_NOT_UNINIT); return; }
      if(plen != SEC_KEY_LEN){ sendErrAck(seq, BP_ERR_VALUE_OUTOFRANGE); return; }
      // S1: 必须已装载内存会话密钥(KE_PUBKEY 阶段生成)，防止明文 KE_CONFIRM 持久化全零密钥
      if(!secHasSessionKey()){ sendErrAck(seq, BP_ERR_SEC_REQUIRED); return; }
      // 持久化内存会话密钥（传 nullptr），nonce 仅用于回显校验
      if(!secStoreKeyAndAuth(nullptr)){
        sendErrAck(seq, BP_ERR_SAVE_AUTH_FAIL); return;
      }
      // 密钥已持久化 = 设备已初始化（替代旧 INIT 的授权语义）
      appStateSet(STATE_WORK_WAIT);
      uint8_t p[1 + SEC_KEY_LEN];
      p[0] = BP_ERR_OK;
      memcpy(p + 1, pl, SEC_KEY_LEN);
      sendReplyFrame(seq, BP_RESP_ACK, p, sizeof(p));
      return;
    }

    default:
      sendErrAck(seq, BP_ERR_UNKNOWN_CMD);
      return;
  }
}
