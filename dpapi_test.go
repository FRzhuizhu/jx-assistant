package main

import (
	"strings"
	"testing"
)

// TestSecretRoundTrip 验证敏感字段加解密往返与防双重加密。
func TestSecretRoundTrip(t *testing.T) {
	plain := "sk-test-1234567890"
	enc, err := protectSecret(plain)
	if err != nil {
		t.Fatalf("protectSecret: %v", err)
	}
	if !strings.HasPrefix(enc, dpapiPrefix) {
		t.Fatalf("密文缺少 %s 前缀: %q", dpapiPrefix, enc)
	}
	if strings.Contains(enc, plain) {
		t.Fatalf("密文包含明文: %q", enc)
	}
	if got := unprotectSecret(enc); got != plain {
		t.Fatalf("解密不匹配: %q != %q", got, plain)
	}

	// 防双重加密：已是密文的值原样返回
	enc2, err := protectSecret(enc)
	if err != nil {
		t.Fatalf("protectSecret(密文): %v", err)
	}
	if enc2 != enc {
		t.Fatalf("密文被二次加密: %q → %q", enc, enc2)
	}

	// 旧版明文原样返回（首启迁移路径）
	if got := unprotectSecret("legacy-plain"); got != "legacy-plain" {
		t.Fatalf("明文不应被改动: %q", got)
	}
	if got, err := protectSecret(""); err != nil || got != "" {
		t.Fatalf("空值应原样: %q %v", got, err)
	}
}
