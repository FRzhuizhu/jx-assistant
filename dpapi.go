package main

// 敏感字段静态加密（P1-8）：API Key / SSH 密码落盘前用 Windows DPAPI
// （用户级，CryptProtectData）加密，格式 dpapi:v1:<base64>，读取时透明解密。
// 沙箱读不受限（官方同款语义）——沙箱内命令 type config.json 看到的只是
// 密文；DPAPI 绑定当前 Windows 用户，密文被拷走也无法在别处解密。
// 首启迁移：旧版明文字段照常读入，下一次保存自动转为密文。

import (
	"encoding/base64"
	"errors"
	"strings"
)

const dpapiPrefix = "dpapi:v1:"

// errNotEncrypted 表示取值不是密文（旧版明文或空值）。
var errNotEncrypted = errors.New("not encrypted")

// protectSecret 把明文加密为 dpapi:v1:<base64>。空串与已是密文的值原样返回
// （避免双重加密）。加密失败时返回原值不落盘密文（fail open：宁可明文也不
// 让用户配置丢失），并把错误交给调用方记日志。
func protectSecret(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, dpapiPrefix) {
		return plain, nil
	}
	blob, err := dpapiProtect([]byte(plain))
	if err != nil {
		return plain, err
	}
	if string(blob) == plain {
		// 平台直通（非 Windows）：不打前缀，避免误标为密文
		return plain, nil
	}
	return dpapiPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// unprotectSecret 透明解密 dpapi:v1: 前缀的值；明文（旧版）原样返回。
// 解密失败（如配置被拷到别的机器/账户）时保留原值——不破坏配置文件，
// 由上层把「密钥无效」如实暴露给用户重新填写。
func unprotectSecret(stored string) string {
	if !strings.HasPrefix(stored, dpapiPrefix) {
		return stored
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, dpapiPrefix))
	if err != nil {
		return stored
	}
	plain, err := dpapiUnprotect(raw)
	if err != nil {
		return stored
	}
	return string(plain)
}
