package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 拉取模型：DeepSeek 风格的富元数据端点——真实字段必须优先于按 ID 猜测，
// 且输出上限绝不能混进上下文长度。
func TestFetchModelsRichMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"deepseek-flash","name":"DeepSeek-V4.1-Flash","object":"model","owned_by":"deepseek",
			 "context_window":1048576,"max_output_tokens":393216,
			 "input_modalities":["text","image"],"output_modalities":["text"],
			 "effort":{"supported_levels":["low","high","max"],"default_level":"high"}},
			{"id":"deepseek-v4-pro","name":"DeepSeek-V4-Pro","object":"model","owned_by":"deepseek",
			 "context_window":1048576,"max_output_tokens":393216,
			 "input_modalities":["text"],"output_modalities":["text"]},
			{"id":"legacy-text-model","object":"model"}
		]}`))
	}))
	defer srv.Close()

	a := skillsTestApp(t)
	p := &ProviderCfg{ID: "t", BaseURL: srv.URL, APIKey: "k"}
	models, err := a.FetchModels(p)
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("应解析出 3 个模型，得到 %d", len(models))
	}
	flash := models[0]
	if flash.ContextWindow != 1048576 {
		t.Errorf("上下文应取真实字段 1048576，得到 %d", flash.ContextWindow)
	}
	if flash.MaxOutput != 393216 {
		t.Errorf("输出上限应取 393216，得到 %d", flash.MaxOutput)
	}
	if !flash.SupportsImages {
		t.Error("input_modalities 含 image 时应判定支持图片")
	}
	if len(flash.ReasoningLevels) != 3 || flash.ReasoningLevels[2] != "max" {
		t.Errorf("思考档位应取服务端 supported_levels: %v", flash.ReasoningLevels)
	}
	pro := models[1]
	if pro.SupportsImages {
		t.Error("纯文本模型不应误判支持图片")
	}
	// 老式端点（只有 id）：回退按 ID 猜测；完全未知的 ID 猜为 0，
	// 运行时按 DefaultContextWindow 处理，绝不取输出上限冒充上下文
	legacy := models[2]
	if legacy.ContextWindow != 0 {
		t.Errorf("未知 ID 的猜测应为 0（运行时用默认上下文），得到 %d", legacy.ContextWindow)
	}
	if legacy.MaxOutput != 0 || legacy.SupportsImages {
		t.Errorf("未知 ID 不应有输出上限或图片能力: %+v", legacy)
	}
}

// 拉取模型：纯 id 的老式端点（OpenAI 早期风格）也要能工作。
func TestFetchModelsBareIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"some-unknown-model"}]}`))
	}))
	defer srv.Close()

	a := skillsTestApp(t)
	models, err := a.FetchModels(&ProviderCfg{ID: "t", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("应解析出 2 个模型，得到 %d", len(models))
	}
	if models[0].ContextWindow != 131072 || !models[0].SupportsImages {
		t.Errorf("gpt-4o 猜测不符: %+v", models[0])
	}
}
