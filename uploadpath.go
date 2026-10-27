package main

// 桌面端「按本机路径登记上传」的公共实现。/api/upload 的 JSON {path} 路径
// （handleUploadByPath）与数字助理派发的附件（dispatch_task 的 attachments）
// 走的是同一套动作——校验路径 → 判重去重或复制进 .jxai/uploads → 写 uploads
// 表（deliver_file 的交付目标来源）→ 返回【文件上传】注记文本，由调用方拼进
// 用户消息开头的「用户附件：」块，让模型知道文件在哪、能当交付目标。两个入口
// 共用本函数，行为与文案永远一致：人工上传是什么效果，派发附件就是什么效果。
//
// 上传落位与判重（与 server.go handleUpload 的 multipart 分支同一套规矩）：
//   - 用户上传的文件统一存 <工作区>/.jxai/uploads/，不占工作区根目录；
//   - 上传永不覆盖任何现有文件：内容一致（大小+全内容哈希，见 fileFingerprint）
//     的重传当上传成功处理、不重复落盘；同名不同内容顺延 -2/-3 存新文件；
//   - 判重对的是 .jxai/uploads 里全部现有文件（不只比同名那个），所以
//     「传 A → 传改过的 A（存成 A-2）→ 再传与第二次相同的 A」第三下能认出
//     与 A-2 一致；模型改过工作区副本后，磁盘指纹随之变化，重传原件会如实
//     按不同文件顺延存新，绝不静默丢数据——因此指纹只现场算、不落库。
//
// 校验与登记分两步：resolveUploadSource 只校验来源（派发工具在建会话之前先用
// 它做预检，坏路径要拒绝在建出会话之前），registerUploadPath 才真动手复制与
// 落库。

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ATTACH_HEAD 用户消息里附件块的头（与前端 app-chat.js 的同名常量一致）：
// 「用户附件：\n<注记行逐字原样>\n\n」拼在消息最前，注记随用户消息一起到达
const ATTACH_HEAD = "用户附件："

// uploadPathError 是附件登记失败的错误：带建议的 HTTP 状态码（客户端给的
// 路径有问题=400，本地复制/落库出问题=500），HTTP 处理器照原样回状态码；
// 派发工具只取它的文案。
type uploadPathError struct {
	code int
	msg  string
}

func (e *uploadPathError) Error() string { return e.msg }

func uploadPathErr(code int, format string, a ...any) error {
	return &uploadPathError{code: code, msg: fmt.Sprintf(format, a...)}
}

// uploadErrCode 取附件登记错误建议的 HTTP 状态码（非本类错误一律 500）。
func uploadErrCode(err error) int {
	if e, ok := err.(*uploadPathError); ok && e.code > 0 {
		return e.code
	}
	return 500
}

// resolveUploadSource 校验并解析一个待登记的本机上传来源：返回绝对路径、
// 文件名与字节数。空路径、路径解析失败、不存在或不可访问、是目录、非法
// 文件名一律报错（不碰任何文件、不落任何库）。
func resolveUploadSource(p string) (string, string, int64, error) {
	src := strings.TrimSpace(p)
	if src == "" {
		return "", "", 0, uploadPathErr(400, "缺少 path 字段")
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", "", 0, uploadPathErr(400, "路径解析失败: %v", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", "", 0, uploadPathErr(400, "文件不存在或不可访问: %s", abs)
	}
	if fi.IsDir() {
		return "", "", 0, uploadPathErr(400, "路径是目录不是文件: %s", abs)
	}
	name := filepath.Base(abs)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/`) {
		return "", "", 0, uploadPathErr(400, "非法文件名")
	}
	return abs, name, fi.Size(), nil
}

// ---------- 上传指纹与判重（路径型 / multipart 共用） ----------

// fileFingerprint 是一次「用户重传的是不是同一个文件」判等的依据：
// 大小 + FNV-1a 64 位全内容哈希。非密码学场景（防的是误判，不是伪造），
// 配合大小预过滤足够可靠；hash/fnv 是标准库、吞吐约 GB/s，百兆文件几十
// 毫秒。刻意不落库：指纹永远对着磁盘现状现场算（见文件头注释）。
type fileFingerprint struct {
	size int64
	hash uint64
}

// fingerprintBytes 算内存数据的指纹（multipart 上传：内容已在手里）。
func fingerprintBytes(data []byte) fileFingerprint {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return fileFingerprint{size: int64(len(data)), hash: h.Sum64()}
}

// fingerprintFile 流式算一个文件的指纹（路径上传的源文件、uploads 目录里的
// 候选文件）。远程会话经 SFTP 读全文——远程上传少见，简单优先。
func fingerprintFile(tctx *toolContext, p string) (fileFingerprint, error) {
	if tctx.isRemote() {
		data, err := tctx.envReadFile(p)
		if err != nil {
			return fileFingerprint{}, err
		}
		return fingerprintBytes(data), nil
	}
	f, err := os.Open(p)
	if err != nil {
		return fileFingerprint{}, err
	}
	defer f.Close()
	h := fnv.New64a()
	size, err := io.Copy(h, f)
	if err != nil {
		return fileFingerprint{}, err
	}
	return fileFingerprint{size: size, hash: h.Sum64()}, nil
}

// envPathJoin 在本地与远程工作区各自正确的分隔符约定下拼路径。
func (t *toolContext) envPathJoin(dir, name string) string {
	if t.isRemote() {
		return path.Join(dir, name)
	}
	return filepath.Join(dir, name)
}

// uploadsDirOf 返回会话工作区的上传目录（<工作区>/.jxai/uploads）。目录不
// 预建：写入方（copyFileAtomic / envWriteFile）都会 MkdirAll，判重扫描对
// 不存在的目录按"无候选"处理。
func (t *toolContext) uploadsDirOf() (string, error) {
	ws, err := t.resolve("")
	if err != nil {
		return "", err
	}
	if t.isRemote() {
		return path.Join(ws, jxAIName, "uploads"), nil
	}
	return filepath.Join(ws, jxAIName, "uploads"), nil
}

// findUploadDuplicate 在上传目录里找与 fp 内容一致的现有文件：先比大小
// （零成本排除绝大多数候选），大小相同才现场算哈希。命中返回该文件完整
// 路径，没命中（或目录还不存在）返回空串。比对集合是目录里**全部**文件
// ——不只同名那个，这是「第三份重传能认出第二份（A-2）」的关键。
func (t *toolContext) findUploadDuplicate(uploadsDir string, fp fileFingerprint) string {
	entries, err := t.envListDir(uploadsDir)
	if err != nil {
		return "" // 目录还没有（首次上传）或读不了：没有可判重的，按新文件走
	}
	for _, e := range entries {
		if e.IsDir || e.Size != fp.size {
			continue
		}
		p := t.envPathJoin(uploadsDir, e.Name)
		cfp, err := fingerprintFile(t, p)
		if err != nil {
			continue // 单个候选读不了就跳过，不拦上传
		}
		if cfp == fp {
			return p
		}
	}
	return ""
}

// suffixedName 生成顺延名：报告.pptx → 报告-2.pptx（序号插在扩展名前，
// 与计划导出的同名顺延同款约定；无扩展名则直接追加）。
func suffixedName(name string, n int) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), n, ext)
}

// allocUploadName 给上传分配第一个空闲文件名：原名被任何目录项占用（含
// 同名目录，撞上也不会去覆盖）就顺延 -2、-3……「上传永不覆盖」由它保证。
func (t *toolContext) allocUploadName(uploadsDir, name string) string {
	if _, _, err := t.envStat(t.envPathJoin(uploadsDir, name)); err != nil {
		return name // 名字空闲
	}
	for n := 2; n < 10000; n++ {
		cand := suffixedName(name, n)
		if _, _, err := t.envStat(t.envPathJoin(uploadsDir, cand)); err != nil {
			return cand
		}
	}
	return suffixedName(name, 9999) // 防御上限：一万份同名不同内容的上传，实际到不了
}

// uploadDupRef 找出上传目录里某个现有文件在本会话登记表里的编号（"u2"那类
// 引用），供去重注记对上号；没登记过就返回空串。
func (a *App) uploadDupRef(sess *SessionMeta, dupPath string) string {
	ups, err := a.Store.UploadsFor(sess.ID)
	if err != nil {
		return ""
	}
	fold := filepath.Separator
	if sess.Remote != nil {
		fold = '/'
	}
	eq := func(p string) bool {
		if fold == '/' {
			return p == dupPath
		}
		return strings.EqualFold(filepath.Clean(p), filepath.Clean(dupPath))
	}
	for i := len(ups) - 1; i >= 0; i-- { // 取最新一条（同路径多次登记时）
		if eq(ups[i].CopyPath) {
			return ups[i].UID
		}
	}
	return ""
}

// registerUploadPath 把本机文件路径 p 登记为会话 sess 的一次上传，返回登记
// 记录与【文件上传】注记正文（注记由调用方拼进用户消息的「用户附件：」块）。
// 原件已在工作区内时不做复制（避免自己覆盖自己），只登记；其余情况按文件头
// 注释的判重/顺延规矩落位 .jxai/uploads。
func (a *App) registerUploadPath(sess *SessionMeta, p string) (*Upload, string, error) {
	if sess.Remote != nil {
		return nil, "", uploadPathErr(400, "远程会话不支持按本机路径上传，请用网页文件上传")
	}
	abs, name, size, err := resolveUploadSource(p)
	if err != nil {
		return nil, "", err
	}
	tctx := &toolContext{app: a, sess: sess}
	ws, err := tctx.resolve("")
	if err != nil {
		return nil, "", uploadPathErr(500, "%v", err)
	}
	// 原件就是工作区根下那个同名文件：不复制只登记（历史行为，交付语义不变）
	dst := filepath.Join(ws, name)
	if strings.EqualFold(canonicalFor(abs), canonicalFor(dst)) {
		u := &Upload{SessionID: sess.ID, OrigPath: abs, CopyPath: dst, Name: name, Size: size, CreatedAt: now()}
		if err := a.Store.AddUpload(u); err != nil {
			return nil, "", uploadPathErr(500, "上传登记落库失败: %v", err)
		}
		note := fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：用户给的文件本来就在工作区内 %s，没有另外的「原件位置」——交付工具（deliver_file）只会把它搬回工作区同一目录。",
			u.UID, name, size, filepath.ToSlash(dst))
		return u, note, nil
	}
	uploadsDir, err := tctx.uploadsDirOf()
	if err != nil {
		return nil, "", uploadPathErr(500, "%v", err)
	}
	// 判重：内容一致的重传当成功处理，不重复落盘（也绕开一切覆盖/占用问题）
	fp, ferr := fingerprintFile(tctx, abs)
	if ferr == nil {
		if dup := tctx.findUploadDuplicate(uploadsDir, fp); dup != "" {
			u := &Upload{SessionID: sess.ID, OrigPath: abs, CopyPath: dup, Name: name, Size: size, CreatedAt: now()}
			if err := a.Store.AddUpload(u); err != nil {
				return nil, "", uploadPathErr(500, "上传登记落库失败: %v", err)
			}
			ref := ""
			if uid := a.uploadDupRef(sess, dup); uid != "" {
				ref = "（即 " + uid + " 那次上传的文件）"
			}
			note := fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：与工作区已有文件 %s 内容完全一致（大小与逐字节哈希比对通过）%s，当上传成功处理，没有重复复制。原件位于 %s；交付时给 deliver_result 的文件项填 upload=%s（或原件路径）即可把成品送回 %s 这个位置。",
				u.UID, name, size, filepath.ToSlash(dup), ref, filepath.ToSlash(abs), u.UID, filepath.ToSlash(filepath.Dir(abs)))
			return u, note, nil
		}
	} // 指纹算不出来（源文件读不了等）：按全新文件继续，复制那步会给出真实错误
	// 全新内容：分配空闲名（同名不同内容顺延 -2/-3），复制（用户主动上传不走
	// 模型权限审批，流式不受 multipart 限额约束），永不覆盖任何现有文件
	dstName := tctx.allocUploadName(uploadsDir, name)
	dst = filepath.Join(uploadsDir, dstName)
	if err := copyFileAtomic(dst, abs); err != nil {
		return nil, "", uploadPathErr(500, "复制进工作区失败: %v", err)
	}
	u := &Upload{SessionID: sess.ID, OrigPath: abs, CopyPath: dst, Name: name, Size: size, CreatedAt: now()}
	if err := a.Store.AddUpload(u); err != nil {
		return nil, "", uploadPathErr(500, "上传登记落库失败: %v", err)
	}
	renamed := ""
	if dstName != name {
		renamed = fmt.Sprintf("工作区里已有同名的其他内容，这份存成了 %s（原有文件未被动过）。", dstName)
	}
	note := fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：原件位于 %s（用户拖入/选择它的位置），已复制一份到工作区 %s（用户上传的文件统一存放在 .jxai/uploads，不占工作区根目录）。%s交付工具（deliver_result 的 upload 参数）会把成品送回 %s 这个位置；只给 path（工作区里的成品文件）即可，upload 不填系统也能按文件自动反查。",
		u.UID, name, size, filepath.ToSlash(abs), filepath.ToSlash(dst), renamed, filepath.ToSlash(filepath.Dir(abs)))
	return u, note, nil
}

// landUploadNote 已删除：上传注记不再作为独立消息落库，一律随上传响应返回，
// 由调用方（前端发送链路 / dispatch_task 派发）拼进用户消息开头的「用户附件：」
// 块——注记随用户消息一起到达模型，插话/排队场景不再出现注记先到、消息后到。
