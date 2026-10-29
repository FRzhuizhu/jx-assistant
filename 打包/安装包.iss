; 坚祥数字助理GO 安装包脚本（Inno Setup 6.4+，需配合简中语言包 ChineseSimplified.isl）
; 由 打包安装包.ps1 组装 stage/ 后调用 ISCC 编译；也可手工：
;   ISCC.exe /DMyAppVersion=1.0.0 安装包.iss

#ifndef MyAppVersion
#define MyAppVersion "1.0.0"
#endif

#define MyAppName "坚祥数字助理GO"
#define MyAppExeName "坚祥数字助理GO.exe"
// 文件版本号必须是纯数字四段（每段 0-65535）：日期版本 20261013-1（旧写法 20261012V1）转为 2026.10.13.1；
// 传统语义版本（如 1.0.3）沿用原行为补 .0 → 1.0.3.0
#if Pos("V", MyAppVersion) > 0
  #define MIVYear Copy(MyAppVersion, 1, 4)
  #define MIVMonth Copy(MyAppVersion, 5, 2)
  #define MIVDay Copy(MyAppVersion, 7, 2)
  #define MIVRev Copy(MyAppVersion, Pos("V", MyAppVersion) + 1, 8)
  #define MyAppFileVersion MIVYear + "." + MIVMonth + "." + MIVDay + "." + MIVRev
#elif Pos("-", MyAppVersion) > 0
  #define MIVYear Copy(MyAppVersion, 1, 4)
  #define MIVMonth Copy(MyAppVersion, 5, 2)
  #define MIVDay Copy(MyAppVersion, 7, 2)
  #define MIVRev Copy(MyAppVersion, Pos("-", MyAppVersion) + 1, 8)
  #define MyAppFileVersion MIVYear + "." + MIVMonth + "." + MIVDay + "." + MIVRev
#else
  #define MyAppFileVersion MyAppVersion + ".0"
#endif

[Setup]
AppId={{BE7768E7-93C5-49D6-9778-BC133A39A797}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher=坚祥
DefaultDirName={localappdata}\Programs\{#MyAppName}
DefaultGroupName={#MyAppName}
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
OutputDir=输出
OutputBaseFilename={#MyAppName}-安装包-{#MyAppVersion}
SetupIconFile=坚祥数字助理GO.ico
UninstallDisplayName={#MyAppName}
UninstallDisplayIcon={app}\{#MyAppExeName}
VersionInfoVersion={#MyAppFileVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
CloseApplications=no

[Languages]
Name: "chinesesimplified"; MessagesFile: "ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce

[Files]
Source: "stage\{#MyAppExeName}"; DestDir: "{app}"; Flags: ignoreversion
; 技能：只装内置技能（pdf/ocr/office/downloader/apkinfo）与共享运行时；技能\user\ 是
; **用户/市场装的技能**，绝不随包——Excludes 必须留着，否则开发机上的内部技能（含服务器
; 地址、部署命令）会跟着装到客户机器上。口径与静默更新块一致（发布静默更新.ps1 白名单）。
Source: "stage\技能\*"; DestDir: "{app}\技能"; Flags: ignoreversion recursesubdirs createallsubdirs; Excludes: "user\*,user"
; 内置组件必须随包（打包清单纪律 ③）：危险命令护栏 / 待办与定时任务 / Git 面板 /
; 语音（朗读与听写）。程序按 {app}\组件\*/manifest.json 发现组件，装不进就没有
; 默认组件（危险命令护栏会一并失效）；打包脚本已强校验 stage\组件 存在且非空。
Source: "stage\组件\*"; DestDir: "{app}\组件"; Flags: ignoreversion recursesubdirs createallsubdirs
; MD 查看器（装完即有）：单文件 exe 全自包含，随包只带 exe + 使用说明；
; .md 关联由下方 [Run] 装完时 /register 静默写入（HKCU，无需提权）
Source: "stage\md-viewer\*"; DestDir: "{app}\md-viewer"; Flags: ignoreversion
Source: "stage\使用说明.md"; DestDir: "{app}"; Flags: ignoreversion

[InstallDelete]
; 安装期清理（升级时先删后装，防旧文件残留）：只清「随包分发、整体重装」的
; 目录；用户的东西严格不碰——data\、config.json、默认工作区\（含旧名「工作区」）、
; 技能\user\（用户技能）、插件\（用户装的 DSH 插件原件）、组件\<非随包 id>（市场
; 装的组件）一律保留。随包目录的逐项清单由 打包安装包.ps1 按 stage 现货生成
; （清理清单.iss），这里只钉死历史遗留布局：v1.0.2 时代本地语音 worker 装在
; {app}\asr，之后迁入 组件\voice\ 但旧目录会一直残留并曾被误用——下架内置
; 组件/改目录结构时，同样往这里补一条。
Type: filesandordirs; Name: "{app}\asr"
#if FileExists(AddBackslash(SourcePath) + "清理清单.iss")
  #include "清理清单.iss"
#endif

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Parameters: "-desktop"
Name: "{group}\卸载{#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Parameters: "-desktop"; Tasks: desktopicon

[Run]
; 装完/升级后静默注册 .md 关联（幂等；注册的是 exe 自身所在路径），
; /register 纯写注册表即退，不弹任何窗口
Filename: "{app}\md-viewer\MD查看器.exe"; Parameters: "/register"; WorkingDir: "{app}\md-viewer"; Flags: runhidden
Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Description: "运行{#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 卸载前先取消 .md 关联（还原注册时备份的原打开方式）并结束驻留查看器，
; 再结束主程序——exe 被占用会导致文件删不净
Filename: "{app}\md-viewer\MD查看器.exe"; Parameters: "/unregister"; WorkingDir: "{app}\md-viewer"; Flags: runhidden; RunOnceId: "UnregisterMdViewer"
Filename: "taskkill"; Parameters: "/IM MD查看器.exe /F"; Flags: runhidden; RunOnceId: "KillMdViewer"
Filename: "taskkill"; Parameters: "/IM {#MyAppExeName} /F"; Flags: runhidden; RunOnceId: "KillApp"

[UninstallDelete]
; WebView2 缓存是可再生的浏览器数据，卸载时清掉（主界面与右栏浏览器面板两份 profile）；
; 会话库与配置（data\ 其余部分、config.json）保留，不做隐性删除
Type: filesandordirs; Name: "{app}\data\webview"
Type: filesandordirs; Name: "{app}\data\webview-browser"

[Code]
const
  // 默认工作区目录名：新版「默认工作区」，旧版「工作区」（升级安装时原地改名，
  // 用户已有文件一份不动；程序启动时还会再兜一次，见 desktop_windows.go）
  LegacyWorkspaceDirName  = '工作区';
  DefaultWorkspaceDirName = '默认工作区';

// MigrateDefaultWorkspace 归置默认工作区目录：全新安装建好「默认工作区」；
// 升级安装把旧版的「工作区」原地改名为「默认工作区」（保文件不丢）。
procedure MigrateDefaultWorkspace();
var
  oldDir, newDir: String;
begin
  oldDir := AddBackslash(ExpandConstant('{app}')) + LegacyWorkspaceDirName;
  newDir := AddBackslash(ExpandConstant('{app}')) + DefaultWorkspaceDirName;
  if DirExists(oldDir) then begin
    if not DirExists(newDir) then begin
      if RenameFile(oldDir, newDir) then
        Log('默认工作区迁移：' + oldDir + ' -> ' + newDir)
      else
        Log('默认工作区迁移失败（程序下次启动会自动重试）：' + oldDir);
    end;
    Exit;
  end;
  if not DirExists(newDir) then begin
    if not CreateDir(newDir) then
      Log('默认工作区目录创建失败（程序首次启动会自动创建）：' + newDir);
  end;
end;

function WebView2Installed(): Boolean;
var
  pv: String;
begin
  Result :=
    RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', pv) and (pv <> '') and (pv <> '0.0.0.0');
  if not Result then
    Result :=
      RegQueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', pv) and (pv <> '') and (pv <> '0.0.0.0');
  if not Result then
    Result :=
      RegQueryStringValue(HKCU, 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', pv) and (pv <> '') and (pv <> '0.0.0.0');
end;

procedure TaskKillApp();
var
  rc: Integer;
begin
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM {#MyAppExeName} /F', '', SW_HIDE, ewWaitUntilTerminated, rc);
  // 查看器可能以托盘宿主驻留，覆盖安装时同样要腾出 exe
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM MD查看器.exe /F', '', SW_HIDE, ewWaitUntilTerminated, rc);
end;

function InitializeSetup(): Boolean;
var
  rc: Integer;
begin
  Result := True;
  if not WebView2Installed() then begin
    if MsgBox(
        '未检测到 Microsoft WebView2 运行时（Windows 10 / 11 一般自带）。' + #13#10#13#10 +
        '桌面版需要它来显示界面；也可以先完成安装（届时会以浏览器模式打开并给出同样的提示）。' + #13#10#13#10 +
        '是否现在打开微软官方下载页安装 WebView2？',
        mbConfirmation, MB_YESNO) = IDYES then
      ShellExec('open', 'https://go.microsoft.com/fwlink/p/?LinkId=2124703', '', '', SW_SHOWNORMAL, ewNoWait, rc);
  end;
  // 覆盖安装前结束运行中的实例，避免 exe 被占用
  TaskKillApp();
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  TaskKillApp();
  Sleep(1500);
  // 默认工作区归置放在复制文件之前：旧目录已改名，装完即用
  MigrateDefaultWorkspace();
  Result := '';
end;
