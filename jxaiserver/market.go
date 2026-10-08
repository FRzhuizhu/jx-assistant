// 技能/组件/插件市场（服务端半边）：清单 + zip 存储与发布 CLI。
//
// 数据布局（数据目录下）：
//
//	market.json                    清单（原子写；GET /market/manifest.json 下发）
//	items/<type>/<id>/<ver>.zip    包体（GET /market/items/... 下载；同一 id 只保留最新版）
//
// 发布走子命令（服务器上经 SSH 使用，市场本身匿名只读——公司内部够用）：
//
//	jxai-server publish   skill|component|plugin <目录|zip> [-id -name -version -desc -tags -data]
//	jxai-server unpublish skill|component|plugin <id> [-data]
//
// 发布校验（三类型各自的最低门槛）：
//
//	skill     根（或唯一子目录）含 SKILL.md
//	component 含 manifest.json 且其 client.js/client.css 指到的文件真实存在；
//	          manifest 声明 host:true（带宿主半边）的拒收——市场只收纯浏览器组件
//	plugin    含 .claude-plugin/plugin.json 或根 plugin.json，且 name 非空
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var marketTypes = map[string]bool{"skill": true, "component": true, "plugin": true}

type marketEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Zip         string   `json:"zip"`    // 相对清单的下载路径（/market/items/...）
	SHA256      string   `json:"sha256"` // zip 的 SHA-256（十六进制）
	Size        int64    `json:"size"`
	UpdatedAt   string   `json:"updatedAt,omitempty"`
}

type marketManifest struct {
	Name       string        `json:"name"`
	UpdatedAt  string        `json:"updatedAt"`
	Skills     []marketEntry `json:"skills"`
	Components []marketEntry `json:"components"`
	Plugins    []marketEntry `json:"plugins"`
}

type marketStore struct {
	dir  string // 数据目录；manifest 在 dir/market.json，包在 dir/items/
	name string
	mu   sync.Mutex
}

func newMarketStore(dir, name string) *marketStore {
	return &marketStore{dir: dir, name: name}
}

func (s *marketStore) manifestPath() string { return filepath.Join(s.dir, "market.json") }
func (s *marketStore) itemsDir() string     { return filepath.Join(s.dir, "items") }

func (s *marketStore) load() marketManifest {
	m := marketManifest{Name: s.name}
	data, err := os.ReadFile(s.manifestPath())
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	if m.Name == "" {
		m.Name = s.name
	}
	return m
}

func (s *marketStore) save(m marketManifest) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	return writeFileAtomic(s.manifestPath(), mustJSONIndent(m), 0644)
}

func (s *marketStore) sliceOf(m *marketManifest, typ string) *[]marketEntry {
	switch typ {
	case "skill":
		return &m.Skills
	case "component":
		return &m.Components
	case "plugin":
		return &m.Plugins
	}
	return nil
}

func (m marketManifest) counts() map[string]int {
	return map[string]int{"skills": len(m.Skills), "components": len(m.Components), "plugins": len(m.Plugins)}
}

// counts 供 healthz 汇总市场条目数。
func (s *marketStore) counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load().counts()
}

/* ---------- HTTP ---------- */

func (s *marketStore) handleManifest(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	m := s.load()
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(mustJSONIndent(m))
}

// handleItem 下发包体：/market/items/<type>/<id>/<file>。逐段校验（防穿越），
// zip 文件名带版本、内容不可变，长缓存无害。
func (s *marketStore) handleItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/market/items/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `\/`) {
			http.NotFound(w, r)
			return
		}
	}
	if !strings.HasSuffix(parts[2], ".zip") || !marketTypes[parts[0]] {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	http.ServeFile(w, r, filepath.Join(s.itemsDir(), parts[0], parts[1], parts[2]))
}

/* ---------- 发布 ---------- */

type publishOpts struct {
	ID, Name, Version, Desc, Tags string
}

// publish 打包一个来源（目录或 zip）并登记为市场条目；返回登记后的条目。
func (s *marketStore) publish(typ, src string, o publishOpts) (*marketEntry, error) {
	if !marketTypes[typ] {
		return nil, fmt.Errorf("类型需为 skill / component / plugin")
	}
	root, cleanup, err := stageSource(src)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	id, name, version, desc := o.ID, o.Name, o.Version, o.Desc
	if err := validateAndDerive(typ, root, &id, &name, &version, &desc); err != nil {
		return nil, err
	}
	id = marketID(id)
	if id == "" {
		return nil, fmt.Errorf("推导不出可用 id（中文名会被清洗掉），请用 -id 指定纯小写字母/数字/中划线的 id")
	}
	if version == "" {
		version = "1.0.0"
	}

	zipBytes, err := zipTree(root)
	if err != nil {
		return nil, fmt.Errorf("打包 zip 失败: %w", err)
	}
	sum := sha256.Sum256(zipBytes)
	dst := filepath.Join(s.itemsDir(), typ, id, marketID(version)+".zip")
	if err := writeFileAtomic(dst, zipBytes, 0644); err != nil {
		return nil, fmt.Errorf("写包失败: %w", err)
	}

	entry := marketEntry{
		ID: id, Name: name, Version: version, Description: desc,
		Tags:   splitTags(o.Tags),
		Zip:    "/market/items/" + typ + "/" + id + "/" + marketID(version) + ".zip",
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(zipBytes)),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	list := s.sliceOf(&m, typ)
	replaced := false
	for i := range *list {
		if (*list)[i].ID == id {
			(*list)[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		*list = append(*list, entry)
	}
	sort.Slice(*list, func(i, j int) bool { return (*list)[i].ID < (*list)[j].ID })
	m.UpdatedAt = entry.UpdatedAt
	if err := s.save(m); err != nil {
		return nil, err
	}
	// 同 id 旧版本 zip 已不在清单里：清掉，存储不随版本累积
	oldDir := filepath.Join(s.itemsDir(), typ, id)
	keep := filepath.Base(dst)
	if entries, err := os.ReadDir(oldDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && e.Name() != keep {
				_ = os.Remove(filepath.Join(oldDir, e.Name()))
			}
		}
	}
	return &entry, nil
}

// unpublish 下架：清单去条目 + 删该 id 的包目录。
func (s *marketStore) unpublish(typ, id string) error {
	if !marketTypes[typ] {
		return fmt.Errorf("类型需为 skill / component / plugin")
	}
	id = marketID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	list := s.sliceOf(&m, typ)
	found := false
	out := (*list)[:0]
	for _, e := range *list {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return fmt.Errorf("%s 里没有条目 %s", typ, id)
	}
	*list = out
	if err := s.save(m); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.itemsDir(), typ, id))
}

// validateAndDerive 做三类型各自的门槛校验，并对未显式指定的字段做推导
// （id/name/version/desc 的来源随类型不同）。
func validateAndDerive(typ, root string, id, name, version, desc *string) error {
	switch typ {
	case "skill":
		if _, err := os.Stat(filepath.Join(root, "SKILL.md")); err != nil {
			return fmt.Errorf("根目录（或唯一子目录）里缺少 SKILL.md")
		}
		skillName, skillDesc := skillMetaLite(root)
		if *name == "" {
			*name = skillName
		}
		if *desc == "" {
			*desc = skillDesc
		}
		if *id == "" {
			*id = *name
		}
	case "component":
		data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
		if err != nil {
			return fmt.Errorf("缺少 manifest.json")
		}
		var m struct {
			ID, Name, Version, Description string
			Host                           bool
			Client                         struct{ JS, CSS string }
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("manifest.json 不是合法 JSON: %w", err)
		}
		if m.Host {
			return fmt.Errorf("该组件声明了宿主半边（host:true）：市场只收纯浏览器半边组件，带宿主的组件随主程序发版")
		}
		if m.Client.JS == "" && m.Client.CSS == "" {
			return fmt.Errorf("manifest.json 的 client.js / client.css 至少要有一个")
		}
		for _, f := range []string{m.Client.JS, m.Client.CSS} {
			if f != "" {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
					return fmt.Errorf("清单里声明的 %s 在包里不存在", f)
				}
			}
		}
		if *id == "" {
			*id = m.ID
		}
		if *name == "" {
			*name = m.Name
		}
		if *version == "" {
			*version = m.Version
		}
		if *desc == "" {
			*desc = m.Description
		}
	case "plugin":
		man, err := readPluginManifest(root)
		if err != nil {
			return err
		}
		if *id == "" {
			*id = man.Name
		}
		if *name == "" {
			*name = man.Name
		}
		if *version == "" {
			*version = man.Version
		}
		if *desc == "" {
			*desc = man.Description
		}
	}
	return nil
}

// readPluginManifest 读 Claude Code 插件清单（.claude-plugin/plugin.json 或根 plugin.json）。
func readPluginManifest(root string) (*struct{ Name, Version, Description string }, error) {
	manPath := filepath.Join(root, ".claude-plugin", "plugin.json")
	if _, err := os.Stat(manPath); err != nil {
		manPath = filepath.Join(root, "plugin.json")
	}
	data, err := os.ReadFile(manPath)
	if err != nil {
		return nil, fmt.Errorf("找不到 .claude-plugin/plugin.json（不是有效的 Claude Code 插件）")
	}
	var m struct{ Name, Version, Description string }
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("plugin.json 不是合法 JSON: %w", err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("plugin.json 缺少 name 字段")
	}
	return &m, nil
}

// skillMetaLite 从 SKILL.md 的 frontmatter 提取 name / description（与主程序
// skill_manager.go 的 skillMeta 同语义的最小实现；本包独立于主程序）。
func skillMetaLite(dir string) (name, description string) {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", ""
	}
	s := strings.TrimSpace(string(data))
	name = filepath.Base(dir)
	if strings.HasPrefix(s, "---") {
		if i := strings.Index(s[3:], "\n---"); i >= 0 {
			for _, line := range strings.Split(s[3:3+i], "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				}
			}
			return name, description
		}
	}
	return name, ""
}

// marketID 清洗条目 id / 版本（zip 路径段）：与主程序 ccplugin 的 ccSanitizeName
// 同语义（保留大小写、非法字符折成中划线）——插件条目 id 即 ccSanitizeName(plugin
// 名)，客户端安装键 name@marketplace 两端才能对上。中文名会被清洗掉，需 -id 指定。
func marketID(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		lastDash = false
	}
	return strings.Trim(b.String(), "-.")
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

/* ---------- 来源整理与打包（本包自备的最小实现） ---------- */

// stageSource 把来源（目录或 zip）物化成包根目录；zip 支持根有清单文件或只
// 多包一层目录两种形态（右键压缩文件夹的常见产物）。
func stageSource(src string) (root string, cleanup func(), err error) {
	src = strings.TrimSpace(src)
	info, statErr := os.Stat(src)
	if statErr != nil {
		return "", nil, fmt.Errorf("路径不存在: %s", src)
	}
	if info.IsDir() {
		// 唯一子目录是包根的形态（如压缩包解开多一层）：上浮一层再校验
		if r, ok := liftSingleSubdir(src); ok {
			return r, func() {}, nil
		}
		return src, func() {}, nil
	}
	if !strings.EqualFold(filepath.Ext(src), ".zip") {
		return "", nil, fmt.Errorf("只支持目录或 .zip 文件: %s", src)
	}
	tmp, err := os.MkdirTemp("", "jxmkt-")
	if err != nil {
		return "", nil, err
	}
	if err := unzipTo(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", nil, fmt.Errorf("解压失败: %w", err)
	}
	root = tmp
	if r, ok := liftSingleSubdir(tmp); ok {
		root = r
	}
	return root, func() { os.RemoveAll(tmp) }, nil
}

// liftSingleSubdir：目录里只有一个子目录、没有别的平级文件时把子目录当根。
// 发布校验随后仍会检查根上有没有 SKILL.md / manifest.json / plugin.json。
func liftSingleSubdir(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return "", false
	}
	return filepath.Join(dir, entries[0].Name()), true
}

// zipTree 把目录打成 zip（相对路径、正斜杠分隔），返回字节。
func zipTree(root string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unzipTo 解压 zip 到目录（条目路径逐段校验，防穿越）。
func unzipTo(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return fmt.Errorf("压缩包里的路径不安全: %s", f.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func mustJSONIndent(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

/* ---------- 子命令 ---------- */

// flagsFirst 把「位置参数在前、flag 在后」的命令行重排成 flag 包要求的
// 「flag 在前」（本 CLI 的 flag 都带值：-data 目录 这类 token 成对搬运）。
func flagsFirst(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, args[i])
	}
	return append(flags, pos...)
}

func runPublish(args []string) int {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	data := fs.String("data", "data", "数据目录")
	id := fs.String("id", "", "条目 id（缺省从包清单推导）")
	name := fs.String("name", "", "展示名")
	version := fs.String("version", "", "版本号")
	desc := fs.String("desc", "", "一句话描述")
	tags := fs.String("tags", "", "逗号分隔标签")
	_ = fs.Parse(flagsFirst(args))
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "用法: jxai-server publish <skill|component|plugin> <目录|zip> [-id -name -version -desc -tags -data]")
		return 2
	}
	s := newMarketStore(*data, "坚祥内部市场")
	entry, err := s.publish(rest[0], rest[1], publishOpts{ID: *id, Name: *name, Version: *version, Desc: *desc, Tags: *tags})
	if err != nil {
		fmt.Fprintln(os.Stderr, "发布失败:", err)
		return 1
	}
	fmt.Printf("已发布 %s %s（版本 %s，包 %s，%.1f KB）\n", rest[0], entry.ID, entry.Version, entry.Zip, float64(entry.Size)/1024)
	return 0
}

func runUnpublish(args []string) int {
	fs := flag.NewFlagSet("unpublish", flag.ExitOnError)
	data := fs.String("data", "data", "数据目录")
	_ = fs.Parse(flagsFirst(args))
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "用法: jxai-server unpublish <skill|component|plugin> <id> [-data]")
		return 2
	}
	s := newMarketStore(*data, "坚祥内部市场")
	if err := s.unpublish(rest[0], rest[1]); err != nil {
		fmt.Fprintln(os.Stderr, "下架失败:", err)
		return 1
	}
	fmt.Printf("已下架 %s %s\n", rest[0], marketID(rest[1]))
	return 0
}

/* ---------- 小工具 ---------- */

func writeErrJSON(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":` + string(mustJSONIndent(msg)) + `}`))
}

func writeJSONStr(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}
