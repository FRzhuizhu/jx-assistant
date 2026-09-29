# Phase 1.1 FINAL pipeline: build the complete runtime snapshot in one shot.
# rewrite -> apt(USTC) update -> apt -d download -> relocate nested deb trees ->
# rewrite -> pip(USTC) -> rewrite -> verify -> clean -> prune -> tar.gz
# ASCII-only. Run: powershell -NoProfile -ExecutionPolicy Bypass -File android-poc\tools\p1-runtime-build.ps1
$ErrorActionPreference = 'Continue'
$adb = 'D:\Android\Sdk\platform-tools\adb.exe'
$dev = 'emulator-5554'

$P = '/data/user/0/com.jianxiang.poc/files/t'
$O = '/data/data/com.termux/files/usr'

$aptConf = @"
Dir::Etc::sourcelist "$P/etc/apt/sources.list";
Dir::Etc::sourceparts "$P/etc/apt/sources.list.d";
Dir::Etc::main "$P/etc/apt/apt.conf";
Dir::Etc::parts "$P/etc/apt/apt.conf.d";
Dir::State "$P/var/lib/apt";
Dir::State::status "$P/var/lib/dpkg/status";
Dir::Cache "$P/var/cache/apt";
Dir::Log "$P/var/log/apt";
Dir::Etc::preferences "$P/etc/apt/preferences";
Dir::Etc::preferencesparts "$P/etc/apt/preferences.d";
Dir::Etc::trusted "$P/etc/apt/trusted.gpg";
Dir::Etc::trustedparts "$P/etc/apt/trusted.gpg.d";
Dir::Bin::methods "$P/lib/apt/methods";
Dir::Bin::apt-key "$P/bin/apt-key";
"@
$b1 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($aptConf))

$sources = "deb http://mirrors.ustc.edu.cn/termux/apt/termux-main stable main`n"
$b2 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($sources))

$pyCode = @"
import sys, docx, openpyxl, pptx
from docx import Document
print(sys.version.split()[0], sys.prefix)
d = Document()
d.add_paragraph("runtime snapshot ok")
d.save("$P/home/sample.docx")
print("DOCX-OK")
try:
    import pypdfium2, pdfplumber, pypdf
    print("PDF-OK")
except Exception as e:
    print("PDF-SKIP:", e)
"@
$bpy = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($pyCode))

$rw = "grep -RIl $O `$P/bin `$P/libexec `$P/etc 2>/dev/null | xargs -r sed -i s#$O#`$P#g; "

$cmd = "P=$P; export P; export APT_CONFIG=`$P/etc/apt/apt.conf; export DPKG_ROOT=`$P; export SSL_CERT_FILE=`$P/etc/tls/cert.pem; " +
  "echo ===REWRITE0===; $rw " +
  "echo $b1 | base64 -d > `$P/etc/apt/apt.conf; " +
  "echo $b2 | base64 -d > `$P/etc/apt/sources.list; " +
  "mkdir -p `$P/etc/apt/preferences.d `$P/etc/apt/trusted.gpg.d `$P/var/log/apt `$P/var/lib/apt/lists/partial `$P/var/cache/apt/archives/partial `$P/tmp; " +
  "echo ===UPDATE===; apt update 2>&1 | tail -3; " +
  "echo ===DOWNLOAD===; apt install -y -d python python-pip python-lxml python-pillow ca-certificates proot 2>&1 | tail -3; " +
  "echo ===UNPACK===; for f in `$P/var/cache/apt/archives/*.deb; do dpkg-deb -x `$f `$P || echo DKFAIL `$f; done; " +
  "echo ===RELOCATE===; cp -a `$P/data/data/com.termux/files/usr/. `$P/ && echo MOVED && rm -rf `$P/data; " +
  "echo ===REWRITE1===; $rw " +
  "echo ===PIP===; pip install -i https://mirrors.ustc.edu.cn/pypi/simple/ python-docx openpyxl python-pptx pypdfium2 pdfplumber pypdf 2>&1 | tail -5; " +
  "echo ===REWRITE2===; $rw " +
  "echo $bpy | base64 -d > `$P/tmp/verify.py; " +
  "echo ===VERIFY===; python3 `$P/tmp/verify.py 2>&1 | tail -4; " +
  "echo ===CLEAN===; rm -rf `$P/var/cache/apt/archives/*.deb `$P/tmp/* `$P/home/sample.docx; " +
  "echo ===PRUNE===; rm -rf `$P/lib/clang `$P/lib/cmake `$P/include `$P/share/doc `$P/share/man `$P/share/info `$P/share/gtk-doc `$P/share/aclocal `$P/share/gettext; " +
  "rm -rf `$P/lib/python3.14/test `$P/lib/python3.14/idlelib `$P/lib/python3.14/tkinter `$P/lib/python3.14/turtledemo `$P/lib/python3.14/lib2to3; " +
  "rm -f `$P/lib/libLLVM* `$P/lib/libclang* `$P/lib/libcompiler_rt* `$P/lib/liblldb* `$P/bin/clang* `$P/bin/llvm-* `$P/bin/ld.lld* `$P/bin/lld* `$P/bin/wasm-ld*; " +
  "echo ===PYC-CLEAN===; find `$P -name __pycache__ -type d -prune -exec rm -rf {} + 2>/dev/null; find `$P -name '*.pyc' -delete 2>/dev/null; echo PYC-CLEANED; " +
  "echo ===SELFCHECK===; apt --version 2>&1 | head -1; python3 --version 2>&1 | head -1; " +
  "echo ===SIZE===; du -sh `$P | tail -1; " +
  "echo ===TAR===; cd `$P && rm -f ../runtime.tar.gz && tar -c . | /system/bin/toybox gzip > ../runtime.tar.gz && echo TAR-OK; " +
  "echo ===SHA===; sha256sum ../runtime.tar.gz; ls -la ../runtime.tar.gz"

$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($cmd))
Write-Output '[p1-runtime-build] full pipeline running (download ~124MB + pip + pack) ...'
& $adb -s $dev logcat -c | Out-Null
& $adb -s $dev shell am start -n com.jianxiang.poc/.CmdActivity -e cmd64 $b64 | Out-Null
Start-Sleep -Seconds 480
& $adb -s $dev logcat -d -s JXCMD:* | Select-Object -Last 30
