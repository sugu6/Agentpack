package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// machineKey 从机器特定标识符派生 AES-256 密钥
// 用于导出文件中敏感数据的加密，不影响 Agent 配置文件的明文存储
var (
	machineKey    []byte
	machineKeyErr error
)

func init() {
	machineKey, machineKeyErr = deriveMachineKey()
}

// deriveMachineKey 从机器标识符派生 32 字节 AES-256 密钥
// 优先读取持久化的密钥文件，保证跨会话一致性；
// 若不存在则用 crypto/rand 生成真正的随机密钥并持久化。
// 机器标识符仅用于生成密钥指纹（辅助标识），不作为密钥来源，
// 确保非 Windows 系统同样拥有高熵密钥。
func deriveMachineKey() ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("derive machine key: determine home directory: %w", err)
	}
	if home == "" {
		return nil, fmt.Errorf("derive machine key: home directory is empty")
	}
	keyDir := filepath.Join(home, ".agentpack")
	keyFile := filepath.Join(keyDir, ".machine_key")

	// 1. 优先读取已持久化的密钥
	if data, err := os.ReadFile(keyFile); err == nil && len(data) == 32 {
		return data, nil
	}

	// 2. 用 crypto/rand 生成真正的随机 32 字节密钥
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，无法安全运行
		panic(fmt.Sprintf("crypto/rand unavailable: %v (encryption key cannot be safely generated)", err))
	}

	// 3. 持久化密钥供后续使用
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("persist machine key: mkdir: %w", err)
	}
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		return nil, fmt.Errorf("persist machine key: writefile: %w", err)
	}
	return key, nil
}

// Encrypt 加密字符串，返回 base64 编码的密文
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if machineKeyErr != nil {
		return "", machineKeyErr
	}

	block, err := aes.NewCipher(machineKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 解密 base64 编码的密文
func Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if machineKeyErr != nil {
		return "", machineKeyErr
	}

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(machineKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// IsEncrypted 检查字符串是否是加密的（以 "enc:" 前缀标识）
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, "enc:")
}

// EncryptEnv 加密 env map 中的敏感值（包含 TOKEN、KEY、SECRET、PASSWORD 等关键词）
// 已加密（以 "enc:" 前缀标识）的值会跳过，防止双重加密。
// 加密失败时返回错误，避免敏感数据以明文意外泄露
func EncryptEnv(env map[string]string) (map[string]string, error) {
	if env == nil {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		// 跳过已加密的值，防止双重加密
		if IsEncrypted(v) {
			out[k] = v
			continue
		}
		// 检测敏感字段关键词（按 _ 分隔的单词边界匹配，避免误匹配子串如 MONKEY、AUTHOR）
		if isSensitiveEnvKey(k) {
			encrypted, err := Encrypt(v)
			if err != nil {
				return nil, fmt.Errorf("encrypt env %q: %w", k, err)
			}
			out[k] = "enc:" + encrypted
		} else {
			out[k] = v
		}
	}
	return out, nil
}

// isSensitiveEnvKey 检查环境变量名是否包含敏感关键词。
// 使用 _ 分隔的单词边界匹配，避免误匹配子串（如 MONKEY 中的 "key"、AUTHOR 中的 "auth"）。
func isSensitiveEnvKey(name string) bool {
	lower := strings.ToLower(name)
	sensitiveWords := []string{"token", "secret", "password", "credential", "apikey"}
	for _, w := range sensitiveWords {
		if hasWord(lower, w) {
			return true
		}
	}
	// "key" 和 "auth" 独立处理：它们更短，需要额外的负向过滤
	if hasWord(lower, "key") && !hasWord(lower, "monkey") && !hasWord(lower, "keyboard") && !hasWord(lower, "keycap") {
		return true
	}
	if hasWord(lower, "auth") && !hasWord(lower, "author") && !hasWord(lower, "authoress") {
		return true
	}
	return false
}

// hasWord 检查 name 中是否包含按 _ 分隔的完整单词 word。
// 例如 "api_key" 包含单词 "key"，但 "monkey" 不包含。
func hasWord(name, word string) bool {
	return name == word ||
		strings.HasPrefix(name, word+"_") ||
		strings.HasSuffix(name, "_"+word) ||
		strings.Contains(name, "_"+word+"_")
}

// DecryptEnv 解密 env map 中的加密值
// 解密失败时返回错误，避免返回不可用的密文数据
func DecryptEnv(env map[string]string) (map[string]string, error) {
	if env == nil {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if IsEncrypted(v) {
			decrypted, err := Decrypt(strings.TrimPrefix(v, "enc:"))
			if err != nil {
				return nil, fmt.Errorf("decrypt env %q: %w", k, err)
			}
			out[k] = decrypted
		} else {
			out[k] = v
		}
	}
	return out, nil
}
