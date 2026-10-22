package main

import (
	"strings"
	"testing"
)

// ---------- 注册表拦截规则（registryNukeHit）----------

func TestRegistryNukeBlocked(t *testing.T) {
	blocked := []string{
		// reg delete 整键删除（无 /v）
		`reg delete HKLM\SOFTWARE\MyApp /f`,
		`REG DELETE hklm\SYSTEM /f`,
		`reg delete HKCR\.exe /f`,
		`reg delete HKLM\SOFTWARE\X /va /f`,           // /va 删键下全部值，仍属大范围
		`reg delete "HKLM\SOFTWARE\My App" /f`,        // 带引号键名
		`reg delete HKLM\SOFTWARE\X /f & echo done`,   // 分段：第一段命中
		`reg.exe delete HKLM\SYSTEM\CurrentControlSet\Services\X /f`,
		`powershell -c "reg delete HKLM\SOFTWARE\X /f"`, // 嵌在引号里也要拦
		`reg delete \\REMOTEPC\HKLM\SOFTWARE\X /f`,    // 远程机形态
		// PowerShell 注册表提供程序
		`Remove-Item HKLM:\SOFTWARE\X -Recurse -Force`,
		`remove-item -Path HKLM:\SOFTWARE\X -Recurse`,
		`Get-ChildItem HKLM:\SOFTWARE\Foo | Remove-Item -Recurse -Force`, // 管道形态
		`ri HKCR:\.txt -Recurse`,
		`Get-ChildItem HKCR:\ | ri -Recurse`,
		`ri HKLM:\SOFTWARE\X -Recurse -Confirm:$false`,
	}
	for _, cmd := range blocked {
		if why := destructiveCommandHit(cmd); why == "" {
			t.Errorf("应拦截：%q", cmd)
		}
	}
}

func TestRegistryNukeAllowed(t *testing.T) {
	allowed := []string{
		// 只读/导出
		`reg query HKLM\SOFTWARE\Microsoft`,
		`reg export HKLM\SOFTWARE\X backup.reg`,
		`Get-ChildItem HKLM:\SOFTWARE`,
		`Get-ItemProperty HKLM:\SOFTWARE\X`,
		// 单值级写删（可逆，交给沙箱/审批层）
		`reg add HKLM\SOFTWARE\MyCompany\App /v Version /d 1.0 /f`,
		`reg delete HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run /v MyApp /f`,
		`reg delete HKLM\SOFTWARE\X /ve /f`,
		`Set-ItemProperty HKLM:\SOFTWARE\X -Name Foo -Value 1`,
		`Remove-ItemProperty HKLM:\SOFTWARE\X -Name Foo`,
		// HKCU 完全不拦（用户态）
		`reg delete HKCU\Software\Foo /f`,
		`Remove-Item HKCU:\Foo -Recurse`,
		// 旧规则 .*hklm 无锚定的误中形态（HKCU 下键名碰巧含 hklm 字样）
		`reg delete HKCU\Software\MyHklmTool /f`,
		// 普通文件删除不受新规则影响
		`Remove-Item D:\工作区\X -Recurse`,
		`Remove-Item HKLM:\SOFTWARE\X`, // 无 -Recurse：非交互下本就因确认提示失败
	}
	for _, cmd := range allowed {
		if why := destructiveCommandHit(cmd); why != "" {
			t.Errorf("应放行：%q，实际命中：%s", cmd, why)
		}
	}
}

// ---------- 其余危险规则零变化（回归）----------

func TestOtherDestructiveRulesUnchanged(t *testing.T) {
	cases := []struct{ cmd, why string }{
		{"format c:", "格式化磁盘"},
		{"shutdown /s /t 0", "关机/重启"},
		{"Restart-Computer -Force", "关机/重启"},
		{"mkfs.ext4 /dev/sda1", "格式化文件系统"},
		{"dd if=a.img of=/dev/sda", "直写块设备"},
		{"rm -rf /", "递归删除根目录或用户主目录"},
		{"rm -rf ~/", "递归删除根目录或用户主目录"},
		{`rd C:\`, "递归删除盘根目录"},
		{`rmdir D:\`, "递归删除盘根目录"},
		{`del /f /s /q C:\`, "递归强删盘根目录"},
		{"bcdedit /set {default} recoveryenabled no", "系统级破坏工具"},
		{":(){ :|:& };:", "fork 炸弹"},
	}
	for _, tc := range cases {
		if why := destructiveCommandHit(tc.cmd); why != tc.why {
			t.Errorf("%q 期望命中 %q，实际 %q", tc.cmd, tc.why, why)
		}
	}
}

// ---------- 拒绝文案：指明护栏、原因、出路，且明说勿绕过 ----------

func TestGuardRejectErrorMentionsNoBypass(t *testing.T) {
	msg := guardRejectError("整键递归删除系统注册表（HKLM/HKCR）").Error()
	for _, want := range []string{"危险命令拦截", "整键递归删除系统注册表", "手动执行", "绕过"} {
		if !strings.Contains(msg, want) {
			t.Errorf("拒绝文案缺关键信息 %q，全文：%s", want, msg)
		}
	}
}
