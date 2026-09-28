#pragma once
#include <BLEDevice.h>
#include <BLEServer.h>
#include <BLEUtils.h>
#include <BLE2902.h>
#include <stdint.h>
#include "hw_config.h"
#include "sensor.h"

// BLE连接/心跳运行时状态(定义于ble_service.cpp)
// L1: 跨线程读写的状态变量加 volatile，防止编译器优化导致可见性问题
extern volatile bool     ble_connected;
extern volatile bool     ble_authorized;      // 当前连接是否为授权设备(或UNINIT下允许的接入)
extern volatile bool     heartbeat_timeout_flag;
extern bool              adv_running;        // 广播运行状态标记
extern volatile uint32_t last_cmd_recv_ms;   // 最近一次收到指令的时间

// M3: onConnect 中不在 btc 回调(3KB栈)里执行 NVS 读取，改为置标志由 loop 异步处理
extern volatile bool g_need_load_auth;

// BLE服务与广播初始化(含回调注册)
void bleInit();

// FS5-6: BLE通信互斥(递归锁): 加密缓冲/notify通道由BLE回调线程与loop线程
// 并发使用, 跨线程发送前须加锁
bool bleCommLock();
void bleCommUnlock();

// 发送二进制应答/事件帧(FS.md: [Magic][Seq][CmdID][Len][Payload][CRC16])
// 会话加密开启时整帧做 AES-GCM 加密后单条 notify 发送（协议不分片）
void sendReplyFrame(uint8_t seq, uint8_t cmdId, const uint8_t* payload, uint8_t payloadLen);

// 强制明文发送应答帧（密钥交换的 HELLO 探测应答必须明文，即使会话加密已开启）
void sendReplyFramePlain(uint8_t seq, uint8_t cmdId, const uint8_t* payload, uint8_t payloadLen);

// 事件帧便捷封装（SeqID=0）
void bleSendButtonEvent(uint8_t swId, uint8_t state, uint8_t cond);  // EVT_BTN 0x81 [SwId][State][Cond]
void bleSendSensorChannelData(uint8_t chIdx);           // EVT_SENSOR 0x82
void bleSendWarnEvent(uint8_t warnCode);                // EVT_WARN 0x83

// 广播安全启停
void stopAdvertisingSafe();
void startAdvertisingSafe();

// 强制断开当前连接(心跳超时/强制重置用)
void bleForceDisconnect();

// 运行时更新 BLE 广播名（g_ble_name 非空则用之，否则用默认 MAC 名）。
// 未连接且正在广播时立即生效（停广播→改设备名→重启广播）；
// 已连接时仅更新内存，下次断开重启广播时生效。
void bleUpdateDeviceName();

// 启动加解密后台任务 secTask(8KB栈): btc回调(3KB栈)严禁执行AES-GCM加解密
// (mbedtls AES setkey 需约4KB栈 → 3KB必溢出崩溃), 故 onWrite 仅挂起接收数据,
// 由 secTask 在安全栈上完成解密→帧解析→指令处理→加密回复
void secTaskStart();
