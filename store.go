package main

// SQLite 存储层：单库单文件（data/jx.db），分表不分库。
// sessions 存会话元信息；messages 是全层级共用的消息表（层级只是
// parent_id 关系，这样跨层级搜索才做得到）；meta 存 schema 版本与迁移标记。
// 驱动用纯 Go 的 modernc.org/sqlite，无 CGo，保持单文件免工具链交付。

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	Thinking   string `json:"thinking,omitempty"` // assistant 该步的思考过程原文
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
	db   *sql.DB
	path string
	mu   sync.Mutex // 串行化写；配合单连接与 WAL，个人负载下无争用问题
}

// dataPath 返回库文件路径（界面「数据位置」展示用）。
func (s *Store) dataPath() string { return s.path }

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "jx.db")
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单连接：SQLite 是单写者模型，全部语句走一个连接最稳，也彻底避开 BUSY
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: dbPath}
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

// migration 是一条数据库结构迁移。sql 按序执行；tolerant 用于
// 兼容账本引入之前手工升级过的旧库（列已存在视为已应用）。
type migration struct {
	id       string
	sql      []string
	tolerant bool // 忽略 duplicate column 错误（幂等升级）
}

func (m migration) checksum() string {
	h := sha256.New()
	fmt.Fprint(h, m.id, "\x00", strings.Join(m.sql, "\x00"))
	return hex.EncodeToString(h.Sum(nil))
}

// storeMigrations 是库结构的全部演进史，只允许在末尾追加。
// 历史条目一经应用即被 checksum 冻结（改动会拒绝启动），
// 新的结构变化一律新增条目，保持旧库可平滑升级。
var storeMigrations = []migration{
	{
		id: "0001_baseline",
		sql: []string{
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
		},
	},
	{
		id:       "0002_session_columns",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN permission TEXT NOT NULL DEFAULT 'workspace-write'`,
			`ALTER TABLE sessions ADD COLUMN plan_mode INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN goal_mode INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN tools TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN unread INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN remote TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN allowed TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		id:       "0003_thinking",
		tolerant: true,
		sql: []string{
			`ALTER TABLE messages ADD COLUMN thinking TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN thinking TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		id:       "0004_goal",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN goal_text TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN goal_status TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN goal_rounds INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		id:       "0005_usage",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN usage_prompt INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN usage_cache_hit INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN usage_calls INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		id:       "0006_usage_output",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN usage_output INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		// 会话来源：human=人工建的子对话（克隆父上下文的分支）。
		// 历史 aux 会话全是人工建的（当时 AI 派发一律建 task），一并回填，
		// 保证右栏「人工/AI」来源标记不打乱。
		id:       "0007_session_origin",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
			`UPDATE sessions SET origin='human' WHERE mode='aux' AND origin=''`,
		},
	},
}

// createSchema 用迁移账本驱动建库：账本表自举，逐条执行未应用的迁移，
// 已应用的校验 checksum 防止历史被篡改（借鉴 ZCode 的 schema_migration 设计）。
func (s *Store) createSchema() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration (
			id TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("建迁移账本失败: %w", err)
	}
	for _, m := range storeMigrations {
		var sum string
		err := s.db.QueryRow(`SELECT checksum FROM schema_migration WHERE id=?`, m.id).Scan(&sum)
		switch {
		case err == nil:
			if sum != m.checksum() {
				return fmt.Errorf("迁移 %s 与账本 checksum 不一致：历史迁移被改动，拒绝启动", m.id)
			}
			continue
		case err != sql.ErrNoRows:
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		ok := true
		for _, q := range m.sql {
			if _, err := tx.Exec(q); err != nil {
				if m.tolerant && strings.Contains(err.Error(), "duplicate column") {
					continue // 旧库早已手工加过该列
				}
				ok = false
				_ = tx.Rollback()
				return fmt.Errorf("迁移 %s 失败: %w", m.id, err)
			}
		}
		if !ok {
			return nil
		}
		if _, err := tx.Exec(`INSERT INTO schema_migration (id, checksum, applied_at) VALUES (?,?,?)`,
			m.id, m.checksum(), now()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
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

// ---------- 全局附属数据（待办与定时任务清单，存 meta 表） ----------

// SetGlobalData 写入一条全局附属数据（key 自动加 globaldata: 前缀）。
func (s *Store) SetGlobalData(key, value string) error {
	return s.setMeta("globaldata:"+key, value)
}

// GetGlobalData 读取全局附属数据；不存在返回空串。
func (s *Store) GetGlobalData(key string) string {
	var v string
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k=?`, "globaldata:"+key).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// ---------- 会话附属数据（todo 清单等，存 meta 表） ----------

// SetSessionData 写入一条会话附属数据（key 自动加 sessdata: 前缀）。
func (s *Store) SetSessionData(sessionID, key, value string) error {
	return s.setMeta("sessdata:"+key+":"+sessionID, value)
}

// GetSessionData 读取会话附属数据；不存在返回空串。
func (s *Store) GetSessionData(sessionID, key string) string {
	var v string
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k=?`,
		"sessdata:"+key+":"+sessionID).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// ---------- 会话 ----------

const sessionCols = `id, title, mode, workspace, parent_id, provider, model, permission, plan_mode, goal_mode, tools, remote, archived, unread, status, created_at, updated_at, thinking, goal_text, goal_status, goal_rounds, usage_prompt, usage_cache_hit, usage_output, usage_calls, origin`

func (s *Store) Create(m *SessionMeta) error { return s.SaveMeta(m) }

func (s *Store) SaveMeta(m *SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 注意：下面的 UPDATE 刻意不写 status——它只由 UpdateStatus 维护（执行中/空闲）。
	// 否则 appendMessage 之类拿着陈旧内存对象回写，会把 running 冲成 idle，
	// 导致「正在执行」在侧栏与重启恢复里都不生效。
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
	_, err := s.db.Exec(`INSERT INTO sessions(`+sessionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, mode=excluded.mode,
		workspace=excluded.workspace, parent_id=excluded.parent_id, provider=excluded.provider,
		model=excluded.model, permission=excluded.permission, plan_mode=excluded.plan_mode,
		goal_mode=excluded.goal_mode, tools=excluded.tools, remote=excluded.remote,
		archived=excluded.archived, unread=excluded.unread,
		updated_at=excluded.updated_at, thinking=excluded.thinking,
		goal_text=excluded.goal_text, goal_status=excluded.goal_status, goal_rounds=excluded.goal_rounds,
		usage_prompt=excluded.usage_prompt, usage_cache_hit=excluded.usage_cache_hit,
		usage_output=excluded.usage_output, usage_calls=excluded.usage_calls, origin=excluded.origin`,
		m.ID, m.Title, m.Mode, m.Workspace, m.ParentID, m.Provider, m.Model,
		permOf(m), boolInt(m.PlanMode), boolInt(m.GoalMode), tools, remote,
		boolInt(m.Archived), boolInt(m.Unread), m.Status, m.CreatedAt, m.UpdatedAt, m.Thinking,
		m.GoalText, m.GoalStatus, m.GoalRounds, m.UsagePrompt, m.UsageCacheHit, m.UsageOutput, m.UsageCalls,
		m.Origin)
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
	var perm, tools, remote string
	var plan, goal, arch, unread int
	err := sc.Scan(&m.ID, &m.Title, &m.Mode, &m.Workspace, &m.ParentID,
		&m.Provider, &m.Model, &perm, &plan, &goal, &tools, &remote,
		&arch, &unread, &m.Status, &m.CreatedAt, &m.UpdatedAt, &m.Thinking,
		&m.GoalText, &m.GoalStatus, &m.GoalRounds, &m.UsagePrompt, &m.UsageCacheHit, &m.UsageOutput, &m.UsageCalls,
		&m.Origin)
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
		(session_id, seq, role, content, tool_calls, tool_call_id, kind, origin, compact, covers_up_to, superseded, created_at, thinking)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, sm.Seq, sm.Role, content, toolCalls, sm.ToolCallID, sm.Kind, sm.Origin,
		compact, sm.CoversUpTo, boolInt(sm.Superseded), sm.CreatedAt, sm.Thinking)
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

const messageCols = `id, session_id, seq, role, content, tool_calls, tool_call_id, kind, origin, compact, covers_up_to, superseded, created_at, thinking`

func scanMessage(sc interface{ Scan(dest ...any) error }) (*StoredMessage, error) {
	sm := &StoredMessage{}
	var content, toolCalls, compact, thinking sql.NullString
	var sup int
	err := sc.Scan(&sm.ID, &sm.SessionID, &sm.Seq, &sm.Role, &content, &toolCalls,
		&sm.ToolCallID, &sm.Kind, &sm.Origin, &compact, &sm.CoversUpTo, &sup, &sm.CreatedAt, &thinking)
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
	if thinking.Valid {
		sm.Thinking = thinking.String
	}
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

// MessagesWithSeq 同 Messages 的过滤规则，但保留 seq 等存储元信息，
// 供前端定位（搜索命中跳转滚动）使用。
func (s *Store) MessagesWithSeq(id string) ([]*StoredMessage, error) {
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*StoredMessage{}
	for rows.Next() {
		sm, err := scanMessage(rows)
		if err != nil {
			continue
		}
		if sm.Kind == "summary" {
			continue // 摘要只在组装上下文时按管线注入，不进入普通消息流
		}
		out = append(out, sm)
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

// LastSeq 返回会话当前的最后一个 seq（无消息返回 0）。
// 上下文继承要一次性算好新 seq 再落库，需要这个基线。
func (s *Store) LastSeq(id string) (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM messages WHERE session_id=?`, id).Scan(&n)
	return n, err
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
