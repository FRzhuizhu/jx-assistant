package main

// 会话后交付（deliver_file）：模型把工作区内的成品文件登记为「本轮结束
// 后由系统搬运到用户上传目录」的作业。登记即时返回、不搬文件——执行发
// 生在 finishRun（本轮输出完成、状态转空闲）之后，模型在对话中永远看
// 不到执行结果，交付通道因此不构成绕过文件权限的路径；执行结果以
// kind=report 注记落库，下一轮对话模型自然可见。
//
// 交付目标来自 uploads 表登记的原件路径（OrigPath）所在目录：桌面端把文件拖进
// 输入框、或用「＋→文件上传」选一次，都会登记原件真实路径，交付就是把成品送回
// 那个位置。模型不必指认是哪一次上传——只给 source 时按工作区副本路径反查同一条
// 记录（见 resolveUploadFor）；查不到原件路径时给出可操作的原因（那种来源交付
// 回不了原处，如实说明而不是硬搬）。
//
// 搬运语义（对齐产品定义）：
//   - 移动：source 复制到目标位置后删除原处（删不掉只留双份，不算失败）；
//   - 快照：目标处若存在将被替代的同名文件，先全量备份到
//     <工作区>/.jxai/delivery-backups/<会话>/<时间戳>_<文件名> 再替换；
//   - 原子：目标同目录临时文件写好后 rename 替换（copyFileAtomic）。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// androidDeliveryDir 手机端交付出口：公有目录「下载」下的专属子目录
//（targetSdk 28 走 legacy storage，WRITE_EXTERNAL_STORAGE 运行时权限即可直写）。
const androidDeliveryDir = "/sdcard/Download/坚祥数字助理"

func deliverFileTool() Tool {
	// 手机端：上传不登记原件路径，「交回上传处」语义不成立——交付出口统一为
	// 公有目录下载文件夹（用户在文件管理器/Wireless 传输里直接可见）。
	if isAndroidRuntime {
		return Tool{
			Spec: toolSpec("deliver_file",
				"把工作区里的成品文件交付到手机的「下载」目录（/sdcard/Download/坚祥数字助理/，用户在系统文件管理器里直接可见）。本轮对话结束后系统会移动该文件过去；目标处若有同名文件会先快照备份到工作区 .jxai/delivery-backups/ 再替换，执行结果以系统注记进入后续对话。"+
					"用户要求「把文件发给我/放到下载目录」时用本工具；工作区内常规写出不需要交付。若返回存储权限错误，请向用户说明：需要在系统弹窗或设置里允许本应用访问文件，然后重试。"+
					"调用本工具不会结束对话（对话结束的判定仍是：只输出思考和正文、没有工具调用），所以应在调用本工具之后再输出给用户的正文总结。",
				objSchema(map[string]any{
					"source":   strProp("待交付文件的路径（须在工作区内，相对或绝对）"),
					"filename": strProp("交付后的文件名（可选，缺省用源文件名）"),
				}, "source")),
			Run: runDeliverFile,
		}
	}
	return Tool{
		Spec: toolSpec("deliver_file",
			"把工作区里的成品文件交付回它原件所在的位置（仅本地会话）。核心逻辑：本轮对话结束后，系统会把这个文件送回那条上传记录登记的原件路径处——桌面端把文件拖进输入框、或用「＋→文件上传」选过的文件都登记了原件路径；若目标处存在将被替代的同名文件，会先把被替代的文件快照备份到工作区 .jxai/delivery-backups/ 下再完成替换，执行结果会以系统注记进入后续对话。"+
				"通常不必指认是哪一次上传：只给 source（工作区里的成品文件），系统会按上传记录自动找到它的原件位置；只有新生成的文件（不是从上传文件改出来的）要送回某处时，才需要显式给 upload（编号 u1 或该次上传的原件路径）。注意：用 write/edit 等文件工具在工作区内写出的常规改动【不需要】用本工具交付（系统本来就能跟踪那些改动）。"+
				"调用本工具不会结束对话（对话结束的判定仍是：只输出思考和正文、没有工具调用），所以应在调用本工具之后再输出给用户的正文总结。",
			objSchema(map[string]any{
				"source":   strProp("待交付文件的路径（须在工作区内，相对或绝对）"),
				"upload":   strProp("交付目标（可选）：上传注记里的编号（如 u1）或该次上传的原件路径；不给时按 source 自动反查它的原件位置，只有新生成的文件才需要显式指定"),
				"filename": strProp("交付后的文件名（可选，缺省用该次上传原件的文件名）"),
			}, "source")),
		Run: runDeliverFile,
	}
}

func runDeliverFile(t *toolContext, args json.RawMessage) (string, error) {
	var p struct {
		Source   string `json:"source"`
		Upload   string `json:"upload"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Source) == "" {
		return "", fmt.Errorf("source 不能为空")
	}
	// upload 是可选参数：模型只给 source 时按工作区副本路径反查那条上传记录里
	// 登记的原件路径（见 resolveUploadFor）；显式给了引用就照它解析。
	if t.isRemote() {
		return "", fmt.Errorf("交付工具仅支持本地会话：远程会话的工作区不在本机，无法搬运到本地上传目录")
	}
	abs, err := t.resolve(p.Source)
	if err != nil {
		return "", err
	}
	if !t.inWorkspace(abs) {
		return "", fmt.Errorf("待交付文件必须位于工作区内：%s（工作区外的文件不属于可交付的模型产物）", filepath.ToSlash(abs))
	}
	if _, err := t.authorize(permAction{Op: "read", Tool: "deliver_file", Path: abs}); err != nil {
		return "", err
	}
	if isDir, _, serr := t.envStat(abs); serr != nil || isDir {
		return "", fmt.Errorf("待交付文件不存在或不是文件：%s", filepath.ToSlash(abs))
	}
	// 手机端：交付出口固定为公有目录下载文件夹（不需要上传记录；目录建不出
	// 来多半是存储权限没给——把可操作的指引带给模型/用户，而不是裸错误）
	if isAndroidRuntime {
		name, err := sanitizeDeliveryName(p.Filename)
		if err != nil {
			return "", err
		}
		if name == "" {
			name = filepath.Base(abs)
		}
		if err := os.MkdirAll(androidDeliveryDir, 0755); err != nil {
			return "", fmt.Errorf("交付目录不可用：%v。多半是未授予存储权限——请向用户说明：在系统弹窗或 设置→应用→数字助理→权限 里允许「文件和媒体」访问后重试一次", err)
		}
		job := &DeliveryJob{
			SessionID: t.sess.ID, SourcePath: abs, TargetDir: androidDeliveryDir, TargetName: name,
			Status: "pending", CreatedAt: now(),
		}
		if err := t.app.Store.AddDeliveryJob(job); err != nil {
			return "", fmt.Errorf("交付登记落库失败: %w", err)
		}
		pending := t.app.Store.PendingDeliveryCount(t.sess.ID)
		out := fmt.Sprintf("已收到交付请求，将在本轮结束后执行（本轮因调用了工具不会结束对话，请继续完成正文输出）。\n登记：%s → %s。目标处若有将被替代的同名文件，会先快照备份到工作区 .jxai/delivery-backups/ 下再完成替换。",
			filepath.ToSlash(abs), filepath.ToSlash(filepath.Join(androidDeliveryDir, name)))
		if pending > 0 {
			out += fmt.Sprintf("当前待交付共 %d 项。", pending)
		}
		return out, nil
	}
	up, uerr := t.resolveUploadFor(p.Upload, abs)
	if uerr != nil {
		return "", uerr
	}
	targetDir := filepath.Dir(up.OrigPath)
	name, err := sanitizeDeliveryName(p.Filename)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = filepath.Base(up.OrigPath)
	}
	if di, err := os.Stat(targetDir); err != nil || !di.IsDir() {
		return "", fmt.Errorf("交付目标目录当前不存在：%s（用户可能移动或删除了该目录，请向用户说明）", filepath.ToSlash(targetDir))
	}
	job := &DeliveryJob{
		SessionID: t.sess.ID, SourcePath: abs, TargetDir: targetDir, TargetName: name,
		Status: "pending", CreatedAt: now(),
	}
	if err := t.app.Store.AddDeliveryJob(job); err != nil {
		return "", fmt.Errorf("交付登记落库失败: %w", err)
	}
	pending := t.app.Store.PendingDeliveryCount(t.sess.ID)
	out := fmt.Sprintf("已收到交付请求，将在本轮结束后执行（本轮因调用了工具不会结束对话，请继续完成正文输出）。\n登记（%s，送回该文件原件所在位置 %s）：%s → %s。目标处若有将被替代的同名文件，会先快照备份到工作区 .jxai/delivery-backups/ 下再完成替换。",
		up.UID, filepath.ToSlash(up.OrigPath), filepath.ToSlash(abs), filepath.ToSlash(filepath.Join(targetDir, name)))
	if pending > 0 {
		out += fmt.Sprintf("当前待交付共 %d 项。", pending)
	}
	return out, nil
}

// findUpload 按模型给的引用解析上传记录：优先 uid（u1/u2…），其次按
// 原件路径（大小写不敏感、词法归一后比对）。只认登记了原件路径的记录。
func (t *toolContext) findUpload(ref string) *Upload {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	ups, err := t.app.Store.UploadsFor(t.sess.ID)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(ref)
	for _, u := range ups {
		if u.OrigPath == "" {
			continue
		}
		if strings.EqualFold(u.UID, ref) {
			return u
		}
		if strings.EqualFold(u.OrigPath, ref) || strings.EqualFold(filepath.ToSlash(u.OrigPath), lower) {
			return u
		}
		if strings.EqualFold(canonicalFor(u.OrigPath), canonicalFor(ref)) {
			return u
		}
		// 也认工作区副本路径：模型常把注记里的工作区路径原样当作 upload 传
		// 回来，那条记录里的原件路径才是真正的交付目标。
		if strings.EqualFold(canonicalFor(u.CopyPath), canonicalFor(ref)) {
			return u
		}
	}
	return nil
}

// resolveUploadFor 解析本次交付要用的上传记录（交付目标的来源）。
//   - 模型显式给了引用：按 uid / 原件路径 / 工作区副本路径解析，解析不了就
//     报可读错误并列出可用编号（新生成的文件要走这条路）；
//   - 没给引用：按待交付文件（source）反查——工作区里的成品文件若是从某个
//     上传文件改出来的，它就在那条记录的 CopyPath 上，原件位置随之确定。
//     同一路径被多次登记时取最后一条（UploadsFor 按 id 升序）。
//
// 反查不到说明这份文件不是从本机拖拽/选择进来的（浏览器、手机、粘贴上传
// 只送内容、不登记原件路径），此时不能瞎猜一个目录去覆盖，如实说明并给出
// 用户可操作的做法。
func (t *toolContext) resolveUploadFor(ref, sourceAbs string) (*Upload, error) {
	ups, err := t.app.Store.UploadsFor(t.sess.ID)
	if err != nil {
		return nil, fmt.Errorf("读取上传记录失败: %v", err)
	}
	if strings.TrimSpace(ref) != "" {
		if u := t.findUpload(ref); u != nil {
			return u, nil
		}
		return nil, fmt.Errorf("找不到上传记录 %s；可用的编号与原件位置：%s", ref, uploadRefList(ups))
	}
	src := canonicalFor(sourceAbs)
	var hit *Upload
	for _, u := range ups {
		if u.OrigPath == "" || u.CopyPath == "" {
			continue
		}
		// 大小写不敏感：canonicalFor 在文件已不存在时会退回词法 Clean（不改大小写），
		// 而归一化拿到的真实大小写两边可能不同（Windows 路径本就大小写不敏感）。
		if strings.EqualFold(canonicalFor(u.CopyPath), src) {
			hit = u
		}
	}
	if hit != nil {
		return hit, nil
	}
	return nil, fmt.Errorf("这份文件没有登记原件路径，交付不了：本会话的上传记录里没有它的原件位置（浏览器、手机、粘贴进来的文件只送内容、登记不了本机路径；新生成的文件也从没登记过）。"+
		"请如实向用户说明，并给两条出路：①要送回桌面上的原位置——让用户把原文件从桌面拖进输入框、或用「＋→文件上传」选一次，再重新交付；"+
		"②这次不交回原处也行——成品就在工作区 %s，用户可以自己取走（也可以用其它方式说明想要的位置）。本会话已登记原件路径的上传：%s。",
		filepath.ToSlash(sourceAbs), uploadRefList(ups))
}

// uploadRefList 列出本会话可用的上传编号与原件位置（给模型纠错用）。
func uploadRefList(ups []*Upload) string {
	parts := []string{}
	for _, u := range ups {
		if u.OrigPath != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", u.UID, filepath.ToSlash(u.OrigPath)))
		}
	}
	if len(parts) == 0 {
		return "（本会话还没有登记过原件路径的上传）"
	}
	return strings.Join(parts, "；")
}

// sanitizeDeliveryName 清洗模型给的交付文件名：只取 basename、拒绝路径
// 分隔符与点目录；空串是合法返回（表示未指定、用原件名）。
func sanitizeDeliveryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("交付文件名不能包含路径分隔符：%s（只给文件名，目录由上传记录决定）", name)
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("非法交付文件名：%s", name)
	}
	if len([]rune(name)) > 200 {
		return "", fmt.Errorf("交付文件名过长（>200 字符）")
	}
	return name, nil
}

// flushDeliveries 认领并执行交付作业。sessionID 为空串时处理全部会话
// （启动恢复用）；由 finishRun 异步调用，ClaimPendingJobs 的原子认领
// 保证并发 flush 不重不漏。
func (a *App) flushDeliveries(sessionID string) {
	jobs, err := a.Store.ClaimPendingJobs(sessionID)
	if err != nil {
		log.Printf("[交付] 认领待执行作业失败: %v", err)
		return
	}
	for _, j := range jobs {
		a.runDeliveryJob(j)
	}
}

func (a *App) runDeliveryJob(j *DeliveryJob) {
	j.ExecutedAt = now()
	ws := a.Cfg.Workspace
	if m, err := a.Store.Get(j.SessionID); err == nil {
		if m.Remote != nil {
			a.finishDeliveryJob(j, "failed", "远程会话不支持交付执行（工作区不在本机）")
			return
		}
		ws = wsOr(a, m)
	}
	fail := func(msg string) {
		a.finishDeliveryJob(j, "failed", msg)
	}
	fi, err := os.Stat(j.SourcePath)
	if err != nil || fi.IsDir() {
		fail(fmt.Sprintf("待交付文件不存在或已被移动：%s（源文件不动的规则下无法交付，若模型已改名请重新登记）", filepath.ToSlash(j.SourcePath)))
		return
	}
	// 目标目录兜底重建（授权晚于登记、目录被清等场景），建不出才算失败
	_ = os.MkdirAll(j.TargetDir, 0755)
	if di, err := os.Stat(j.TargetDir); err != nil || !di.IsDir() {
		fail(fmt.Sprintf("交付目标目录不存在：%s", filepath.ToSlash(j.TargetDir)))
		return
	}
	target := filepath.Join(j.TargetDir, j.TargetName)
	// 目标处有将被替代的同名文件：先全量快照，快照失败就不动目标（宁可
	// 不交付也不能没有退路）
	if _, err := os.Stat(target); err == nil {
		backup, err := uniqueBackupPath(ws, j.SessionID, j.TargetName)
		if err == nil {
			err = copyFileAtomic(backup, target)
		}
		if err != nil {
			fail(fmt.Sprintf("快照备份失败，已放弃本次交付（目标文件未动）：%v", err))
			return
		}
		j.BackupPath = backup
	}
	if err := copyFileAtomic(target, j.SourcePath); err != nil {
		fail(fmt.Sprintf("搬运失败：%v（目标文件未被动过或仍为旧版）", err))
		return
	}
	// 移动语义：目标就位后再删源文件；删不掉只是留了双份，不算失败
	if err := os.Remove(j.SourcePath); err != nil {
		log.Printf("[交付] 源文件删除失败（交付本身已成功，工作区留有副本）：%s: %v", j.SourcePath, err)
	}
	a.finishDeliveryJob(j, "done", "")
}

// finishDeliveryJob 回写作业终态并向会话注入模型可见的执行结果注记。
func (a *App) finishDeliveryJob(j *DeliveryJob, status, errMsg string) {
	j.Status = status
	j.Error = errMsg
	_ = a.Store.UpdateDeliveryJob(j)
	target := filepath.ToSlash(filepath.Join(j.TargetDir, j.TargetName))
	var note string
	switch status {
	case "done":
		note = fmt.Sprintf("【交付完成】已把 %s 移动到 %s。", filepath.ToSlash(j.SourcePath), target)
		if j.BackupPath != "" {
			note += fmt.Sprintf("目标处原有同名文件，已被替代并快照备份到 %s。", filepath.ToSlash(j.BackupPath))
		} else {
			note += "目标处原无同名文件，未发生替代。"
		}
	default:
		note = fmt.Sprintf("【交付完成】交付《%s》失败：%s。", j.TargetName, errMsg)
	}
	_ = a.Store.AppendRow(j.SessionID, &StoredMessage{
		Message: TextMessage("user", note), Kind: kindReport, Origin: "delivery", CreatedAt: now(),
	})
	if m, err := a.Store.Get(j.SessionID); err == nil {
		m.UpdatedAt = now()
		_ = a.Store.SaveMeta(m)
	}
	a.broadcast(Event{Type: "session_updated", SessionID: j.SessionID})
}

// uniqueBackupPath 生成不冲突的快照备份路径：
// <工作区>/.jxai/delivery-backups/<会话>/<yyyyMMdd-HHmmss>_<文件名>，
// 同秒撞名时追加 -2、-3…。
func uniqueBackupPath(ws, sessionID, name string) (string, error) {
	dir := filepath.Join(ws, ".jxai", "delivery-backups", sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	p := filepath.Join(dir, stamp+"_"+name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p, nil
		}
		p = filepath.Join(dir, fmt.Sprintf("%s_%s-%d", stamp, name, i))
	}
}
