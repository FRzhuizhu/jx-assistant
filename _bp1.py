# -*- coding: utf-8 -*-
# 后端补丁：unread 列 + fork 端点
import io
miss = []
def patch(path, pairs):
    s = io.open(path, encoding='utf-8').read()
    for old, new in pairs:
        if old in s:
            s = s.replace(old, new, 1)
        else:
            miss.append(path + ': ' + old[:60].replace('\n', '\\n'))
    io.open(path, 'w', encoding='utf-8').write(s)

# 1. SessionMeta.unread
patch('session.go', [(
'\tArchived   bool     `json:"archived,omitempty"` // 归档：不进侧栏列表，记录保留',
'\tArchived   bool     `json:"archived,omitempty"` // 归档（回收站）：不进侧栏列表，记录保留\n\tUnread     bool     `json:"unread,omitempty"`   // 标为未读：侧栏红点')])

# 2. store.go：unread 列（升级 + 列清单 + save/scan）
patch('store.go', [(
"\t\t`ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,",
"\t\t`ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,\n\t\t`ALTER TABLE sessions ADD COLUMN unread INTEGER NOT NULL DEFAULT 0`,")])

s = io.open('store.go', encoding='utf-8').read()
s = s.replace(
    "const sessionCols = `id, title, mode, workspace, parent_id, provider, model, permission, plan_mode, goal_mode, tools, archived, status, created_at, updated_at`",
    "const sessionCols = `id, title, mode, workspace, parent_id, provider, model, permission, plan_mode, goal_mode, tools, archived, unread, status, created_at, updated_at`", 1)
s = s.replace(
    "\t\tgoal_mode=excluded.goal_mode, tools=excluded.tools, archived=excluded.archived,\n\t\tstatus=excluded.status, updated_at=excluded.updated_at`,\n\t\tm.ID, m.Title, m.Mode, m.Workspace, m.ParentID, m.Provider, m.Model,\n\t\tpermOf(m), boolInt(m.PlanMode), boolInt(m.GoalMode), tools, boolInt(m.Archived), m.Status, m.CreatedAt, m.UpdatedAt)",
    "\t\tgoal_mode=excluded.goal_mode, tools=excluded.tools, archived=excluded.archived,\n\t\tunread=excluded.unread, status=excluded.status, updated_at=excluded.updated_at`,\n\t\tm.ID, m.Title, m.Mode, m.Workspace, m.ParentID, m.Provider, m.Model,\n\t\tpermOf(m), boolInt(m.PlanMode), boolInt(m.GoalMode), tools, boolInt(m.Archived), boolInt(m.Unread), m.Status, m.CreatedAt, m.UpdatedAt)", 1)
s = s.replace(
    "\tvar perm, tools string\n\tvar plan, goal, arch int\n\terr := sc.Scan(&m.ID, &m.Title, &m.Mode, &m.Workspace, &m.ParentID,\n\t\t&m.Provider, &m.Model, &perm, &plan, &goal, &tools, &arch, &m.Status, &m.CreatedAt, &m.UpdatedAt)",
    "\tvar perm, tools string\n\tvar plan, goal, arch, unread int\n\terr := sc.Scan(&m.ID, &m.Title, &m.Mode, &m.Workspace, &m.ParentID,\n\t\t&m.Provider, &m.Model, &perm, &plan, &goal, &tools, &arch, &unread, &m.Status, &m.CreatedAt, &m.UpdatedAt)", 1)
s = s.replace(
    "\tm.Archived = arch == 1\n",
    "\tm.Archived = arch == 1\n\tm.Unread = unread == 1\n", 1)
io.open('store.go', 'w', encoding='utf-8').write(s)

# 3. PATCH 支持 unread
patch('server.go', [(
'\t\tArchived   *bool     `json:"archived"`',
'\t\tArchived   *bool     `json:"archived"`\n\t\tUnread     *bool     `json:"unread"`'),
(
'\tif body.Archived != nil {\n\t\tm.Archived = *body.Archived\n\t}',
'\tif body.Archived != nil {\n\t\tm.Archived = *body.Archived\n\t}\n\tif body.Unread != nil {\n\t\tm.Unread = *body.Unread\n\t}')])

# 4. fork 端点
patch('server.go', [(
'\tmux.HandleFunc("GET /api/approvals", a.handleListApprovals)',
'''\tmux.HandleFunc("POST /api/sessions/{id}/fork", a.handleFork)
\tmux.HandleFunc("GET /api/approvals", a.handleListApprovals)'''),
(
'// handleListApprovals 返回待决定的审批（前端轮询兜底）。',
'''// handleFork 分支对话：复制出一个新会话。若目标正在执行，只复制到
// 最近一轮完成的位置（最后一条带正文且无工具调用的助手消息）。
func (a *App) handleFork(w http.ResponseWriter, r *http.Request) {
\tm := a.sessionFromPath(w, r)
\tif m == nil {
\t\treturn
\t}
\tall, err := a.Store.RawMessagesAll(m.ID)
\tif err != nil {
\t\twriteErr(w, 500, err.Error())
\t\treturn
\t}
\tmsgs := []*StoredMessage{}
\tfor _, x := range all {
\t\tif x.Kind != "summary" {
\t\t\tmsgs = append(msgs, x)
\t\t}
\t}
\tif a.IsRunning(m.ID) {
\t\tlast := -1
\t\tfor i, x := range msgs {
\t\t\tif x.Role == "assistant" && len(x.ToolCalls) == 0 && strings.TrimSpace(x.Text()) != "" {
\t\t\t\tlast = i
\t\t\t}
\t\t}
\t\tif last >= 0 {
\t\t\tmsgs = msgs[:last+1]
\t\t}
\t}
\tfork := &SessionMeta{
\t\tID: newID(), Title: m.Title + "（分支）", Mode: m.Mode, Workspace: m.Workspace,
\t\tParentID: m.ParentID, Provider: m.Provider, Model: m.Model, Permission: m.Permission,
\t\tPlanMode: m.PlanMode, GoalMode: m.GoalMode, Tools: m.Tools,
\t\tCreatedAt: now(), UpdatedAt: now(),
\t}
\tif err := a.Store.Create(fork); err != nil {
\t\twriteErr(w, 500, err.Error())
\t\treturn
\t}
\tfor _, x := range msgs {
\t\t_ = a.Store.AppendRow(fork.ID, &StoredMessage{
\t\t\tMessage: x.Message, Kind: x.Kind, CreatedAt: fork.CreatedAt,
\t\t})
\t}
\ta.broadcast(Event{Type: "session_updated", SessionID: fork.ID})
\twriteJSON(w, 200, fork)
}

// handleListApprovals 返回待决定的审批（前端轮询兜底）。''')])

print('missed:', len(miss))
for m in miss:
    print(' -', m)
