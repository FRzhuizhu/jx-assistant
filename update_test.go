package main

// versionCompare 双体系版本号比较的单测：新的「日期+V序号」（20261008V2）
// 与旧点分号（0.1.9），以及两体系交界处「日期恒新」的规则。

import "testing"

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// 旧体系（原语义不回退）
		{"0.1.9", "0.1.9", 0},
		{"0.2", "0.2.0", 0},
		{"0.2.1", "0.2", 1},
		{"1.0.0", "0.9.9", 1},
		{"v0.2", "0.1.9", 1},
		// 新体系：日期比 8 位数字，再比 V 序号（V10 > V9）
		{"20261008V1", "20261008V1", 0},
		{"20261008V2", "20261008V1", 1},
		{"20261008V1", "20261008V2", -1},
		{"20261008V10", "20261008V9", 1}, // 字典序会排错，必须按整数比
		{"20261009V1", "20261008V99", 1}, // 日期优先于序号
		{"20261101V1", "20261031V1", 1},
		{"20261008v2", "20261008V10", -1}, // 小写 v 同样识别
		// 交界：日期体系恒新于旧体系
		{"20260101V1", "99.99.99", 1},
		{"99.99.99", "20260101V1", -1},
	}
	for _, c := range cases {
		if got := versionCompare(c.a, c.b); got != c.want {
			t.Fatalf("versionCompare(%q, %q) = %d，期待 %d", c.a, c.b, got, c.want)
		}
		// 对称性：反向比较取反（相等除外）
		if rev := versionCompare(c.b, c.a); rev != -c.want {
			t.Fatalf("versionCompare(%q, %q) = %d，与正向 %d 不对称", c.b, c.a, rev, c.want)
		}
	}
}
