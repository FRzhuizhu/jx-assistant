package main

// progress_write 参数解析：schema 声明为数组（全量清单整体替换），旧 schema 的
// 单对象形态（progress 曾被声明为单个 {content,status} 对象）保持兼容。

import (
	"encoding/json"
	"testing"
)

func TestProgressItemsFromArgs(t *testing.T) {
	// 数组：schema 声明的正常形态；status 缺省按 pending 处理（下游补默认）
	items, err := progressItemsFromArgs(json.RawMessage(
		`{"progress":[{"content":"第一步：调研","status":"in_progress"},{"content":"第二步：实施"}]}`))
	if err != nil || len(items) != 2 || items[0].Status != "in_progress" || items[1].Status != "" {
		t.Fatalf("数组形态解析失败: items=%+v err=%v", items, err)
	}
	// 单对象：旧 schema 形态，包成单项列表（2026-10-13 修复前会报 unmarshal 错）
	items, err = progressItemsFromArgs(json.RawMessage(
		`{"progress":{"content":"读齐接口细节","status":"in_progress"}}`))
	if err != nil || len(items) != 1 || items[0].Content != "读齐接口细节" || items[0].Status != "in_progress" {
		t.Fatalf("单对象兼容失败: items=%+v err=%v", items, err)
	}
	// 缺省 / null / 空数组：均为空清单（清空语义，由 runProgressWrite 落库）
	for _, args := range []string{`{}`, `{"progress":null}`, `{"progress":[]}`} {
		if items, err = progressItemsFromArgs(json.RawMessage(args)); err != nil || len(items) != 0 {
			t.Fatalf("%s 应解析为空清单: items=%+v err=%v", args, items, err)
		}
	}
	// 非法形态仍要报错（字符串既不是数组也不是单对象）
	if _, err = progressItemsFromArgs(json.RawMessage(`{"progress":"abc"}`)); err == nil {
		t.Fatal("字符串形态应报错")
	}
}
