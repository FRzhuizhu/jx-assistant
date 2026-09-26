package main

// 原子替换写入（对齐 DSH fs-local 的 writeFileAtomic）：同目录私有临时文件
// 写好后 rename 替换目录项。两层意义：
//   - 任何时刻磁盘上不存在"半截文件"（读到一半的是旧版或新版全文）；
//   - 替换语义换指新 inode：工作区内指向外部文件的 NTFS 硬链接不会被写穿
//     （外部别名保持旧内容，与 DSH 硬链接边界的事实性兜底同款）。

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic 以原子替换语义写文件：同目录建临时文件 → 写入 + fsync →
// （Windows：尽力把原文件的安全描述符复制到临时文件）→ rename 覆盖。
// 临时文件建不出来时退回直写（少一层原子性但功能不减）。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".jx-atomic-*")
	if err != nil {
		return os.WriteFile(path, data, 0644)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // rename 成功后这是 no-op
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	copySecurityDescriptorBestEffort(path, tmpName)
	if err := os.Rename(tmpName, path); err != nil {
		// 失败常见原因：目标正被其他程序占用/锁定（rename 需要删除权，
		// 多数程序不共享删除），或目标只读。明确告知这不是沙箱拒绝——
		// 申请 sandbox_permissions 无济于事，免得模型去跟审批打架。
		return fmt.Errorf("替换写入 %s 失败: %w（常见原因：目标文件正被其他程序占用或为只读——这不是沙箱拒绝，申请权限无济于事；请让用户关闭占用该文件的程序后重试）",
			filepath.ToSlash(path), err)
	}
	return nil
}
