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
	"sort"
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
	{
		// 文件快照（checkpoint）与交付成果（delivery）：对齐 ZCode 的 workspace
		// checkpoint 机制。checkpoint 记录一次文件改动前的内容（write /
		// edit 自动记录，snapshot_file 由模型在不可追踪修改前手动记录），
		// 编辑历史消息回退或按轮撤销时据此恢复；delivery 是模型调用
		// deliver_result 登记的最终交付文件清单，前端展示并提供下载。
		id: "0008_snapshots",
		sql: []string{
			`CREATE TABLE IF NOT EXISTS checkpoints (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
				seq INTEGER NOT NULL,
				tool_call_id TEXT NOT NULL DEFAULT '',
				tool_name TEXT NOT NULL DEFAULT '',
				path TEXT NOT NULL,
				existed_before INTEGER NOT NULL DEFAULT 1,
				before_content BLOB,
				after_hash TEXT NOT NULL DEFAULT '',
				after_size INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_checkpoints_session ON checkpoints(session_id, seq)`,
			`CREATE TABLE IF NOT EXISTS deliveries (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
				seq INTEGER NOT NULL,
				path TEXT NOT NULL,
				note TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_deliveries_session ON deliveries(session_id, seq)`,
		},
	},
	{
		// 快照正文改为落盘到工作区 .jxai/snapshots/（用户可见、可随工作区整体
		// 迁移，也避免大快照撑大 SQLite）；库里只留元数据与大小（before_size）。
		// 旧库里已存在的 before_content BLOB 继续兼容读取。
		id:       "0009_snapshot_before_size",
		tolerant: true,
		sql: []string{
			`ALTER TABLE checkpoints ADD COLUMN before_size INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		// 派发关系语义修正：数字助理派发的是顶层工作任务，不是挂在主对话下的
		// 子对话。派发来源记到 dispatched_by（回报链：中断回报 / list_sessions
		// 的「我派生的」标记），parent_id 只留给人工建的子对话。
		// 历史数据里被错挂的派生任务（parent_id 指向助理主对话、origin 非 human）
		// 一并搬到正确语义下。
		id:       "0010_dispatch_toplevel",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN dispatched_by TEXT NOT NULL DEFAULT ''`,
			`UPDATE sessions SET dispatched_by = parent_id, parent_id = '' ` +
				`WHERE parent_id != '' AND origin != 'human' ` +
				`AND parent_id IN (SELECT id FROM sessions WHERE mode = 'assistant')`,
		},
	},
	{
		// 子对话专家名：dispatch_sub 按专家预设派发时记下名字，系统提示词
		// 构建时按名解析追加（见 experts.go / agent.go 的 systemPrompt）。
		id:       "0011_session_expert",
		tolerant: true,
		sql: []string{
			`ALTER TABLE sessions ADD COLUMN expert TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		// 问题反馈（feedback.go）：客户端一键上报的问题/建议，只进不出——
		// 没有查看接口，排查时直接查库。diagnostics 存客户端附带的上下文 JSON 原文。
		id:       "0012_feedback",
		tolerant: true,
		sql: []string{
			`CREATE TABLE IF NOT EXISTS feedback (
				id          TEXT PRIMARY KEY,
				kind        TEXT NOT NULL DEFAULT 'other',
				content     TEXT NOT NULL,
				session_id  TEXT NOT NULL DEFAULT '',
				diagnostics TEXT NOT NULL DEFAULT '',
				version     TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL
			)`,
		},
	},
	{
		// 反馈补标题列（对齐工单结构：标题+正文+图片；图片存文件系统，
		// 文件名约定 <反馈id>-<序号>.<ext>，见 feedback.go）。
		id:       "0013_feedback_title",
		tolerant: true,
		sql: []string{
			`ALTER TABLE feedback ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		// 桌面端上传登记 + 会话后交付（deliver.go）：uploads 只登记带原件
		// 真实路径的桌面上传（uid 供模型在 deliver_file 里引用，原件路径即
		// 交付目标）；delivery_jobs 是模型登记、本轮结束后由系统执行的搬运
		// 队列（快照备份→原子替换→移动语义）。
		id:       "0014_upload_delivery",
		tolerant: true,
		sql: []string{
			`CREATE TABLE IF NOT EXISTS uploads (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id  TEXT NOT NULL,
				uid         TEXT NOT NULL,
				orig_path   TEXT NOT NULL DEFAULT '',
				copy_path   TEXT NOT NULL DEFAULT '',
				name        TEXT NOT NULL DEFAULT '',
				size        INTEGER NOT NULL DEFAULT 0,
				created_at  TEXT NOT NULL
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_uploads_session_uid ON uploads(session_id, uid)`,
			`CREATE TABLE IF NOT EXISTS delivery_jobs (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id  TEXT NOT NULL,
				source_path TEXT NOT NULL,
				target_dir  TEXT NOT NULL,
				target_name TEXT NOT NULL,
				backup_path TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'pending',
				error       TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL,
				executed_at TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_jobs_session ON delivery_jobs(session_id, status)`,
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
					_ = s.AppendRow(id, &StoredMessage{Message: m, Seq: seq, Kind: kindChat, CreatedAt: meta.CreatedAt})
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

const sessionCols = `id, title, mode, workspace, parent_id, provider, model, permission, plan_mode, goal_mode, tools, remote, archived, unread, status, created_at, updated_at, thinking, goal_text, goal_status, goal_rounds, usage_prompt, usage_cache_hit, usage_output, usage_calls, origin, dispatched_by, expert`

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
		// SSH 密码落盘前 DPAPI 加密（副本上做，内存对象保持明文供连接使用）
		rr := *m.Remote
		if rr.Password != "" {
			if enc, err := protectSecret(rr.Password); err == nil {
				rr.Password = enc
			}
		}
		if b, err := json.Marshal(&rr); err == nil {
			remote = string(b)
		}
	}
	_, err := s.db.Exec(`INSERT INTO sessions(`+sessionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, mode=excluded.mode,
		workspace=excluded.workspace, parent_id=excluded.parent_id, provider=excluded.provider,
		model=excluded.model, permission=excluded.permission, plan_mode=excluded.plan_mode,
		goal_mode=excluded.goal_mode, tools=excluded.tools, remote=excluded.remote,
		archived=excluded.archived, unread=excluded.unread,
		updated_at=excluded.updated_at, thinking=excluded.thinking,
		goal_text=excluded.goal_text, goal_status=excluded.goal_status, goal_rounds=excluded.goal_rounds,
		usage_prompt=excluded.usage_prompt, usage_cache_hit=excluded.usage_cache_hit,
		usage_output=excluded.usage_output, usage_calls=excluded.usage_calls, origin=excluded.origin,
		dispatched_by=excluded.dispatched_by, expert=excluded.expert`,
		m.ID, m.Title, m.Mode, m.Workspace, m.ParentID, m.Provider, m.Model,
		permOf(m), boolInt(m.PlanMode), boolInt(m.GoalMode), tools, remote,
		boolInt(m.Archived), boolInt(m.Unread), m.Status, m.CreatedAt, m.UpdatedAt, m.Thinking,
		m.GoalText, m.GoalStatus, m.GoalRounds, m.UsagePrompt, m.UsageCacheHit, m.UsageOutput, m.UsageCalls,
		m.Origin, m.DispatchedBy, m.Expert)
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
		&m.Origin, &m.DispatchedBy, &m.Expert)
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
			r.Password = unprotectSecret(r.Password) // 透明解密 DPAPI 密文
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

// RepointWorkspace 把显式挂在 old 目录下的会话改指 new（默认工作区改名迁移用：
// 旧版的默认工作区是「安装目录\工作区」，改名成「默认工作区」后，挂过旧路径的
// 会话要跟着走，否则会指向一个已不存在的文件夹）。未单独设过工作目录的会话存
// 的是空串、本来就跟随默认工作区，无需处理。返回改动的会话数。
// 比较忽略大小写（Windows 盘符/目录名不敏感）。
func (s *Store) RepointWorkspace(old, new string) (int, error) {
	old, new = strings.TrimSpace(old), strings.TrimSpace(new)
	if old == "" || new == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE sessions SET workspace=? WHERE workspace=? COLLATE NOCASE`, new, old)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
	return s.AppendRow(id, &StoredMessage{Message: m, Kind: kindChat, CreatedAt: now()})
}

// AppendRow 追加一条完整消息记录；Seq 为 0 时自动分配。
func (s *Store) AppendRow(id string, sm *StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendRowLocked(id, sm)
}

// keepRequests 是每会话保留的模型请求快照行（kind=request）条数：最近
// 三轮足够观察相邻请求间的变化与缓存影响；全量上下文体积大，逐条留档会
// 让库无界增长。
const keepRequests = 3

// AppendRequestRow 追加一条模型输入快照行（kind=request），写入后裁掉
// 更早的行，每会话只保留最近 keepRequests 条（环形）。
func (s *Store) AppendRequestRow(id string, sm *StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendRowLocked(id, sm); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM messages WHERE session_id=? AND kind='request' AND seq NOT IN
		(SELECT seq FROM messages WHERE session_id=? AND kind='request' ORDER BY seq DESC LIMIT ?)`,
		id, id, keepRequests)
	return err
}

// RecentRequestRows 取最近 n 条模型输入快照行（旧→新返回）；不足 n 条给实际数量。
func (s *Store) RecentRequestRows(id string, n int) ([]*StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ` + messageCols + ` FROM messages WHERE session_id=? AND kind='request' ORDER BY seq DESC LIMIT ?`, id, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StoredMessage
	for rows.Next() {
		sm, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *Store) appendRowLocked(id string, sm *StoredMessage) error {
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
	rows, err := s.db.Query(`SELECT `+messageCols+` FROM messages WHERE session_id=? ORDER BY seq`, id)
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

// EditUserMessage 编辑重发的原子落地（语义对齐 ZCode 的 editUserQuery：同会话
// 截断后重发）：事务内删除 seq 之后的全部记录并改写该条用户消息正文。
// 返回被移除的条数，供前端确认提示用。
func (s *Store) EditUserMessage(sessionID string, seq int64, content string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=? AND seq>?`, sessionID, seq).Scan(&removed); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	res, err := tx.Exec(`UPDATE messages SET content=? WHERE session_id=? AND seq=?`, content, sessionID, seq)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return 0, fmt.Errorf("会话 %s 里不存在 seq=%d 的消息", sessionID, seq)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, now(), sessionID); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	// 编辑点之后的文件快照与交付登记一并清除：它们锚定的消息行已不存在，
	// 留着会让下次回退拿到错误的状态基线
	if _, err := tx.Exec(`DELETE FROM checkpoints WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM deliveries WHERE session_id=? AND seq>?`, sessionID, seq); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	return removed, tx.Commit()
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

/* ---------- 文件快照与交付成果 ---------- */

// Checkpoint 是一条文件改动快照：改动发生前该文件的内容。path 为
// 工作区相对路径（脱离工作区的绝对写入存原样绝对路径）；after_hash 供回退前
// 校验"文件自这次改动后没被外部改过"。before 正文落盘在工作区
// .jxai/snapshots/<会话id>/<checkpoint id>.bin（BeforeSize 记字节数）；
// 旧库内联的 before_content BLOB 继续兼容。
type Checkpoint struct {
	ID            int64  `json:"id"`
	SessionID     string `json:"sessionId"`
	Seq           int64  `json:"seq"`
	ToolCallID    string `json:"-"`
	ToolName      string `json:"toolName"`
	Path          string `json:"path"`
	ExistedBefore bool   `json:"existedBefore"`
	Before        []byte `json:"-"`
	BeforeSize    int64  `json:"beforeSize"`
	AfterHash     string `json:"-"`
	AfterSize     int64  `json:"afterSize"`
	CreatedAt     string `json:"createdAt"`
}

// Delivery 是一条交付登记：模型调用 deliver_result 记下的最终成果文件。
type Delivery struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId"`
	Seq       int64  `json:"seq"`
	Path      string `json:"path"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
}

const checkpointCols = `id, session_id, seq, tool_call_id, tool_name, path, existed_before, before_content, after_hash, after_size, created_at, before_size`

// checkpointSizeExpr 是一条快照的体积口径：新库用 before_size（正文已落盘），
// 旧库内联 BLOB 用 LENGTH(before_content) 兜底。
const checkpointSizeExpr = `COALESCE(NULLIF(before_size,0), LENGTH(before_content))`

func scanCheckpoint(sc interface{ Scan(dest ...any) error }) (*Checkpoint, error) {
	cp := &Checkpoint{}
	var existed int
	err := sc.Scan(&cp.ID, &cp.SessionID, &cp.Seq, &cp.ToolCallID, &cp.ToolName, &cp.Path,
		&existed, &cp.Before, &cp.AfterHash, &cp.AfterSize, &cp.CreatedAt, &cp.BeforeSize)
	if err != nil {
		return nil, err
	}
	cp.ExistedBefore = existed == 1
	return cp, nil
}

// AppendCheckpoint 落一条快照（写失败只影响回退能力，调用方按 best-effort 处理）。
// 返回新行 id——正文文件按 id 命名（.jxai/snapshots/<会话>/<id>.bin）。
func (s *Store) AppendCheckpoint(cp *Checkpoint) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`INSERT INTO checkpoints
		(session_id, seq, tool_call_id, tool_name, path, existed_before, before_content, after_hash, after_size, created_at, before_size)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		cp.SessionID, cp.Seq, cp.ToolCallID, cp.ToolName, cp.Path, boolInt(cp.ExistedBefore),
		cp.Before, cp.AfterHash, cp.AfterSize, cp.CreatedAt, cp.BeforeSize)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CheckpointsAfter 取 seq 之后（不含）的全部快照，按落库顺序（即改动发生顺序）。
// 这是「编辑历史消息回退」的取数范围。
func (s *Store) CheckpointsAfter(sessionID string, seq int64) ([]*Checkpoint, error) {
	rows, err := s.db.Query(`SELECT `+checkpointCols+` FROM checkpoints
		WHERE session_id=? AND seq>? ORDER BY id`, sessionID, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Checkpoint{}
	for rows.Next() {
		if cp, err := scanCheckpoint(rows); err == nil {
			out = append(out, cp)
		}
	}
	return out, rows.Err()
}

// CheckpointsIn 取指定 seq 集合内的快照（「按轮撤销」的取数范围）。
func (s *Store) CheckpointsIn(sessionID string, seqs []int64) ([]*Checkpoint, error) {
	if len(seqs) == 0 {
		return []*Checkpoint{}, nil
	}
	args := []any{sessionID}
	marks := []string{}
	for _, q := range seqs {
		marks = append(marks, "?")
		args = append(args, q)
	}
	rows, err := s.db.Query(`SELECT `+checkpointCols+` FROM checkpoints
		WHERE session_id=? AND seq IN (`+strings.Join(marks, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Checkpoint{}
	for rows.Next() {
		if cp, err := scanCheckpoint(rows); err == nil {
			out = append(out, cp)
		}
	}
	return out, rows.Err()
}

// DeleteCheckpoints 删除指定 id 的快照（回退成功后调用，锚定消息未变但状态已还原）。
func (s *Store) DeleteCheckpoints(sessionID string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteCheckpointsLocked(sessionID, ids)
}

// deleteCheckpointsLocked 是 DeleteCheckpoints 的持锁版本（已在 s.mu 内的调用方用）。
func (s *Store) deleteCheckpointsLocked(sessionID string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{sessionID}
	marks := []string{}
	for _, id := range ids {
		marks = append(marks, "?")
		args = append(args, id)
	}
	_, err := s.db.Exec(`DELETE FROM checkpoints WHERE session_id=? AND id IN (`+strings.Join(marks, ",")+`)`, args...)
	return err
}

// PruneCheckpointsOlderThan 清理会话内超龄快照（保留策略：按天），返回被删的
// 行 id（正文文件按 id 落盘，调用方据此同步删文件）。按文件原子清理：一个
// 文件的全部快照都早于截止时间才整体删除——只删一半会让回退拿到"中间状态"
// 的错误基线（该删新建却恢复成中间内容）。
func (s *Store) PruneCheckpointsOlderThan(sessionID, cutoff string) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id FROM checkpoints WHERE session_id=? AND NOT EXISTS (
		SELECT 1 FROM checkpoints c2
		WHERE c2.session_id = checkpoints.session_id AND c2.path = checkpoints.path
		  AND c2.created_at >= ?)`, sessionID, cutoff)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := s.deleteCheckpointsLocked(sessionID, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// snapshotBytesOf 统计会话内指定池（手动 / 自动）的快照字节数。
func (s *Store) snapshotBytesOf(sessionID string, manual bool) (int64, error) {
	q := `SELECT COALESCE(SUM(` + checkpointSizeExpr + `),0) FROM checkpoints WHERE session_id=? AND tool_name`
	if manual {
		q += `=?`
	} else {
		q += `<>?`
	}
	var n int64
	err := s.db.QueryRow(q, sessionID, manualSnapshotTool).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// EvictCheckpointsOver 把两个池（自动快照 / 手动快照）的总量分别压到预算内，
// 返回被删的行 id（供正文文件同步删除）。按文件原子驱逐：从最久未动的文件
// 开始，一旦驱逐就把该文件的全部快照删掉（不能删一半）；某文件在某池没有
// 足迹就不会因该池超限被驱逐。预算传负数表示该池不限量。
func (s *Store) EvictCheckpointsOver(sessionID string, maxAuto, maxManual int64) ([]int64, error) {
	type row struct {
		id     int64
		path   string
		sz     int64
		manual bool
	}
	rows, err := s.db.Query(`SELECT id, path, `+checkpointSizeExpr+`, tool_name
		FROM checkpoints WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	type group struct {
		ids            []int64
		autoB, manualB int64
		last           int64 // 最新一条的 id：驱逐从最久未动的文件开始
	}
	order := []string{}
	byPath := map[string]*group{}
	for rows.Next() {
		var r row
		var tool string
		if err := rows.Scan(&r.id, &r.path, &r.sz, &tool); err != nil {
			continue
		}
		r.manual = tool == manualSnapshotTool
		g, ok := byPath[r.path]
		if !ok {
			g = &group{}
			byPath[r.path] = g
			order = append(order, r.path)
		}
		g.ids = append(g.ids, r.id)
		g.last = r.id
		if r.manual {
			g.manualB += r.sz
		} else {
			g.autoB += r.sz
		}
	}
	rows.Close()
	ids := []int64{}
	evict := func(manualPool bool, budget int64) {
		if budget < 0 {
			return // 该池不限量
		}
		total := int64(0)
		for _, g := range byPath {
			if manualPool {
				total += g.manualB
			} else {
				total += g.autoB
			}
		}
		if total <= budget {
			return
		}
		paths := append([]string{}, order...)
		sort.Slice(paths, func(i, j int) bool { return byPath[paths[i]].last < byPath[paths[j]].last })
		for _, p := range paths {
			g := byPath[p]
			footprint := g.autoB
			if manualPool {
				footprint = g.manualB
			}
			if footprint <= 0 {
				continue
			}
			total -= footprint
			ids = append(ids, g.ids...)
			if total <= budget {
				break
			}
		}
	}
	evict(false, maxAuto)
	evict(true, maxManual)
	if err := s.DeleteCheckpoints(sessionID, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// CheckpointsByIDs 按行 id 取快照（全字段，含 AfterHash / BeforeSize），
// 供「单文件撤回」在成功后精确清理这几条快照。找不到的行直接略过。
func (s *Store) CheckpointsByIDs(sessionID string, ids []int64) ([]*Checkpoint, error) {
	if len(ids) == 0 {
		return []*Checkpoint{}, nil
	}
	args := []any{sessionID}
	marks := []string{}
	for _, id := range ids {
		marks = append(marks, "?")
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT `+checkpointCols+` FROM checkpoints
		WHERE session_id=? AND id IN (`+strings.Join(marks, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Checkpoint{}
	for rows.Next() {
		if cp, err := scanCheckpoint(rows); err == nil {
			out = append(out, cp)
		}
	}
	return out, rows.Err()
}

// CheckpointsForPath 取某文件在本会话的全部快照（按落库顺序 = 改动发生顺序）。
// 这是文件改动面板行内「历史版本」的数据源：path 用工作区相对路径（与
// Checkpoint.Path 同一口径）。含手动快照（snapshot_file）与自动快照。
func (s *Store) CheckpointsForPath(sessionID, path string) ([]*Checkpoint, error) {
	rows, err := s.db.Query(`SELECT `+checkpointCols+` FROM checkpoints
		WHERE session_id=? AND path=? ORDER BY id`, sessionID, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Checkpoint{}
	for rows.Next() {
		if cp, err := scanCheckpoint(rows); err == nil {
			out = append(out, cp)
		}
	}
	return out, rows.Err()
}

// AddDelivery 落一条交付登记。
func (s *Store) AddDelivery(d *Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO deliveries (session_id, seq, path, note, created_at)
		VALUES(?,?,?,?,?)`, d.SessionID, d.Seq, d.Path, d.Note, d.CreatedAt)
	return err
}

// DeliveriesFor 返回会话全部交付登记（按落库顺序），供前端分组到对应回合。
func (s *Store) DeliveriesFor(sessionID string) ([]*Delivery, error) {
	rows, err := s.db.Query(`SELECT id, session_id, seq, path, note, created_at
		FROM deliveries WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.SessionID, &d.Seq, &d.Path, &d.Note, &d.CreatedAt); err == nil {
			out = append(out, &d)
		}
	}
	return out, rows.Err()
}

// CopySnapshots 把源会话的快照与交付登记按 seq 映射复制到新会话（分支对话用）。
// 快照正文文件由调用方按返回的配对（源 id → 新 id）另行复制（两个会话可能
// 挂不同工作区，落盘位置不同；同工作区时也在不同的 <会话id>/ 子目录下）。
func (s *Store) CopySnapshots(fromID, toID string, seqMap map[int64]int64) ([][2]int64, error) {
	cps, err := s.CheckpointsAfter(fromID, 0) // seq 从 1 起，>0 即全部
	if err != nil {
		return nil, err
	}
	pairs := [][2]int64{}
	for _, cp := range cps {
		nq, ok := seqMap[cp.Seq]
		if !ok {
			continue
		}
		nc := *cp
		nc.ID, nc.SessionID, nc.Seq = 0, toID, nq
		// 新行的正文文件尚未写入：先把元数据里带的内联 BLOB（旧数据）清掉，
		// 走统一的落盘复制路径；BeforeSize 保留给预算口径
		nc.Before = nil
		id, err := s.AppendCheckpoint(&nc)
		if err != nil {
			return pairs, err
		}
		pairs = append(pairs, [2]int64{cp.ID, id})
	}
	dvs, err := s.DeliveriesFor(fromID)
	if err != nil {
		return pairs, err
	}
	for _, d := range dvs {
		nq, ok := seqMap[d.Seq]
		if !ok {
			continue
		}
		nd := *d
		nd.ID, nd.SessionID, nd.Seq = 0, toID, nq
		if err := s.AddDelivery(&nd); err != nil {
			return pairs, err
		}
	}
	return pairs, nil
}

// Upload 是一条桌面上传登记：原件真实路径只在桌面端原生文件选择框里采集到
// （浏览器给不出本机路径），orig_path 既是模型的只读参考，也是 deliver_file
// 的交付目标来源。uid（"u1"/"u2"...）会话内唯一，分支继承时原样保留，
// 历史注记里的编号在新会话里继续有效。
type Upload struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId"`
	UID       string `json:"uid"`
	OrigPath  string `json:"origPath"`
	CopyPath  string `json:"copyPath"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
}

const uploadCols = `id, session_id, uid, orig_path, copy_path, name, size, created_at`

func scanUpload(sc interface{ Scan(dest ...any) error }) (*Upload, error) {
	u := &Upload{}
	if err := sc.Scan(&u.ID, &u.SessionID, &u.UID, &u.OrigPath, &u.CopyPath, &u.Name, &u.Size, &u.CreatedAt); err != nil {
		return nil, err
	}
	return u, nil
}

// AddUpload 登记一条上传；uid 留空时按会话内自增分配（u1、u2…）。
func (s *Store) AddUpload(u *Upload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.UID == "" {
		var n int64
		if err := s.db.QueryRow(`SELECT COALESCE(MAX(CAST(SUBSTR(uid, 2) AS INTEGER)), 0)
			FROM uploads WHERE session_id=?`, u.SessionID).Scan(&n); err != nil {
			return err
		}
		u.UID = fmt.Sprintf("u%d", n+1)
	}
	res, err := s.db.Exec(`INSERT INTO uploads (session_id, uid, orig_path, copy_path, name, size, created_at)
		VALUES(?,?,?,?,?,?,?)`, u.SessionID, u.UID, u.OrigPath, u.CopyPath, u.Name, u.Size, u.CreatedAt)
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

// UploadsFor 返回会话全部上传登记（按登记顺序）。
func (s *Store) UploadsFor(sessionID string) ([]*Upload, error) {
	rows, err := s.db.Query(`SELECT `+uploadCols+` FROM uploads WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Upload{}
	for rows.Next() {
		if u, err := scanUpload(rows); err == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// CopyUploadsOnBranch 分支对话用：把源会话的上传登记原样（uid 不变）复制到
// 新会话，历史注记里的 u 编号在新会话里继续可解析。
func (s *Store) CopyUploadsOnBranch(fromID, toID string) error {
	ups, err := s.UploadsFor(fromID)
	if err != nil {
		return err
	}
	for _, u := range ups {
		nu := *u
		nu.ID, nu.SessionID = 0, toID
		if err := s.AddUpload(&nu); err != nil {
			return err
		}
	}
	return nil
}

// DeleteUploadsForSession 删除会话时清掉它的上传登记。
func (s *Store) DeleteUploadsForSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM uploads WHERE session_id=?`, sessionID)
	return err
}

// DeliveryJob 是一条登记在册、等待本轮结束后执行的交付搬运：
// 把 source_path 移动到 target_dir/target_name，目标处若有将被替代的
// 同名文件先快照备份（backup_path 记录备份去处）。
type DeliveryJob struct {
	ID         int64  `json:"id"`
	SessionID  string `json:"sessionId"`
	SourcePath string `json:"sourcePath"`
	TargetDir  string `json:"targetDir"`
	TargetName string `json:"targetName"`
	BackupPath string `json:"backupPath"`
	Status     string `json:"status"` // pending | running | done | failed
	Error      string `json:"error"`
	CreatedAt  string `json:"createdAt"`
	ExecutedAt string `json:"executedAt"`
}

const deliveryJobCols = `id, session_id, source_path, target_dir, target_name, backup_path, status, error, created_at, executed_at`

func scanDeliveryJob(sc interface{ Scan(dest ...any) error }) (*DeliveryJob, error) {
	j := &DeliveryJob{}
	if err := sc.Scan(&j.ID, &j.SessionID, &j.SourcePath, &j.TargetDir, &j.TargetName,
		&j.BackupPath, &j.Status, &j.Error, &j.CreatedAt, &j.ExecutedAt); err != nil {
		return nil, err
	}
	return j, nil
}

// AddDeliveryJob 登记一条待执行交付。同一会话里目标位置（target_dir +
// target_name）相同的 pending 作业只保留一条：后登记的覆盖先登记的源文件，
// 避免模型反复调整后同一目标被搬两次。
func (s *Store) AddDeliveryJob(j *DeliveryJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int64
	err := s.db.QueryRow(`SELECT id FROM delivery_jobs
		WHERE session_id=? AND status='pending' AND target_dir=? AND target_name=?`,
		j.SessionID, j.TargetDir, j.TargetName).Scan(&id)
	switch {
	case err == nil:
		_, err = s.db.Exec(`UPDATE delivery_jobs SET source_path=?, created_at=? WHERE id=?`,
			j.SourcePath, j.CreatedAt, id)
		j.ID = id
		return err
	case err != sql.ErrNoRows:
		return err
	}
	res, err := s.db.Exec(`INSERT INTO delivery_jobs
		(session_id, source_path, target_dir, target_name, status, created_at)
		VALUES(?,?,?,?, 'pending', ?)`, j.SessionID, j.SourcePath, j.TargetDir, j.TargetName, j.CreatedAt)
	if err != nil {
		return err
	}
	j.ID, _ = res.LastInsertId()
	j.Status = "pending"
	return nil
}

// ClaimPendingJobs 原子认领待执行交付（pending→running）并返回认领到的作业。
// sessionID 为空串时认领全部会话的（启动恢复兜底用）。
func (s *Store) ClaimPendingJobs(sessionID string) ([]*DeliveryJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `SELECT ` + deliveryJobCols + ` FROM delivery_jobs WHERE status='pending'`
	var args []any
	if sessionID != "" {
		q += ` AND session_id=?`
		args = append(args, sessionID)
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	out := []*DeliveryJob{}
	for rows.Next() {
		if j, err := scanDeliveryJob(rows); err == nil {
			out = append(out, j)
		}
	}
	rows.Close()
	for _, j := range out {
		if _, err := s.db.Exec(`UPDATE delivery_jobs SET status='running' WHERE id=? AND status='pending'`, j.ID); err != nil {
			return out, err
		}
		j.Status = "running"
	}
	return out, nil
}

// UpdateDeliveryJob 回写执行结果（状态/备份去处/错误/完成时间）。
func (s *Store) UpdateDeliveryJob(j *DeliveryJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE delivery_jobs SET backup_path=?, status=?, error=?, executed_at=? WHERE id=?`,
		j.BackupPath, j.Status, j.Error, j.ExecutedAt, j.ID)
	return err
}

// ResetStuckJobs 启动恢复：上一进程崩溃时可能停在 running，复位回 pending
// 等待统一重放。返回复位条数。
func (s *Store) ResetStuckJobs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE delivery_jobs SET status='pending' WHERE status='running'`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// PendingDeliveryCount 返回会话待执行交付条数（工具回执里给模型一个队列概览）。
func (s *Store) PendingDeliveryCount(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM delivery_jobs WHERE session_id=? AND status='pending'`, sessionID).Scan(&n)
	return n
}

// DeletePendingJobsForSession 删除会话时清掉它的待执行交付（用户删会话即放弃）。
func (s *Store) DeletePendingJobsForSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM delivery_jobs WHERE session_id=? AND status IN ('pending','running')`, sessionID)
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
