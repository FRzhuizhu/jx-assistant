package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 步数上限（maxSteps）：全新安装按新默认 100；老配置里还是旧默认 30 的自动提到
// 100 并落盘（升级机器不再卡在 30 步）；手动设过其他值的一律保留；缺项/0 按默认补。
func TestMaxStepsDefaultAndLegacyBump(t *testing.T) {
	cases := []struct {
		name string
		raw  string // 空串 = 不预置配置文件（模拟全新安装）
		want int
	}{
		{"全新安装", "", maxStepsDefault},
		{"老配置仍是旧默认 30", `{"maxSteps":30}`, maxStepsDefault},
		{"手动调大到 50", `{"maxSteps":50}`, 50},
		{"手动调小到 5", `{"maxSteps":5}`, 5},
		{"配置里缺这一项", `{"port":8899}`, maxStepsDefault},
		{"写成 0（异常值）", `{"maxSteps":0}`, maxStepsDefault},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if tc.raw != "" {
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("%s: 加载失败: %v", tc.name, err)
		}
		if cfg.MaxSteps != tc.want {
			t.Fatalf("%s: 运行时 maxSteps 应为 %d，得到 %d", tc.name, tc.want, cfg.MaxSteps)
		}
		// 落盘值一致（提升要立即回写，免得上层读数与文件打架）
		disk, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("%s: 重新读盘失败: %v", tc.name, err)
		}
		if disk.MaxSteps != tc.want {
			t.Fatalf("%s: 重读后 maxSteps 应为 %d，得到 %d", tc.name, tc.want, disk.MaxSteps)
		}
	}
}
