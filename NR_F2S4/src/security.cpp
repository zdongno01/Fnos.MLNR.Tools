#include "security.h"
#include "hw_config.h"
#include <Arduino.h>
#include <nvs.h>
#include <string.h>
#include <esp_system.h>
#include <esp_random.h>

#include <mbedtls/gcm.h>
#include <mbedtls/pk.h>
#include <mbedtls/rsa.h>
#include <mbedtls/md.h>
#include <mbedtls/platform_util.h>

// ===================== 状态(内存映像) =====================
static bool s_is_init = false;                          // 是否已完成初始化(NVS is_init)
static uint8_t s_session_key[SEC_KEY_LEN];              // 当前会话密钥(内存)
static bool    s_session_key_loaded = false;            // 内存中是否已装载会话密钥

// ===================== NVS 初始化标志与会话密钥读写 =====================
bool secIsInitialized() { return s_is_init; }

// 随机源回调(mbedtls RSA OAEP 加密需要)
static int secRng(void*, unsigned char* buf, size_t len)
{
  esp_fill_random(buf, len);
  return 0;
}

void secLoadAuth()
{
  s_is_init = false;
  s_session_key_loaded = false;
  nvs_handle_t hnd;
  if(nvs_open(SEC_NVS_NS, NVS_READONLY, &hnd) == ESP_OK){
    uint8_t flag = 0;
    if(nvs_get_u8(hnd, "is_init", &flag) == ESP_OK) s_is_init = (flag == 1);
    size_t klen = SEC_KEY_LEN;
    if(nvs_get_blob(hnd, "sec_key", s_session_key, &klen) == ESP_OK && klen == SEC_KEY_LEN){
      s_session_key_loaded = true;
    }
    nvs_close(hnd);
  }
  // M8: 一致性检查——is_init=1 但无 sec_key(NVS 损坏/边缘状态)时视为未初始化，
  // 防止明文全开放攻击
  if(s_is_init && !s_session_key_loaded){
    s_is_init = false;
  }
}

bool secSaveAuth()
{
  nvs_handle_t hnd;
  if(nvs_open(SEC_NVS_NS, NVS_READWRITE, &hnd) != ESP_OK) return false;
  esp_err_t e1 = nvs_set_u8(hnd, "is_init", 1);
  esp_err_t e2 = nvs_commit(hnd);
  nvs_close(hnd);
  if(e1 != ESP_OK || e2 != ESP_OK) return false;
  s_is_init = true;
  return true;
}

// 密钥交换确认(KE_CONFIRM)成功后调用：持久化会话密钥 + 标记已初始化
// key 传 nullptr 时持久化内存中已有的会话密钥（KE_PUBKEY 阶段生成的 AES 密钥）
bool secStoreKeyAndAuth(const uint8_t key[SEC_KEY_LEN])
{
  const uint8_t* k = (key != nullptr) ? key : s_session_key;
  nvs_handle_t hnd;
  if(nvs_open(SEC_NVS_NS, NVS_READWRITE, &hnd) != ESP_OK) return false;
  esp_err_t e1 = nvs_set_blob(hnd, "sec_key", k, SEC_KEY_LEN);
  esp_err_t e2 = nvs_set_u8(hnd, "is_init", 1);
  esp_err_t e3 = nvs_commit(hnd);
  nvs_close(hnd);
  if(e1 != ESP_OK || e2 != ESP_OK || e3 != ESP_OK) return false;
  memcpy(s_session_key, k, SEC_KEY_LEN);
  s_session_key_loaded = true;
  s_is_init = true;
  return true;
}

void secClearAuth()
{
  nvs_handle_t hnd;
  if(nvs_open(SEC_NVS_NS, NVS_READWRITE, &hnd) == ESP_OK){
    esp_err_t e1 = nvs_erase_key(hnd, "is_init");
    esp_err_t e2 = nvs_erase_key(hnd, "sec_key");
    esp_err_t e3 = nvs_commit(hnd);
    nvs_close(hnd);
    // L10: 检查擦除/提交结果, 失败时记录——否则重启后 NVS 仍含旧密钥,
    //      设备保持"已初始化"状态, 恢复出厂安全语义被破坏
    if(e1 != ESP_OK || e2 != ESP_OK || e3 != ESP_OK){
      Serial.printf("[SEC] secClearAuth: NVS erase/commit failed (e1=%d e2=%d e3=%d)\n",
                    (int)e1, (int)e2, (int)e3);
    }
  }
  s_is_init = false;
  s_session_key_loaded = false;
  // 内存中的密钥内容清零，避免残留
  mbedtls_platform_zeroize(s_session_key, sizeof(s_session_key));
}

// ===================== 会话加密状态 =====================
bool secHasSessionKey()         { return s_session_key_loaded; }
bool secSessionEnabled()        { return s_session_key_loaded; }

void secSetSessionKey(const uint8_t key[SEC_KEY_LEN])
{
  memcpy(s_session_key, key, SEC_KEY_LEN);
  s_session_key_loaded = true;
}

void secClearSessionKeyMemory()
{
  mbedtls_platform_zeroize(s_session_key, sizeof(s_session_key));
  s_session_key_loaded = false;
}

// ===================== AES-128-GCM 会话加解密(动态会话密钥) =====================
bool secEncrypt(const uint8_t* in, size_t len, uint8_t* out, size_t outMax, size_t* outLen)
{
  if(!s_session_key_loaded || in == nullptr || out == nullptr || outLen == nullptr) return false;
  if(len == 0 || GCM_IV_LEN + len + GCM_TAG_LEN > outMax) return false;

  uint8_t iv[GCM_IV_LEN];
  esp_fill_random(iv, sizeof(iv)); // 硬件随机IV

  mbedtls_gcm_context gcm;
  mbedtls_gcm_init(&gcm);
  int r = mbedtls_gcm_setkey(&gcm, MBEDTLS_CIPHER_ID_AES, s_session_key, AES_KEY_LEN * 8);
  if(r == 0){
    r = mbedtls_gcm_crypt_and_tag(&gcm, MBEDTLS_GCM_ENCRYPT, len,
                                  iv, GCM_IV_LEN, nullptr, 0,
                                  in, out + GCM_IV_LEN, GCM_TAG_LEN,
                                  out + GCM_IV_LEN + len);
  }
  mbedtls_gcm_free(&gcm);
  if(r != 0) return false;

  memcpy(out, iv, GCM_IV_LEN);
  *outLen = GCM_IV_LEN + len + GCM_TAG_LEN;
  return true;
}

bool secDecrypt(const uint8_t* in, size_t len, uint8_t* out, size_t outMax, size_t* outLen)
{
  if(!s_session_key_loaded || in == nullptr || out == nullptr || outLen == nullptr) return false;
  if(len < GCM_IV_LEN + GCM_TAG_LEN) return false;

  size_t ctLen = len - GCM_IV_LEN - GCM_TAG_LEN;
  if(ctLen > outMax) return false;

  const uint8_t* iv  = in;
  const uint8_t* ct  = in + GCM_IV_LEN;
  const uint8_t* tag = in + GCM_IV_LEN + ctLen;

  mbedtls_gcm_context gcm;
  mbedtls_gcm_init(&gcm);
  int r = mbedtls_gcm_setkey(&gcm, MBEDTLS_CIPHER_ID_AES, s_session_key, AES_KEY_LEN * 8);
  if(r == 0){
    r = mbedtls_gcm_auth_decrypt(&gcm, ctLen,
                                 iv, GCM_IV_LEN, nullptr, 0,
                                 tag, GCM_TAG_LEN, ct, out);
  }
  mbedtls_gcm_free(&gcm);
  if(r != 0) return false;

  *outLen = ctLen;
  return true;
}

// ===================== RSA-2048-OAEP 密钥交换 =====================
bool secRsaPubEncrypt(const uint8_t* pubDer, size_t pubLen,
                      const uint8_t* in, size_t inLen,
                      uint8_t* out, size_t outMax, size_t* outLen)
{
  if(pubDer == nullptr || pubLen == 0 || in == nullptr || out == nullptr || outLen == nullptr){
    return false;
  }
  if(inLen > SEC_KEY_LEN || outMax < SEC_RSA_CIPHER_LEN) return false;

  mbedtls_pk_context pk;
  mbedtls_pk_init(&pk);
  int r = mbedtls_pk_parse_public_key(&pk, pubDer, pubLen);
  if(r != 0){
    mbedtls_pk_free(&pk);
    return false;
  }
  if(mbedtls_pk_get_type(&pk) != MBEDTLS_PK_RSA){
    mbedtls_pk_free(&pk);
    return false;
  }

  mbedtls_rsa_context* rsa = mbedtls_pk_rsa(pk);
  // L9: 校验模长为 2048-bit(256B), 与 SEC_RSA_CIPHER_LEN 一致。
  //     若不校验, 误发 1024-bit 公钥时 OAEP 只写 128B, 而 *outLen 仍为 256,
  //     上位机按 256B 读取会拿到未初始化栈数据且密钥交换必然失败。
  if(mbedtls_rsa_get_len(rsa) != SEC_RSA_CIPHER_LEN){
    mbedtls_pk_free(&pk);
    return false;
  }
  // OAEP + SHA-256（与上位机 Go crypto/rsa.EncryptOAEP(sha256.New(), rand, pub, msg, nil) 对齐，
  // label 传空以匹配 Go 侧 label=nil）
  mbedtls_rsa_set_padding(rsa, MBEDTLS_RSA_PKCS_V21, MBEDTLS_MD_SHA256);
  size_t olen = 0;
  r = mbedtls_rsa_rsaes_oaep_encrypt(rsa, secRng, nullptr, MBEDTLS_RSA_PUBLIC,
                                     nullptr, 0, inLen, in, out);
  olen = (r == 0) ? SEC_RSA_CIPHER_LEN : 0;
  mbedtls_pk_free(&pk);

  if(r != 0) return false;
  *outLen = olen;
  return true;
}
