#pragma once
#include <stdint.h>
#include <stddef.h>

// ===================== 安全模块（动态会话密钥 + RSA-2048-OAEP 密钥分发） =====================
// 会话密钥不再写死：UNINIT 设备首次连接时由上位机生成 RSA-2048 密钥对，
// 固件随机生成 16B AES 对称密钥、用上位机公钥 OAEP-SHA256 加密回传，
// 上位机私钥解密后双方以该对称密钥建立 AES-128-GCM 加密会话，并各自持久化。
// NVS 命名空间: sec_cfg → is_init(u8) 初始化标志 + sec_key(blob 16B) 会话密钥
#define SEC_NVS_NS "sec_cfg"
#define SEC_KEY_LEN 16U   // 与 AES_KEY_LEN 一致

// 密钥交换中 RSA-OAEP 密文固定长度（RSA-2048）
#define SEC_RSA_CIPHER_LEN 256U
// 公钥 DER（SubjectPublicKeyInfo）分片接收缓冲上限（2×135B）
// L12: 注意——上位机须发送 DER 格式(SubjectPublicKeyInfo/SPKI)的 RSA-2048 公钥，
// 而非 PKCS#1 RSAPublicKey 裸格式。需与上位机核对 DER 编码一致性。
#define SEC_PUBKEY_BUF_LEN 300U

// ---------- NVS 初始化标志与会话密钥 ----------
bool secIsInitialized();                 // NVS中是否已初始化(is_init=1)
void secLoadAuth();                      // 启动/连接时加载初始化标志与会话密钥到内存
bool secSaveAuth();                      // [兼容] 仅标记已初始化(不含密钥，密钥交换走 secStoreKeyAndAuth)
void secClearAuth();                     // 清除初始化标志与会话密钥(NVS+内存)，重置回 UNINIT
bool secStoreKeyAndAuth(const uint8_t key[SEC_KEY_LEN]); // 持久化会话密钥+标记已初始化(KE_CONFIRM)；
                                            // key 传 nullptr 表示持久化内存中已有的会话密钥

// ---------- 会话加密状态 ----------
bool secHasSessionKey();                 // 内存中是否已装载会话密钥(=当前会话是否加密)
void secSetSessionKey(const uint8_t key[SEC_KEY_LEN]);  // 装载内存密钥(不落NVS，密钥交换中间态)
void secClearSessionKeyMemory();         // 仅清内存密钥(断开/降级，不删NVS)
bool secSessionEnabled();                // 等价 secHasSessionKey()

// ---------- AES-128-GCM 会话加解密 ----------
// 帧格式: [IV(12)][密文(N)][TAG(16)]，密钥为当前会话密钥
bool secEncrypt(const uint8_t* in, size_t len, uint8_t* out, size_t outMax, size_t* outLen);
bool secDecrypt(const uint8_t* in, size_t len, uint8_t* out, size_t outMax, size_t* outLen);

// ---------- RSA-2048-OAEP 密钥交换 ----------
// 解析上位机公钥 DER（SubjectPublicKeyInfo），随机生成 16B 对称密钥，
// 用公钥 OAEP-SHA256 加密输出密文（固定 256B）。成功后密钥保留在内存(secSetSessionKey 由调用方执行)。
// 返回 true 且 *outLen=SEC_RSA_CIPHER_LEN 表示成功。
bool secRsaPubEncrypt(const uint8_t* pubDer, size_t pubLen,
                      const uint8_t* in, size_t inLen,
                      uint8_t* out, size_t outMax, size_t* outLen);
