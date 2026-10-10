/* 市场发布（把技能包发到市场服务器）：POST /api/market/publish

   市场服务器（jxai-server，另一仓库）的发布端点是 POST /admin/api/publish：
   multipart，字段 type / file（zip）/ id / name / version / desc / tags（逗号分隔），
   成功回 {entry:{id,version,...}}，失败非 2xx + {error}。

   本端点是它的客户端半边，做三件事：
     1. 收前端投喂的技能包——单个 .zip 原样透传；前端剥好顶层目录的一组文件在这里打 zip；
     2. 按「市场安装」的规矩校验并补全元信息（装得上才算合格）：
        · 包内 SKILL.md 必须在压缩包根部或其唯一子目录里（与 extractSkillZip 同规则，
          否则装到本机会被拒）；
        · **SKILL.md 得有正文**：空文件、只有 frontmatter、压缩数据损坏读不出的一律拒收——
          这种包在市场上装得上却用不了（技能列表没描述、read_skill 报「没有可用的说明正文」），
          属于「文件对上了但内容是空的」，必须在发布前拦掉；
        · id 要能被 sanitizeSkillID 接受，且不与自带技能重名；
        · name/description 优先取 SKILL.md 的 frontmatter（装好后技能列表显示的就是它），
          其次 manifest.json；
        · version 从文件名解析（v 后一定是版本号，v1.3.1_20261009 → 1.3.1），
          其次 manifest.json / SKILL.md 里的 version；
        · tags 只有前端给了才发（frontmatter 的 tags 也认）。
     3. 转发给市场服务器并回执（带包体 sha256/size），成功后清本机清单缓存，
        前端刷新即可看到新条目。

   ---------- 五个字段的来源（从哪一级取的，按先后顺序） ----------

   | 字段 | 第一来源（显式） | 包体推导（依次回退） |
   |---|---|---|
   | id（必填） | meta.id（前端输入框） | 包内 manifest.json 的 id → SKILL.md 所在目录名 → SKILL.md 的 name → 包名（zip 文件名/顶层目录名）→ 中文→英文词表（skillIDFromChinese，只在这一步兜底）；都不行就 400 要求手填 |
   | name | meta.name | manifest.json 的 name → SKILL.md 的 name → id |
   | version | meta.version | 包名里 v 后那串（v1.3.1_20261009 → 1.3.1）→ manifest.json 的 version → SKILL.md 的 version → "0.0.0" |
   | tags | meta.tags | SKILL.md 的 tags（frontmatter，逗号或 [] 分隔；manifest.json 无此字段） |
   | description | meta.description | SKILL.md 的 description → manifest.json 的 description |

   注：name/description 的取舍与「装好后技能列表显示什么」对齐——列表读的是 SKILL.md
   的 frontmatter（skill_manager.go 的 skillMeta），所以描述优先 SKILL.md；manifest.json
   的 name 多是友好名，故名称先试它。 */

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// marketPublishMaxZip 单个技能包上限（防误传大文件：包体在内存里组装/转发）。
const marketPublishMaxZip = 200 << 20

// marketPublishClient 转发发布请求用（大包 + 服务端打 zip，给足时间）。
var marketPublishClient = &http.Client{Timeout: 10 * time.Minute}

// marketOrigin 取市场清单地址的 origin（发布端点与清单同源）。
func marketOrigin(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("市场地址不合法: %s", rawURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

/* ---------- 名字 → 版本号 / 技能 id ---------- */

var (
	// versionAfterV：v/V 后面那串一定是版本号（v1.3.1_20261009 → 1.3.1）
	reVersionAfterV = regexp.MustCompile(`[vV](\d+(?:\.\d+){0,3})`)
	// versionBare：没有 v 前缀时的裸版本；后面的 +时间戳 / _时间戳 / -时间戳 不算版本本体
	// （RE2 没有否定前瞻，用「后一个字符不能是数字或点」来收尾）
	reVersionBare = regexp.MustCompile(`(?:^|[^0-9.])(\d+\.\d+(?:\.\d+){0,2})(?:[^0-9.]|$)`)
	// versionTail：id 里要去掉的版本尾巴（v1.3.1 / 1.3.1 及随后的 _20261009、+20261009）
	reVersionTail = regexp.MustCompile(`[._\-]?[vV]?\d+(?:\.\d+){1,3}(?:[+_\-]?\d{6,8})?$`)
	reTimestamp   = regexp.MustCompile(`[._\-]?\d{8}$`)
)

// skillVersionFromName 从文件名/目录名里取版本号：文件名里出现 v 时，v 后面一定是版本号；
// 版本后跟时间戳（1.3.1_20261009、v1.3.1+20261009）也只取版本本体。
func skillVersionFromName(name string) string {
	s := strings.TrimRight(strings.TrimSpace(name), `/\`)
	if i := strings.LastIndexAny(s, `\/`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, ".zip"), ".ZIP")
	if m := reVersionAfterV.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := reVersionBare.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// skillIDForMarket 把名字收拾成市场接受的技能 ID：只留小写字母/数字/中划线。
// 市场端对中文名会拒收（「推导不出可用 id（中文名会被清洗掉）」），所以推不出时返回空串，
// 交给候选链的下一个或让用户手填。
func skillIDForMarket(s string) string {
	t := reNonMarketID.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	t = strings.Trim(t, "-")
	return t
}

var reNonMarketID = regexp.MustCompile(`[^a-z0-9]+`)

// skillIDFromName 把包名/目录名收拾成能用的技能 id：去掉版本与时间戳尾巴、
// 去掉压缩包扩展名，再按 sanitizeSkillID 去非法字符。
func skillIDFromName(name string) string {
	return sanitizeSkillID(skillNameStem(name))
}

// skillNameStem 把包名/目录名收拾成「纯名字」：取最后一段、去 .zip、去掉版本与
// 时间戳尾巴（v1.3.1_20261009 / 1.3.1+20261009 / _20261009 都不算名字）。
func skillNameStem(name string) string {
	s := strings.TrimRight(strings.TrimSpace(name), `/\`)
	if i := strings.LastIndexAny(s, `\/`); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, ".zip"), ".ZIP")
	s = strings.TrimSpace(reVersionTail.ReplaceAllString(s, ""))
	s = strings.TrimSpace(reTimestamp.ReplaceAllString(s, ""))
	return strings.Trim(s, " _-·")
}

/* ---------- 中文名 → 英文 id（词表兜底） ---------- */

// 市场端只收纯小写字母/数字/中划线的 id，中文名会被清洗成空串、直接拒收；
// 而中文技能名（微信操作、设备点检…）在国内团队里是常态。这里给一份对照词表：
// 把中文名逐词换成英文词根再接起来（微信操作_技能包 → wechat-ops），让「必填的 id」
// 不用每次手填，也不至于每次现编一个不一致的名字。
//
// 规则（宁缺勿猜）：
//   - 词表键最长 4 字，匹配时从 4 字往 1 字回退（「企业微信」先于「微信」命中）；
//   - 值是 "" 的键是噪音词（技能/技能包/工具…），丢掉不影响语义；
//   - 只要有一个汉字没被词表覆盖就整体放弃（返回空串），继续走「请用户手填」——
//     猜错一半的 id 比让用户自己敲一个更糟（市场 id 是唯一键，改起来要重发）；
//   - 词表是「建议」不是事实：前端填进可编辑的 ID 框并标注来源，用户随时可改。
var skillIDWordList = map[string]string{
	// 噪音词（丢掉不影响语义）
	"技能包": "", "技能": "", "工具包": "", "工具": "", "插件": "", "组件": "",
	"的": "", "与": "", "和": "", "及": "", "之": "", "小": "",

	// 常见修饰词
	"自动": "auto", "自动化": "automation", "批量": "batch", "智能": "ai", "定时": "schedule",
	"快速": "fast", "简易": "easy", "通用": "common", "助手": "assistant", "工具集": "tools",

	// 沟通协作
	"微信": "wechat", "企业微信": "wecom", "钉钉": "dingtalk", "飞书": "feishu", "公众号": "mp",
	"小程序": "miniapp", "聊天": "chat", "群聊": "group", "群": "group", "会话": "session",
	"联系人": "contact", "消息": "message", "通知": "notify", "发送": "send", "接收": "recv",
	"回复": "reply", "记录": "record", "转发": "forward", "邮件": "mail", "邮箱": "mailbox",
	"短信": "sms", "电话": "phone", "会议": "meeting", "日程": "calendar", "提醒": "remind",

	// 文件与文档
	"文件": "file", "文件夹": "folder", "目录": "dir", "文档": "docs", "表格": "sheet",
	"幻灯片": "slides", "图片": "image", "照片": "photo", "图像": "image", "视频": "video",
	"音频": "audio", "语音": "voice", "文字": "text", "识别": "ocr", "导出": "export",
	"导入": "import", "备份": "backup", "恢复": "restore", "上传": "upload", "下载": "download",
	"压缩": "zip", "解压": "unzip", "合并": "merge", "拆分": "split", "转换": "convert",
	"重命名": "rename", "清理": "clean", "打印": "print", "扫描": "scan", "扫码": "scan",

	// 数据与图表
	"数据": "data", "分析": "analysis", "统计": "stat", "报表": "report", "报告": "report",
	"图表": "charts", "看板": "board", "大屏": "dashboard", "查询": "query", "汇总": "summary",
	"对比": "diff", "计算": "calc", "生成": "gen", "采集": "collect", "抓取": "crawl",
	"爬虫": "spider", "监控": "monitor", "日志": "log", "审计": "audit",

	// 业务（本厂常见）
	"设备": "device", "点检": "checkup", "巡检": "inspection", "保养": "maintenance",
	"操作": "ops", "运维": "ops", "处理": "process", "登记": "register", "入库": "import",
	"维修": "repair", "台账": "ledger", "库存": "stock", "订单": "order", "客户": "customer",
	"销售": "sales", "财务": "finance", "发票": "invoice", "合同": "contract", "考勤": "attendance",
	"日报": "daily", "周报": "weekly", "月报": "monthly", "项目": "project", "进度": "progress",
	"质量": "quality", "检验": "inspect", "审批": "approval", "工单": "ticket", "流程": "flow",
	"任务": "task", "签到": "checkin", "标签": "label", "模板": "template", "示例": "demo",

	// 技术
	"接口": "api", "数据库": "db", "服务器": "server", "网络": "network", "网页": "web",
	"网站": "site", "浏览器": "browser", "脚本": "script", "安装": "install", "卸载": "uninstall",
	"更新": "update", "升级": "upgrade", "版本": "version", "配置": "config", "设置": "config",
	"权限": "perm", "账号": "account", "登录": "login", "加密": "encrypt", "解密": "decrypt",
	"校验": "validate", "检查": "check", "同步": "sync", "迁移": "migrate", "排队": "queue",

	// 专有名词（本厂/产品）
	"精制": "jingzhi", "坚祥": "jx", "数字助理": "assistant", "管家": "keeper", "大师": "master",
}

// isHanRune 判断是不是汉字（词表未覆盖的汉字会让 skillIDFromChinese 整体放弃）。
func isHanRune(r rune) bool { return unicode.Is(unicode.Han, r) }

// skillIDFromChinese 用词表把中文名换成英文 id（微信操作_技能包_v1.3.1_20261009
// → wechat-ops）。词表没覆盖全就返回空串，交给「请用户手填」那一档。
func skillIDFromChinese(name string) string {
	s := skillNameStem(name)
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		hit := false
		for n := 4; n >= 1; n-- { // 从长到短匹配，企业微信优先于微信
			if i+n > len(r) {
				continue
			}
			en, ok := skillIDWordList[string(r[i:i+n])]
			if !ok {
				continue
			}
			b.WriteByte(' ') // 词与词之间用空格隔开，最后统一过 skillIDForMarket
			b.WriteString(en)
			b.WriteByte(' ')
			i += n
			hit = true
			break
		}
		if hit {
			continue
		}
		if isHanRune(r[i]) {
			return "" // 有汉字没被词表覆盖：不猜，让用户手填
		}
		b.WriteRune(r[i]) // 英文/数字/分隔符原样保留，交给 skillIDForMarket 收拾
		i++
	}
	return skillIDForMarket(b.String())
}

/* ---------- 包体检查 ---------- */

type skillPackInfo struct {
	Size        int64
	SHA256      string
	SkillMDPath string            // 包内 SKILL.md 的路径（以包根为基准）
	BaseDir     string            // SKILL.md 所在目录（根 = ""）
	SkillMDSize int64             // SKILL.md 的字节数（0 = 空文件）
	SkillMDBody string            // SKILL.md 去掉 frontmatter 后的正文（不能为空）
	Name        string            // SKILL.md frontmatter 的 name
	Description string            // SKILL.md frontmatter 的 description
	Version     string            // SKILL.md frontmatter 的 version
	Tags        []string          // SKILL.md frontmatter 的 tags
	TopDirs     []string          // 包根下的顶层项名字（目录或文件）
	AllNames    []string          // 全部条目路径（重打包时判断是否已规整）
	Manifest    skillPackManifest // 包内 manifest.json（可能为空）
}

type skillPackManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// skillFrontmatter 取 SKILL.md 开头 YAML frontmatter 里的 name / description /
// version / tags（与 skillMeta 的读法一致：只认这几个键，不引 YAML 库）。
// tags 支持 `a, b`、`a，b`（中文逗号）与 `[a, b]` 三种写法。
func skillFrontmatter(text string) (name, description, version string, tags []string) {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "---") {
		return "", "", "", nil
	}
	i := strings.Index(s[3:], "\n---")
	if i < 0 {
		return "", "", "", nil
	}
	for _, line := range strings.Split(s[3:3+i], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), `"'`)
		case strings.HasPrefix(line, "description:"):
			description = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), `"'`)
		case strings.HasPrefix(line, "version:"):
			version = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "version:")), `"'`)
		case strings.HasPrefix(line, "tags:"):
			v := strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
			v = strings.Trim(strings.Trim(v, "[]"), " ")
			for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '，' }) {
				if t = strings.Trim(strings.TrimSpace(t), `"'`); t != "" {
					tags = append(tags, t)
				}
			}
		}
	}
	return name, description, version, tags
}

// checkSkillMDBody SKILL.md 内容体检：空文件、只有 frontmatter 没有正文的都拦下来。
// 这两种包发到市场上「装得上、用不了」——技能列表里没描述，模型调 read_skill 时
// 拿到的是「没有可用的说明正文」（runReadUserSkill 对空正文报错），等于给用户埋雷；
// 与其让它在市场里躺着，不如在发布这一步就说清楚。口径与 read_skill 对齐：
// 判定的是「去掉 frontmatter 之后的正文」（stripFrontmatter 与 skill_manager.go 同一份）。
func checkSkillMDBody(path string, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("包内 %s 是空文件（0 字节或只有空白）：技能必须有说明正文，"+
			"否则市场里装得上、read_skill 却读不出内容", path)
	}
	if strings.TrimSpace(stripFrontmatter(string(raw))) == "" {
		return fmt.Errorf("包内 %s 只有 frontmatter 没有正文：frontmatter 只放 name/description，"+
			"技能说明（怎么用、有哪些脚本）要写在 --- 之后", path)
	}
	return nil
}

// cleanZipPath 规范包内路径：统一正斜杠、去掉盘符/绝对路径与前导 ./。
func cleanZipPath(p string) string {
	s := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if i := strings.Index(s, ":"); i == 1 { // C:/...
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "/")
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	return s
}

// inspectSkillZip 检查技能包并抽出元信息：递归找 SKILL.md（包里常见「外层包目录 +
// 安装说明.md + 内层技能目录」这种两层结构），只读中央目录 + SKILL.md / manifest.json
// 两个条目，不解压整包。
func inspectSkillZip(data []byte) (*skillPackInfo, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是合法的 zip 包: %w", err)
	}
	info := &skillPackInfo{Size: int64(len(data))}
	sum := sha256.Sum256(data)
	info.SHA256 = hex.EncodeToString(sum[:])
	topDirs := map[string]bool{}
	names := make([]string, 0, len(zr.File))
	skillPaths := make([]string, 0, 1)
	noteTop := func(n string) { // 顶层项 = 路径第一段（目录或文件）
		n = strings.TrimSuffix(cleanZipPath(n), "/")
		if n == "" {
			return
		}
		if i := strings.Index(n, "/"); i > 0 {
			n = n[:i]
		}
		topDirs[n] = true
	}
	for _, f := range zr.File {
		n := cleanZipPath(f.Name)
		if n == "" {
			continue
		}
		noteTop(n)
		if strings.HasSuffix(n, "/") { // 目录条目
			continue
		}
		names = append(names, n)
		// 递归找 SKILL.md：不管它在外层包目录里还是技能目录里
		if strings.EqualFold(path.Base(n), "SKILL.md") {
			skillPaths = append(skillPaths, n)
		}
	}
	if len(skillPaths) == 0 {
		return nil, fmt.Errorf("包内递归找过了，没有 SKILL.md（技能必须自带说明文件）")
	}
	if len(skillPaths) > 1 {
		shown := skillPaths
		if len(shown) > 4 {
			shown = shown[:4]
		}
		return nil, fmt.Errorf("包里有 %d 处 SKILL.md（%s）：一个包只能装一个技能，请分开打包",
			len(skillPaths), strings.Join(shown, "、"))
	}
	info.SkillMDPath = skillPaths[0]
	if i := strings.LastIndex(info.SkillMDPath, "/"); i > 0 {
		info.BaseDir = info.SkillMDPath[:i]
	}
	info.AllNames = names
	info.TopDirs = sortedKeys(topDirs)
	// SKILL.md 文本 → frontmatter（读不出来 = 压缩数据坏了，直接拒收，
	// 不能静默降级成「没有 frontmatter」——那样 id/名称只能靠猜）
	raw, err := readZipEntry(zr, info.SkillMDPath)
	if err != nil {
		return nil, fmt.Errorf("包内 %s 读不出来（压缩数据损坏，或条目超过 4MB 上限）：%w", info.SkillMDPath, err)
	}
	info.SkillMDSize = int64(len(raw))
	if err := checkSkillMDBody(info.SkillMDPath, raw); err != nil {
		return nil, err
	}
	info.Name, info.Description, info.Version, info.Tags = skillFrontmatter(string(raw))
	info.SkillMDBody = stripFrontmatter(string(raw))
	// manifest.json（技能运行清单，可选）：技能目录里的那一份
	if raw, err := readZipEntry(zr, path.Join(info.BaseDir, "manifest.json")); err == nil {
		var m skillPackManifest
		if json.Unmarshal(raw, &m) == nil {
			info.Manifest = m
		}
	}
	return info, nil
} // rewriteSkillZip 把包规整成市场服务器接受的结构：包根只有那个技能目录
// （SKILL.md 所在目录）。技能目录藏在深层（外层包目录 + 内层技能目录）时提升到包根，
// 技能目录之外的散文件（如外层的「安装说明.md」）并进技能目录里——一个字节都不丢。
//
// jxai-server 的发布校验要求「SKILL.md 在包根，或包根下的唯一子目录里有 SKILL.md」，
// 而本机安装端（extractSkillZip）也只认一层，所以上传前统一收拾成这样。
// 已经规范的原样返回，保留原始字节（sha256 与用户手里那份包一致）。
func rewriteSkillZip(data []byte, pack *skillPackInfo) ([]byte, bool, error) {
	if pack.SkillMDPath == "SKILL.md" {
		return data, false, nil // 根上就有 SKILL.md：本来就合规
	}
	pkgDir := pack.BaseDir // 技能目录名（取最后一段，当包根下的唯一目录）
	if i := strings.LastIndex(pkgDir, "/"); i >= 0 {
		pkgDir = pkgDir[i+1:]
	}
	if pkgDir == "" {
		return data, false, nil
	}
	prefix := strings.ToLower(pack.BaseDir) + "/"
	clean := !strings.Contains(pack.BaseDir, "/")
	if clean {
		for _, n := range pack.AllNames {
			if !strings.HasPrefix(strings.ToLower(n), prefix) {
				clean = false // 技能目录外面还有东西（散文件或外层目录）
				break
			}
		}
	}
	if clean {
		return data, false, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, false, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := map[string]bool{}
	for _, f := range zr.File {
		n := cleanZipPath(f.Name)
		if n == "" || strings.HasSuffix(n, "/") {
			continue // 目录条目不用留：解压时自建
		}
		var target string
		if strings.HasPrefix(strings.ToLower(n), prefix) {
			target = pkgDir + "/" + n[len(pack.BaseDir)+1:] // 技能目录内的：相对路径照旧
		} else {
			target = pkgDir + "/" + path.Base(n) // 技能目录外的散文件：并进技能目录
			if used[strings.ToLower(target)] {
				target = pkgDir + "/" + n // 重名：保留原相对路径，别丢文件
			}
		}
		used[strings.ToLower(target)] = true
		hdr := &zip.FileHeader{Name: target, Method: f.Method}
		hdr.Modified = f.Modified
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, false, err
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false, err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if err != nil {
			return nil, false, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}

func hasZipEntry(names []string, want string) bool {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ { // 小数组，插入排序够用（避免 import sort）
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if strings.EqualFold(cleanZipPath(f.Name), name) {
			if f.UncompressedSize64 > 4<<20 { // SKILL.md / manifest.json 不该这么大
				return nil, fmt.Errorf("条目过大: %s", name)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 4<<20))
		}
	}
	return nil, fmt.Errorf("包里没有 %s", name)
}

/* ---------- 打包（前端投喂的是一组文件时） ---------- */

// buildSkillZip 按包内相对路径把一组文件打成 zip（UTF-8 名、deflate）。
// paths 与 uploads 一一对应：multipart 的 FileName() 只给 basename（Go 按 RFC 7578
// 剥掉了目录），所以包内相对路径由前端另用 paths 字段给。
func buildSkillZip(uploads []*multipart.FileHeader, paths []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, uh := range uploads {
		name := ""
		if i < len(paths) {
			name = paths[i]
		}
		if strings.TrimSpace(name) == "" {
			name = uh.Filename
		}
		name = cleanZipPath(name)
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		if name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return nil, fmt.Errorf("文件名里有非法路径: %s", name)
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.Modified = time.Now()
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		in, err := uh.Open()
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(fw, io.LimitReader(in, marketPublishMaxZip))
		in.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

/* ---------- 端点 ---------- */

func (a *App) handleMarketPublish(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(a.Cfg.MarketURL)
	if raw == "" {
		writeErr(w, 400, "未配置市场服务器（设置 · 网络访问 · 市场服务器）")
		return
	}
	origin, err := marketOrigin(raw)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, marketPublishMaxZip+16<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, 400, "上传内容解析失败（或超出大小上限）: "+err.Error())
		return
	}
	var meta struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Zip         bool     `json:"zip"`
	}
	if s := strings.TrimSpace(r.FormValue("meta")); s != "" {
		if err := json.Unmarshal([]byte(s), &meta); err != nil {
			writeErr(w, 400, "meta 不是合法 JSON: "+err.Error())
			return
		}
	}
	var uploads []*multipart.FileHeader
	if r.MultipartForm != nil {
		uploads = r.MultipartForm.File["files"]
	}
	if len(uploads) == 0 {
		writeErr(w, 400, "没有收到上传内容（files 为空）")
		return
	}
	// 包内相对路径（与 files 同序）：multipart 的 FileName() 只剩 basename，路径得单独给
	var paths []string
	if s := strings.TrimSpace(r.FormValue("paths")); s != "" {
		if err := json.Unmarshal([]byte(s), &paths); err != nil {
			writeErr(w, 400, "paths 不是合法 JSON 数组: "+err.Error())
			return
		}
	}

	// 1) 得到 zip 字节：单个 .zip 原样，否则现打
	var (
		data    []byte
		srcName string // 解析版本号用的原始名字（zip 文件名 / 包内目录名 / meta.id）
	)
	if len(uploads) == 1 && (meta.Zip || strings.EqualFold(path.Ext(uploads[0].Filename), ".zip")) {
		f, err := uploads[0].Open()
		if err != nil {
			writeErr(w, 400, "读取上传文件失败: "+err.Error())
			return
		}
		data, err = io.ReadAll(io.LimitReader(f, marketPublishMaxZip))
		f.Close()
		if err != nil {
			writeErr(w, 400, "读取上传文件失败: "+err.Error())
			return
		}
		srcName = uploads[0].Filename
	} else {
		data, err = buildSkillZip(uploads, paths)
		if err != nil {
			writeErr(w, 400, "打包失败: "+err.Error())
			return
		}
		// 文件夹上传：版本/id 看顶层目录名（前端也可在 meta 里给出）
		if len(paths) > 0 {
			if i := strings.Index(cleanZipPath(paths[0]), "/"); i > 0 {
				srcName = cleanZipPath(paths[0])[:i]
			}
		}
	}

	// 2) 校验包体 + 抽元信息；结构不合规（外层包目录 / 技能目录外的散文件）就规整后重算
	pack, err := inspectSkillZip(data)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	normalized := false
	if fixed, changed, nerr := rewriteSkillZip(data, pack); nerr != nil {
		writeErr(w, 400, "技能包规整失败: "+nerr.Error())
		return
	} else if changed {
		data = fixed
		normalized = true
		if pack, err = inspectSkillZip(data); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}

	// 3) 元信息：前端填的优先，缺的按 包内 manifest.json → 技能目录名 → SKILL.md 名 → 包名 推。
	//    市场端只收纯小写字母/数字/中划线的 id（中文名会被清洗掉、直接拒收），
	//    所以候选逐个过 skillIDForMarket，都推不出就要求手填。
	id := ""
	if raw := strings.TrimSpace(meta.ID); raw != "" {
		if id = skillIDForMarket(raw); id == "" {
			writeErr(w, 400, "技能 ID 只能用纯小写字母/数字/中划线（市场端规则，如 wechat-chat-ops），不能带中文或空格")
			return
		}
	} else {
		skillDir := ""
		if pack.BaseDir != "" {
			skillDir = pack.BaseDir
			if i := strings.LastIndex(skillDir, "/"); i >= 0 {
				skillDir = skillDir[i+1:]
			}
		}
		for _, cand := range []string{pack.Manifest.ID, skillDir, pack.Name, skillIDFromName(srcName)} {
			if id = skillIDForMarket(cand); id != "" {
				break
			}
		}
		// 上面全落空（多半是中文名）：用中文→英文词表给个建议 id。词表没覆盖全
		// 就还是给空串，落到下面「请手填」——不猜。
		if id == "" {
			for _, cand := range []string{skillDir, pack.Name, pack.Manifest.Name, srcName} {
				if id = skillIDFromChinese(cand); id != "" {
					break
				}
			}
		}
	}
	if id == "" {
		writeErr(w, 400, "技能 ID 推不出来：市场只收纯小写字母/数字/中划线的 ID（如 wechat-chat-ops），"+
			"中文名（如「微信操作」）请在弹层里填一个英文 ID")
		return
	}
	if builtinSkillIDs[id] {
		writeErr(w, 400, fmt.Sprintf("%s 是自带技能的 id，会装不上，请改名", id))
		return
	}
	version := strings.TrimSpace(meta.Version)
	if version == "" {
		version = skillVersionFromName(srcName)
	}
	if version == "" {
		version = strings.TrimSpace(pack.Manifest.Version)
	}
	if version == "" {
		version = strings.TrimSpace(pack.Version) // SKILL.md frontmatter 的 version
	}
	if version == "" {
		version = "0.0.0"
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = strings.TrimSpace(pack.Manifest.Name) // 包内 manifest.json 的友好名优先（SKILL.md 的 name 多是 id 风格）
	}
	if name == "" {
		name = strings.TrimSpace(pack.Name)
	}
	if name == "" {
		name = id
	}
	desc := strings.TrimSpace(meta.Description)
	if desc == "" {
		desc = strings.TrimSpace(pack.Description)
	}
	if desc == "" {
		desc = strings.TrimSpace(pack.Manifest.Description)
	}
	tags := make([]string, 0, len(meta.Tags))
	for _, t := range meta.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		tags = append(tags, pack.Tags...) // SKILL.md frontmatter 的 tags
	}

	// 4) 转发给市场服务器（同源 /admin/api/publish）
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("type", "skill")
	_ = mw.WriteField("id", id)
	// 发布者姓名：市场服务器记进条目的 owner；市场卡片据此判断「这条是不是我
	// 发的」（只对自己发的显示「更新发布 / 删除」）。没设置就不带（老行为）。
	if owner := strings.TrimSpace(a.Cfg.PublisherName); owner != "" {
		_ = mw.WriteField("owner", owner)
	}
	_ = mw.WriteField("name", name)
	_ = mw.WriteField("version", version)
	if desc != "" {
		_ = mw.WriteField("desc", desc)
	}
	if len(tags) > 0 {
		_ = mw.WriteField("tags", strings.Join(tags, ","))
	}
	pkgName := id + "-" + version + ".zip"
	fw, err := mw.CreateFormFile("file", pkgName)
	if err != nil {
		writeErr(w, 500, "组装发布请求失败: "+err.Error())
		return
	}
	if _, err := fw.Write(data); err != nil {
		writeErr(w, 500, "组装发布请求失败: "+err.Error())
		return
	}
	if err := mw.Close(); err != nil {
		writeErr(w, 500, "组装发布请求失败: "+err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, origin+"/admin/api/publish", &body)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := marketPublishClient.Do(req)
	if err != nil {
		writeErr(w, 502, "连接市场服务器失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var srv struct {
		Entry map[string]any `json:"entry"`
		Error string         `json:"error"`
	}
	_ = json.Unmarshal(respBody, &srv)
	if resp.StatusCode >= 400 {
		msg := srv.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		writeErr(w, resp.StatusCode, "市场服务器拒绝发布: "+msg)
		return
	}
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		logf := strings.TrimSpace(string(respBody))
		if len(logf) > 200 {
			logf = logf[:200]
		}
		writeErr(w, 502, fmt.Sprintf("市场服务器返回了意外状态 %d: %s", resp.StatusCode, logf))
		return
	}

	// 发布成功后清单就变了：清本机缓存，前端刷新即可看到新条目
	a.marketMu.Lock()
	a.marketCache = marketCacheEntry{}
	a.marketMu.Unlock()

	out := map[string]any{
		"ok": 1, "type": "skill", "id": id, "name": name, "version": version,
		"description": desc, "tags": tags, "sha256": pack.SHA256, "size": pack.Size,
		"skillMd": pack.SkillMDPath, "package": pkgName, "normalized": normalized,
	}
	if srv.Entry != nil {
		out["entry"] = srv.Entry
	}
	writeJSON(w, 200, out)
}

/* ---------- 删除（下架）自己发布的条目 ---------- */

// handleMarketUnpublish 把一条自己发布的条目从市场上删掉（下架）。
//
// 权限口径（2026-10-10 定）：只管「自己发的」——条目的 owner 与本机设置里的
// 发布者姓名一致才允许；管理员机制后置，不做例外。服务端的实现是
// POST <市场origin>/admin/api/unpublish（jxai-server 侧新增，需先上线）；
// 还没上线时把 404/405 转成一句明确的提示。老条目（owner 为空）谁都删不了，
// 等市场服务器开始记录归属后重新发布一次即可。
func (a *App) handleMarketUnpublish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !marketTypeOK(body.Type) || strings.TrimSpace(body.ID) == "" {
		writeErr(w, 400, "需要 type（skill/component/plugin）与 id")
		return
	}
	owner := strings.TrimSpace(a.Cfg.PublisherName)
	if owner == "" {
		writeErr(w, 400, "还没设置发布者姓名（设置 · 网络访问 · 发布者姓名），无法判断哪些条目是你发布的")
		return
	}
	raw := strings.TrimSpace(a.Cfg.MarketURL)
	if raw == "" {
		writeErr(w, 400, "未配置市场服务器（设置 · 网络访问 · 市场服务器）")
		return
	}
	origin, err := marketOrigin(raw)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// 先按最新清单核一遍归属：不是自己发的就地拒绝，不去打扰市场服务器
	man, err := a.fetchMarketManifest(raw, true)
	if err != nil {
		writeErr(w, 502, "市场清单获取失败: "+err.Error())
		return
	}
	entry, found := marketEntryIn{}, false
	for _, e := range marketEntriesOf(man, body.Type) {
		if e.ID == body.ID {
			entry, found = e, true
			break
		}
	}
	if !found {
		writeErr(w, 404, fmt.Sprintf("市场里没有 %s %s", body.Type, body.ID))
		return
	}
	if strings.TrimSpace(entry.Owner) == "" {
		writeErr(w, 403, "这条没有发布者信息（老条目，或市场服务器还没记录归属），删不了")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(entry.Owner), owner) {
		writeErr(w, 403, fmt.Sprintf("只能删除自己发布的条目：这条的发布者是「%s」", entry.Owner))
		return
	}
	ver := strings.TrimSpace(body.Version)
	if ver == "" {
		ver = entry.Version
	}
	payload, _ := json.Marshal(map[string]string{"type": body.Type, "id": body.ID, "version": ver, "owner": owner})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, origin+"/admin/api/unpublish", bytes.NewReader(payload))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := marketPublishClient.Do(req)
	if err != nil {
		writeErr(w, 502, "连接市场服务器失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		writeErr(w, 501, "市场服务器还没有删除接口（/admin/api/unpublish）：等 jxai-server 升级后再试")
		return
	}
	var srv struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(respBody, &srv)
	if resp.StatusCode >= 400 {
		msg := srv.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		writeErr(w, resp.StatusCode, "市场服务器拒绝删除: "+msg)
		return
	}
	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 204 {
		writeErr(w, 502, fmt.Sprintf("市场服务器返回了意外状态 %d", resp.StatusCode))
		return
	}
	// 条目没了：清本机清单缓存，前端刷新即看不到
	a.marketMu.Lock()
	a.marketCache = marketCacheEntry{}
	a.marketMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": 1, "type": body.Type, "id": body.ID, "version": ver, "owner": owner})
}
