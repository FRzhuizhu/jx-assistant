package main

import (
	"reflect"
	"testing"
)

// 思考档位的最终口径：存储值是当前推断的子集 → 视为旧版自动推断写死的，
// 跟随新版本刷新；拉取到的真实 supported_levels（含推断之外的档位）原样保留。
func TestEffectiveReasoningLevels(t *testing.T) {
	cases := []struct {
		name, id string
		stored   []string
		want     []string
	}{
		{
			// 存量 DeepSeek 配置：旧版 guess 写死的开/关两档，本版本补了强度档
			name: "deepseek 旧推断自动升级", id: "deepseek-flash",
			stored: []string{"enabled", "disabled"},
			want:   []string{"enabled", "disabled", "low", "high", "max"},
		},
		{
			// 拉取到的真实档位里有推断不认识的档：以存储值为准
			name: "真实 supported_levels 不被推断覆盖", id: "deepseek-v4-pro",
			stored: []string{"enabled", "low", "high", "max", "xhigh"},
			want:   []string{"enabled", "low", "high", "max", "xhigh"},
		},
		{
			name: "存储为空回退推断", id: "glm-5.3",
			stored: nil,
			want:   []string{"enabled", "low", "high", "max"},
		},
		{
			// 按命名规则无思考控制的模型：存过值说明端点报过真实档位，原样保留
			name: "推断为 nil 时存储值原样", id: "my-custom-model",
			stored: []string{"low", "high"},
			want:   []string{"low", "high"},
		},
		{
			name: "推断为 nil 且存储为空", id: "llama-3",
			stored: nil,
			want:   nil,
		},
	}
	for _, c := range cases {
		got := effectiveReasoningLevels(c.id, c.stored)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：effectiveReasoningLevels(%q, %v) = %v，期望 %v", c.name, c.id, c.stored, got, c.want)
		}
	}
}

// 预置 DeepSeek 模板必须自带强度档：新用户开箱即有思考强度可选。
func TestDeepSeekPresetHasEffortLevels(t *testing.T) {
	preset, ok := PresetByID("deepseek")
	if !ok {
		t.Fatal("预置清单里没有 deepseek")
	}
	for _, m := range preset.Models {
		for _, lv := range []string{"enabled", "disabled", "low", "high", "max"} {
			if !containsStr(m.ReasoningLevels, lv) {
				t.Errorf("预置模型 %s 缺思考档位 %q（现有 %v）", m.ID, lv, m.ReasoningLevels)
			}
		}
	}
}
