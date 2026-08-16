package crypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestEncryptEnv_SkipsAlreadyEncrypted 验证已加密值跳过，不发生双重加密
func TestEncryptEnv_SkipsAlreadyEncrypted(t *testing.T) {
	// 生成一个真实密文作为"已加密"输入
	ct, err := Encrypt("secret-value")
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"API_TOKEN": "enc:" + ct,
		"FOO":       "bar",
	}
	out, err := EncryptEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if out["API_TOKEN"] != "enc:"+ct {
		t.Errorf("already-encrypted value must pass through unchanged")
	}
	if out["FOO"] != "bar" {
		t.Errorf("non-sensitive value must pass through unchanged")
	}
}

// TestEncryptEnv_PlainValueWithEncPrefix 验证明文恰好以 "enc:" 开头时
// 不会被误判为已加密跳过，而是按敏感字段加密。
func TestEncryptEnv_PlainValueWithEncPrefix(t *testing.T) {
	env := map[string]string{
		"SECRET_KEY": "enc:plain-looking-value",
	}
	out, err := EncryptEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(out["SECRET_KEY"]) {
		t.Errorf("expected value to be encrypted, got %q", out["SECRET_KEY"])
	}
	// 解密后应还原为原文
	dec, err := DecryptEnv(out)
	if err != nil {
		t.Fatal(err)
	}
	if dec["SECRET_KEY"] != "enc:plain-looking-value" {
		t.Errorf("round trip mismatch: got %q", dec["SECRET_KEY"])
	}
}

// TestDecryptEnv_PlainValueWithEncPrefix 验证非敏感键的明文 enc: 前缀值
// 在 DecryptEnv 中按明文保留而不是报错。
func TestDecryptEnv_PlainValueWithEncPrefix(t *testing.T) {
	env := map[string]string{
		"MY_MESSAGE": "enc:hello",
	}
	out, err := DecryptEnv(env)
	if err != nil {
		t.Fatalf("expected no error for plain enc:-prefixed value, got %v", err)
	}
	if out["MY_MESSAGE"] != "enc:hello" {
		t.Errorf("expected plain value preserved, got %q", out["MY_MESSAGE"])
	}
}

// TestIsSensitiveEnvKey_Normalization 验证不同命名风格都能识别敏感关键词
func TestIsSensitiveEnvKey_Normalization(t *testing.T) {
	sensitive := []string{
		"API_KEY", "api_key", "API-KEY", "api-key", "apiKey", "ApiKey", "APIKEY",
		"DB_PASSWORD", "dbPassword", "ACCESS_TOKEN", "CLIENT_SECRET",
		"AWS_CREDENTIAL", "OPENAI_APIKEY",
	}
	for _, k := range sensitive {
		if !isSensitiveEnvKey(k) {
			t.Errorf("expected %q to be sensitive", k)
		}
	}

	notSensitive := []string{
		"MONKEY", "monkey", "KEYBOARD", "AUTHOR", "AUTHORS", "MONKEY_PATCH",
		"HELLO", "NAME", "USER_MONKEY_TAIL",
	}
	for _, k := range notSensitive {
		if isSensitiveEnvKey(k) {
			t.Errorf("expected %q to be NOT sensitive", k)
		}
	}
}

// TestNormalizeEnvKey 验证名称归一化
func TestNormalizeEnvKey(t *testing.T) {
	cases := map[string]string{
		"API_KEY":        "api_key",
		"api-key":        "api_key",
		"apiKey":         "api_key",
		"ApiKey":         "api_key",
		"APIKEY":         "apikey",
		"APIKey":         "api_key",
		"myApiKey":       "my_api_key",
		"DBPassword":     "db_password",
		"MY_MONKEY_TAIL": "my_monkey_tail",
		"plain":          "plain",
	}
	for in, want := range cases {
		if got := normalizeEnvKey(in); got != want {
			t.Errorf("normalizeEnvKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHasWord 验证单词边界匹配
func TestHasWord(t *testing.T) {
	if !hasWord("api_key", "key") {
		t.Error("expected api_key to contain word key")
	}
	if hasWord("monkey", "key") {
		t.Error("expected monkey to NOT contain word key")
	}
	if !hasWord("key", "key") {
		t.Error("expected exact match")
	}
	if !hasWord("secret_token_value", "token") {
		t.Error("expected middle word match")
	}
	// 验证关键词本身作为子串时不误报
	if strings.Contains("monkey", "key") && !hasWord("monkey", "key") {
		// ok: sub-string present but not word-boundary
	}
}

func TestDecryptEnv_ForeignCiphertextReturnsError(t *testing.T) {
	env := map[string]string{
		"API_KEY": "enc:" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	_, err := DecryptEnv(env)
	if err == nil {
		t.Fatal("expected error for ciphertext that cannot be decrypted with local key")
	}
}
