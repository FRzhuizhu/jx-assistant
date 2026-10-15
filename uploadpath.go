package main

// 桌面端「按本机路径登记上传」的公共实现。/api/upload 的 JSON {path} 路径
// （handleUploadByPath）与数字助理派发的附件（dispatch_task 的 attachments）
// 走的是同一套动作——校验路径 → 必要时复制一份进会话工作区（流式，不限额）
// → 写 uploads 表（deliver_file 的交付目标来源）→ 回落一条【文件上传】系统
// 注记，让模型知道文件在哪、能当交付目标。两个入口共用本函数，行为与文案
// 永远一致：人工上传是什么效果，派发附件就是什么效果。
//
// 校验与登记分两步：resolveUploadSource 只校验来源（派发工具在建会话之前先用
// 它做预检，坏路径要拒绝在建出会话之前），registerUploadPath 才真动手复制与
// 落库。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

// registerUploadPath 把本机文件路径 p 登记为会话 sess 的一次上传，返回登记
// 记录与【文件上传】注记正文（注记由调用方决定何时落库，见 landUploadNote）。
// 原件已在工作区内时不做复制（避免自己覆盖自己），只登记。
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
	dst := filepath.Join(ws, name)
	inWorkspace := strings.EqualFold(canonicalFor(abs), canonicalFor(dst))
	if !inWorkspace {
		// 用户主动上传不走模型权限审批，直接复制进工作区（流式，不受
		// multipart 那条 32MB 上限约束）
		if err := copyFileAtomic(dst, abs); err != nil {
			return nil, "", uploadPathErr(500, "复制进工作区失败: %v", err)
		}
	}
	u := &Upload{SessionID: sess.ID, OrigPath: abs, CopyPath: dst, Name: name, Size: size, CreatedAt: now()}
	if err := a.Store.AddUpload(u); err != nil {
		return nil, "", uploadPathErr(500, "上传登记落库失败: %v", err)
	}
	var note string
	if inWorkspace {
		note = fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：用户给的文件本来就在工作区内 %s，没有另外的「原件位置」——交付工具（deliver_file）只会把它搬回工作区同一目录。",
			u.UID, name, size, filepath.ToSlash(dst))
	} else {
		note = fmt.Sprintf("【文件上传】%s《%s》（%d 字节）：原件位于 %s（用户拖入/选择它的位置），已复制一份到工作区 %s。交付工具（deliver_file）会把成品送回 %s 这个位置；只给 source（工作区里的成品文件）即可，不必指认是哪一次上传。",
			u.UID, name, size, filepath.ToSlash(abs), filepath.ToSlash(dst), filepath.ToSlash(filepath.Dir(abs)))
	}
	return u, note, nil
}

// landUploadNote 把一条【文件上传】注记落进会话（report/origin=upload，模型
// 读得到），并按上传的口径刷新会话更新时间、广播刷新——与用户手动上传后的
// 表现完全一致。
func (a *App) landUploadNote(sess *SessionMeta, note string) {
	_ = a.Store.AppendRow(sess.ID, &StoredMessage{Message: TextMessage("user", note), Kind: kindReport, Origin: "upload", CreatedAt: now()})
	sess.UpdatedAt = now()
	_ = a.Store.SaveMeta(sess)
	a.broadcast(Event{Type: "session_updated", SessionID: sess.ID})
}
