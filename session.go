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
//      office=标准办公模式 coding=编程模式（扩展模式样板：设计人员等未来模式照此新增）
// permission: workspace-write=工作区内读写 full=完全访问 read-only=只读
// （后端权限引擎强制执行，见 perm.go；tools 为启用的能力开关 id 列表：
// unsandboxed=允许工作区外执行命令、sandbox-write=只读档下沙箱内写入、
// browser/computer=操作浏览器/电脑；完全访问模式下 unsandboxed/browser/
// computer 三项强制视为开启，面板勾选联动点亮且不可单独关闭，切回其他
// 档位时自动取消）
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
	GoalText   string           `json:"goalText,omitempty"`   // 目标模式的当前目标
	GoalStatus string           `json:"goalStatus,omitempty"` // active=推进中 complete=已达成 blocked=受阻
	GoalRounds int              `json:"goalRounds,omitempty"` // 自动续跑已用轮数
	Tools      []string         `json:"tools,omitempty"`
	Origin     string           `json:"origin,omitempty"` // 来源：human=人工建的子对话（右栏标记 + 划选送子对话复用判定）
	// 子对话专家名（dispatch_sub 的 expert 预设）：只记名字，系统提示词构建时
	// 按名解析追加（专家被删改后该段随之变化；名字解析不到则静默无追加段）
	Expert string `json:"expert,omitempty"`
	// 数字助理派发的工作任务是顶层会话（左侧任务列表可见）；dispatched_by 记
	// 派发它的助理会话 id，作为回报链（服务重启的中断回报 / list_sessions 标记）。
	DispatchedBy string `json:"dispatchedBy,omitempty"`
	Archived   bool             `json:"archived,omitempty"` // 归档：不进侧栏列表，记录保留
	Unread     bool             `json:"unread,omitempty"`   // 标为未读：侧栏红点
	Status     string           `json:"status,omitempty"`   // idle | running
	// 最近一轮异常中断的类型（step=达到步数上限 / error=执行失败 / restart=服务在
	// 执行期间重启，空=正常收口）：左侧栏把时间位换成「⚠ 已中断」提醒，与
	// 「⟳ 运行中」同槽、天然互斥。值存在 meta 表（Store.SetLastStop），只由会话
	// 列表接口临时填进来——不在 sessionCols 里，SaveMeta 也就写不到它。
	LastStop string `json:"lastStop,omitempty"`
	// 累计用量（来自每次模型调用的流尾 usage，按 token 累加而非按次平均，
	// 供上下文面板「Token 用量」分组：缓存命中% = hit/prompt）
	UsagePrompt   int64 `json:"usagePrompt,omitempty"`   // 历次调用 prompt_tokens 总和（输入）
	UsageCacheHit int64 `json:"usageCacheHit,omitempty"` // 历次调用 prompt_cache_hit_tokens 总和（缓存读取）
	UsageOutput   int64 `json:"usageOutput,omitempty"`   // 历次调用 completion_tokens 总和（输出）
	UsageCalls    int64 `json:"usageCalls,omitempty"`    // 模型调用次数
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
