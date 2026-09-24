package main

import "testing"

// TestSameOrigin 同源判定矩阵（P1-9 审批/权限 API 的 CSRF 面封堵依据）。
func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:8788", "127.0.0.1:8788", true},
		{"http://localhost:8788", "127.0.0.1:8788", false}, // 不同主机
		{"http://evil.example.com", "127.0.0.1:8788", false},
		{"http://127.0.0.1:8788", "127.0.0.1:8788", true},
		{"http://127.0.0.1:9999", "127.0.0.1:8788", false}, // 不同端口
		{"https://example.com", "example.com", true},       // 443 缺省等价
		{"http://example.com", "example.com:80", true},     // 80 缺省等价
		{"https://example.com", "example.com:443", true},
		{"http://example.com", "example.com:443", false},
		{"", "127.0.0.1:8788", false}, // 空 Origin 解析不出 host → false 但由调用方跳过
		{"not-a-url", "127.0.0.1:8788", false},
		{"http://[::1]:8788", "[::1]:8788", true}, // IPv6 字面量
	}
	for _, c := range cases {
		if c.origin == "" {
			continue // 空 Origin 由 withCORS 直接放行（非浏览器客户端），不经此函数
		}
		if got := sameOrigin(c.origin, c.host); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", c.origin, c.host, got, c.want)
		}
	}
}
