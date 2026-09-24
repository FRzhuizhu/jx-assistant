; 坚祥数字助理GO 安装包脚本（Inno Setup 6.4+，需配合简中语言包 ChineseSimplified.isl）
; 由 打包安装包.ps1 组装 stage/ 后调用 ISCC 编译；也可手工：
;   ISCC.exe /DMyAppVersion=1.0.0 安装包.iss

#ifndef MyAppVersion
#define MyAppVersion "1.0.0"
#endif

#define MyAppName "坚祥数字助理GO"
#define MyAppExeName "坚祥数字助理GO.exe"

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
VersionInfoVersion={#MyAppVersion}.0
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
Source: "stage\技能\*"; DestDir: "{app}\技能"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "stage\使用说明.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Parameters: "-desktop"
Name: "{group}\卸载{#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Parameters: "-desktop"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; WorkingDir: "{app}"; Description: "运行{#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 卸载前先结束运行中的程序（exe 被占用会导致文件删不净）
Filename: "taskkill"; Parameters: "/IM {#MyAppExeName} /F"; Flags: runhidden; RunOnceId: "KillApp"

[UninstallDelete]
; WebView2 缓存是可再生的浏览器数据，卸载时清掉；
; 会话库与配置（data\ 其余部分、config.json）保留，不做隐性删除
Type: filesandordirs; Name: "{app}\data\webview"

[Code]
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
  Result := '';
end;
