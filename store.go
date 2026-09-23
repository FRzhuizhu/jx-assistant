package main

// SQLite 存储层：单库单文件（data/jx.db），分表不分库。
// sessions 存会话元信息；messages 是全层级共用的消息表（层级只是
// parent_id 关系，这样跨层级搜索才做得到）；meta 存 schema 版本与迁移标记。
// 驱动用纯 Go 的 modernc.org/sqlite，无 CGo，保持单文件免工具链交付。

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// StoredMessage 是带存储元数据的消息：seq 是会话内单调序号；
// kind 区分 chat（正常对话）/ summary（压缩摘要）/ report（系统注入的回报）；
// compact 存规则压缩后的表示（特殊值 omit 表示组装上下文时整行跳过）。
type StoredMessage struct {
	Message
	ID         int64  `json:"id"`
	SessionID  string `json:"sessionId"`
	Seq        int64  `json:"seq"`
	Kind       string `json:"kind"`
	Origin     string `json:"origin,omitempty"` // summary 来源：auto | manual
	Compact    string `json:"-"`
	CoversUpTo int64  `json:"coversUpTo,omitempty"`
	Superseded bool   `json:"superseded,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

// 消息搜索命中：会话 + 片段。
type SearchHit struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Mode      string `json:"mode"`
	Seq       int64  `json:"seq"`
	Role      string `json:"role"`
	Snippet   string `json:"snippet"`
}

type Store struct {
	db *sql.DB
	mu sync.Mutex // 串行化写；配合单连接与 WAL，个人负载下无争用问题
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "jx.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单连接：SQLite 是单写者模型，全部语句走一个连接最稳，也彻底避开 BUSY
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.createSchema(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateLegacy(filepath.Join(dataDir, "sessions")); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) createSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT 'task',
			workspace TEXT NOT NULL DEFAULT '',
			parent_id TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'idle',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_id)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT,
			tool_calls TEXT,
			tool_call_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'chat',
			origin TEXT NOT NULL DEFAULT '',
			compact TEXT,
			covers_up_to INTEGER NOT NULL DEFAULT 0,
			superseded INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT '',
			UNIQUE(session_id, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, seq)`,
		`CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	// 旧库升级：补齐后续版本新增的会话列（已存在则跳过）
	upgrades := []string{
		`ALTER TABLE sessions ADD COLUMN permission TEXT NOT NULL DEFAULT 'workspace-write'`,
		`ALTER TABLE sessions ADD COLUMN plan_mode INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN goal_mode INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN tools TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN unread INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN remote TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN allowed TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range upgrades {
		if _, err := s.db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("升级表结构失败: %w", err)
		}
	}
	return nil
}

// migrateLegacy 把旧的 JSONL 存储（data/sessions/*.meta.json + *.jsonl）
// 一次性导入 SQLite，原文件原地保留为备份。
func (s *Store) migrateLegacy(legacyDir string) error {
	var done string
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k='legacy_migrated'`).Scan(&done)
	if err == nil && done == "1" {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	entries, err := os.ReadDir(legacyDir)
	if err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".meta.json") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".meta.json")
			meta := &SessionMeta{}
			if data, rerr := os.ReadFile(filepath.Join(legacyDir, e.Name())); rerr == nil {
				_ = json.Unmarshal(data, meta)
			}
			if meta.ID == "" {
				meta.ID = id
			}
			if meta.Mode == "" {
				meta.Mode = "task"
			}
			if uerr := s.Create(meta); uerr != nil {
				continue
			}
			if f, oerr := os.Open(filepath.Join(legacyDir, id+".jsonl")); oerr == nil {
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
				seq := int64(0)
				for sc.Scan() {
					line := strings.TrimSpace(sc.Text())
					if line == "" {
						continue
					}
					m := Message{}
					if json.Unmarshal([]byte(line), &m) != nil {
						continue
					}
					seq++
					_ = s.AppendRow(id, &StoredMessage{Message: m, Seq: seq, Kind: "chat", CreatedAt: meta.CreatedAt})
				}
				f.Close()
			}
		}
	}
	return s.setMeta("legacy_migrated", "1")
}

func (s *Store) setMeta(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO meta(k,v) VALUES(?,?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// ---------- 会话 ----------

const sessionCols = `id, title, mode, workspace, parent_id, provider, model, permission, plan_mode, goal_mode, tools, remote, allowed, archived, unread, status, created_at, updated_at`

func (s *Store) Create(m *SessionMeta) error { return s.SaveMeta(m) }

func (s *Store) SaveMeta(m *SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tools := ""
	if len(m.Tools) > 0 {
		if b, err := json.Marshal(m.Tools); err == nil {
			tools = string(b)
		}
	}
	remote := ""
	if m.Remote != nil {
		if b, err := json.Marshal(m.Remote); err == nil {
			remote = string(b)
		}
	}
	allowed := ""
	if len(m.Allowed) > 0 {
		if b, err := json.Marshal(m.Allowed); err == nil {
			allowed = string(b)
		}
	}
	_, err := s.db.Exec(`INSERT INTO sessions(`+sessionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, mode=excluded.mode,
		workspace=excluded.workspace, parent_id=excluded.parent_id, provider=excluded.provider,
		model=excluded.model, permission=excluded.permission, plan_mode=excluded.plan_mode,
		goal_mode=excluded.goal_mode, tools=excluded.tools, remote=excluded.remote,
		allowed=excluded.allowed, archived=excluded.archived, unread=excluded.unread,
		status=excluded.status, updated_at=excluded.updated_at`,
		m.ID, m.Title, m.Mode, m.Workspace, m.ParentID, m.Provider, m.Model,
		permOf(m), boolInt(m.PlanMode), boolInt(m.GoalMode), tools, remote, allowed,
		boolInt(m.Archived), boolInt(m.Unread), m.Status, m.CreatedAt, m.UpdatedAt)
	return err
}

func permOf(m *SessionMeta) string {
	if m.Permission == "" {
		return "workspace-write"
	}
	return m.Permission
}

func scanSession(sc interface{ Scan(dest ...any) error }) (*SessionMeta, error) {
	m := &SessionMeta{}
	var perm, tools, remote, allowed string
	var plan, goal, arch, unread int
	err := sc.Scan(&m.ID, &m.Title, &m.Mode, &m.Workspace, &m.ParentID,
		&m.Provider, &m.Model, &perm, &plan, &goal, &tools, &remote, &allowed,
		&arch, &unread, &m.Status, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.Permission = perm
	m.PlanMode = plan == 1
	m.GoalMode = goal == 1
	m.Archived = arch == 1
	m.Unread = unread == 1
	if tools != "" {
		_ = json.Unmarshal([]byte(tools), &m.Tools)
	}
	if remote != "" {
		r := &RemoteWorkspace{}
		if json.Unmarshal([]byte(remote), r) == nil && r.Host != "" {
			m.Remote = r
		}
	}
	if allowed != "" {
		_ = json.Unmarshal([]byte(allowed), &m.Allowed)
	}
	return m, nil
}

func (s *Store) Get(id string) (*SessionMeta, error) {
	row := s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id=?`, id)
	m, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("会话 %s 不存在", id)
	}
	return m, err
}

// List 返回全部会话，按更新时间倒序。
func (s *Store) List() ([]*SessionMeta, error) {
	rows, err := s.db.Query(`SELECT ` + sessionCols + ` FROM sessions ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionMeta{}
	for rows.Next() {
		m, err := scanSession(rows)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Children 返回挂在某会话下的子会话（派发子任务 / 辅助对话）。
func (s *Store) Children(parentID string) ([]*SessionMeta, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE parent_id=? ORDER BY updated_at DESC`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionMeta{}
	for rows.Next() {
		m, err := scanSession(rows)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM messages WHERE session_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	return err
}

func (s *Store) UpdateStatus(id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE sessions SET status=? WHERE id=?`, status, id)
	return err
}

// RunningSessions 返回状态为 running 的会话（服务启动时做中断恢复用）。
func (s *Store) RunningSessions() ([]*SessionMeta, error) {
	rows, err := s.db.Query(`SELECT ` + sessionCols + ` FROM sessions WHERE status='running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionMeta{}
	for rows.Next() {
		if m, err := scanSession(rows); err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

// ---------- 消息 ----------

// Append 追加一条普通对话消息（kind=chat），seq 自动递增，并刷新会话更新时间。
func (s *Store) Append(id string, m Message) error {
	return s.AppendRow(id, &StoredMessage{Message: m, Kind: "chat", CreatedAt: now()})
}

// AppendRow 追加一条完整消息记录；Seq 为 0 时自动分配。
func (s *Store) AppendRow(id string, sm *StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sm.Seq == 0 {
		if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM messages WHERE session_id=?`, id).Scan(&sm.Seq); err != nil {
			return err
		}
	}
	var content any
	if sm.Content != nil {
		content = sm.Text()
	}
	var toolCalls any
	if len(sm.ToolCalls) > 0 {
		b, err := json.Marshal(sm.ToolCalls)
		if err != nil {
			return err
		}
		toolCalls = string(b)
	}
	var compact any
	if sm.Compact != "" {
		compact = sm.Compact
	}
	_, err := s.db.Exec(`INSERT INTO messages
		(session_id, seq, role, content, tool_calls, tool_call_id, kind, origin, compact, covers_up_to, superseded, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, sm.Seq, sm.Role, content, toolCalls, sm.ToolCallID, sm.Kind, sm.Origin,
		compact, sm.CoversUpTo, boolInt(sm.Superseded), sm.CreatedAt)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, now(), id)
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const messageCols = `id, session_id, seq, role, content, tool_calls, tool_call_id, kind, origin, compact, covers_up_to, superseded, created_at`

func scanMessage(sc interface{ Scan(dest ...any) error }) (*StoredMessage, error) {
	sm := &StoredMessage{}
	var content, toolCalls, compact sql.NullString
	var sup int
	err := sc.Scan(&sm.ID, &sm.SessionID, &sm.Seq, &sm.Role, &content, &toolCalls,
		&sm.ToolCallID, &sm.Kind, &sm.Origin, &compact, &sm.CoversUpTo, &sup, &sm.CreatedAt)
	if err != nil {
		return nil, err
	}
	if content.Valid {
		sm.Content = content.String
	}
	if toolCalls.Valid && toolCalls.String != "" {
		_ = json.Unmarshal([]byte(toolCalls.String), &sm.ToolCalls)
	}
	sm.Compact = compact.String
	sm.Superseded = sup == 1
	return sm, nil
}

// Messages 返回会话全部消息（按 seq 升序），展开为模型/前端通用的 Message 形态。
func (s *Store) Messages(id string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		sm, err := scanMessage(rows)
		if err != nil {
			continue
		}
		if sm.Kind == "summary" {
			continue // 摘要只在组装上下文时按管线注入，不进入普通消息流
		}
		out = append(out, sm.Message)
	}
	return out, rows.Err()
}

// RawMessages 分页读取原生历史（含摘要行，未做任何折叠），供历史查询工具与压缩使用。
func (s *Store) RawMessages(id string, offset, limit int) ([]*StoredMessage, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, id).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE session_id=? ORDER BY seq LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*StoredMessage{}
	for rows.Next() {
		if sm, err := scanMessage(rows); err == nil {
			out = append(out, sm)
		}
	}
	return out, total, rows.Err()
}

// RawMessagesAll 读取会话全部消息（含摘要行），供上下文组装与压缩使用。
func (s *Store) RawMessagesAll(id string) ([]*StoredMessage, error) {
	rows, err := s.db.Query(`SELECT ` + messageCols + ` FROM messages WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*StoredMessage{}
	for rows.Next() {
		if sm, err := scanMessage(rows); err == nil {
			out = append(out, sm)
		}
	}
	return out, rows.Err()
}

// SupersedeAutoSummaries 把会话内所有未过期的自动摘要标记为已过期（新自动摘要合并旧摘要时用）。
func (s *Store) SupersedeAutoSummaries(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE messages SET superseded=1 WHERE session_id=? AND kind='summary' AND origin='auto' AND superseded=0`, sessionID)
	return err
}
func (s *Store) SetCompact(sessionID string, seq int64, compact string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE messages SET compact=? WHERE session_id=? AND seq=?`, compact, sessionID, seq)
	return err
}

// SearchMessages 跨会话全文搜索（当前 LIKE 实现；个人规模数据足够，量大后可升 FTS）。
func (s *Store) SearchMessages(query string, limit int) ([]SearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query 不能为空")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT m.session_id, s.title, s.mode, m.seq, m.role, m.content
		FROM messages m JOIN sessions s ON s.id = m.session_id
		WHERE m.kind IN ('chat','report') AND m.content LIKE '%' || ? || '%'
		ORDER BY m.id DESC LIMIT ?`, query, limit*3)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var content sql.NullString
		if err := rows.Scan(&h.SessionID, &h.Title, &h.Mode, &h.Seq, &h.Role, &content); err != nil {
			continue
		}
		h.Snippet = snippetAround(content.String, query, 90)
		hits = append(hits, h)
	}
	// 同一会话只留一条最佳命中（最新的），避免刷屏
	seen := map[string]bool{}
	out := []SearchHit{}
	for _, h := range hits {
		if seen[h.SessionID] {
			continue
		}
		seen[h.SessionID] = true
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// snippetAround 截取命中位置附近的片段。
func snippetAround(s, query string, radius int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	idx := strings.Index(s, query)
	if idx < 0 {
		if len([]rune(s)) <= radius*2 {
			return s
		}
		r := []rune(s)
		return string(r[:radius]) + "…"
	}
	r := []rune(s)
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + len(query) + radius
	if end > len(r) {
		end = len(r)
	}
	out := ""
	if start > 0 {
		out += "…"
	}
	out += string(r[start:end])
	if end < len(r) {
		out += "…"
	}
	return out
}

func (s *Store) Close() error { return s.db.Close() }
