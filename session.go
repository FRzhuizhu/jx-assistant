package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// SessionMeta 是一个会话的元信息；消息正文存于 messages 表。
// mode: task=普通任务会话 assistant=数字助理总控 aux=辅助答疑对话
// permission: workspace-write=工作区内读写 full=完全访问 read-only=只读
// （后端权限引擎强制执行，见 perm.go；tools 为启用的能力开关 id 列表：
// unsandboxed=允许工作区外执行命令、sandbox-write=只读档下沙箱内写入、
// browser/computer=操作浏览器/电脑；完全访问模式下 unsandboxed/browser/
// computer 三项强制视为开启，面板勾选联动点亮且不可单独关闭）
// remote 非空时会话挂载 SSH 远程工作区：文件工具走 SFTP、命令走远程 shell。
type SessionMeta struct {
	ID         string           `json:"id"`
	Title      string           `json:"title"`
	Mode       string           `json:"mode"`
	Workspace  string           `json:"workspace"`
	Remote     *RemoteWorkspace `json:"remote,omitempty"`
	ParentID   string           `json:"parentId,omitempty"`
	Provider   string           `json:"provider,omitempty"`
	Model      string           `json:"model,omitempty"`
	Permission string           `json:"permission,omitempty"`
	Thinking   string           `json:"thinking,omitempty"` // 思考档位：enabled/disabled/low/medium/high，空=服务端默认
	PlanMode   bool             `json:"planMode,omitempty"`
	GoalMode   bool             `json:"goalMode,omitempty"`
	Tools      []string         `json:"tools,omitempty"`
	Archived   bool             `json:"archived,omitempty"` // 归档：不进侧栏列表，记录保留
	Unread     bool             `json:"unread,omitempty"`   // 标为未读：侧栏红点
	Status     string           `json:"status,omitempty"`   // idle | running
	CreatedAt  string           `json:"createdAt"`
	UpdatedAt  string           `json:"updatedAt"`
}

func newID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func now() string { return time.Now().Format("2006-01-02 15:04:05") }

// truncateRunes 按字符数截断字符串，超出部分以省略号结尾。
func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
