package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ============================================================
// NR_F2S4 BLE 安全会话实现（动态会话密钥 + RSA-2048-OAEP 密钥分发）
// 依据用户拍板修订版：
//   - 非对称算法 RSA-2048 + OAEP-SHA256
//   - 对称密钥长度 AES-128（16 字节）
//   - 密钥分发：上位机保留私钥、只发公钥(DER) → 固件随机生成 16B 对称密钥、
//     用公钥 OAEP-SHA256 加密回传 → 上位机私钥解密 → 加密 CONFIRM → 双方持久化、删除非对称密钥
//   - 固件被重置(UNINIT)时密钥全部作废，上位机再连必须重走初始化流程
//   - 加密会话帧格式：[IV(12)][密文(N)][TAG(16)]，AES-128-GCM
// ============================================================

// AESKeyLen AES-128 密钥长度
const AESKeyLen = 16

// GCMIVLen AES-GCM 初始向量长度（12 字节）
const GCMIVLen = 12

// GCMTagLen AES-GCM 认证标签长度（16 字节）
const GCMTagLen = 16

// RSABits RSA 密钥位长（用户拍板 RSA-2048）
const RSABits = 2048

// RSACipherLen RSA-2048 OAEP 密文长度
const RSACipherLen = 256

// SecurityCtx 加密会话上下文
type SecurityCtx struct {
	mu     sync.RWMutex // 保护密钥/GCM 的并发访问（发送加密指令与接收 NOTIFY 并行）
	aesKey []byte       // 16 字节 AES 会话密钥（由密钥交换动态建立，不再写死）
	gcm    cipher.AEAD
}

// NewSecurityCtx 创建空安全上下文（不含任何密钥；密钥由握手流程 SetKey 装载）
func NewSecurityCtx() *SecurityCtx {
	return &SecurityCtx{}
}

// GenerateRSAKeyPair 生成 RSA-2048 密钥对，返回私钥与公钥 DER（PKIX SubjectPublicKeyInfo）。
func GenerateRSAKeyPair() (*rsa.PrivateKey, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, RSABits)
	if err != nil {
		return nil, nil, fmt.Errorf("RSA 密钥对生成失败: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("公钥 DER 编码失败: %w", err)
	}
	return priv, pubDER, nil
}

// RSADecryptOAEP 用私钥解密固件回传的 OAEP 密文（label=nil，与固件 mbedtls 对齐）。
// 调用方完成解密后应立即清零私钥。
func RSADecryptOAEP(priv *rsa.PrivateKey, ciphertext []byte) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("RSA 私钥为空")
	}
	plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("RSA-OAEP 解密失败: %w", err)
	}
	return plain, nil
}

// HasKey 是否已有 AES 会话密钥（握手完成前为 false）
func (s *SecurityCtx) HasKey() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.aesKey) == AESKeyLen
}

// SetKey 设置 AES 密钥并初始化 GCM。
func (s *SecurityCtx) SetKey(key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(key) != AESKeyLen {
		return fmt.Errorf("AES 密钥长度必须为 %d 字节", AESKeyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("AES cipher 创建失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("GCM 创建失败: %w", err)
	}
	s.aesKey = make([]byte, AESKeyLen)
	copy(s.aesKey, key)
	s.gcm = gcm
	return nil
}

// ClearKey 清除密钥（断开/降级时调用）。
// 清除后需重新 SetKey（重连时从本地设置按 MAC 装载）才能继续加密通信。
func (s *SecurityCtx) ClearKey() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aesKey = nil
	s.gcm = nil
}

// Encrypt 加密明文为 [IV(12)][密文(N)][TAG(16)] 格式。
func (s *SecurityCtx) Encrypt(plaintext []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.gcm == nil {
		return nil, errors.New("加密会话未建立")
	}
	iv := make([]byte, GCMIVLen)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("生成 IV 失败: %w", err)
	}
	// GCM.Seal: nonce || ciphertext || tag 一次完成
	out := s.gcm.Seal(nil, iv, plaintext, nil)
	// 拼接 IV + (ciphertext+tag)
	frame := make([]byte, 0, GCMIVLen+len(out))
	frame = append(frame, iv...)
	frame = append(frame, out...)
	return frame, nil
}

// Decrypt 解密 [IV(12)][密文(N)][TAG(16)] 帧为明文。
func (s *SecurityCtx) Decrypt(frame []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.gcm == nil {
		return nil, errors.New("加密会话未建立")
	}
	if len(frame) < GCMIVLen+GCMTagLen {
		return nil, errors.New("加密帧长度不足")
	}
	iv := frame[:GCMIVLen]
	ct := frame[GCMIVLen:]
	return s.gcm.Open(nil, iv, ct, nil)
}
