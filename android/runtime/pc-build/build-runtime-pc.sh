#!/bin/bash
# arm64 运行时快照的 PC 侧组装脚本（不需要安卓设备/模拟器）。
#
# 在 WSL Ubuntu 里运行：bash android/runtime/pc-build/build-runtime-pc.sh
# 产物：runtime-arm64.tar.gz + sha256（更新 android/runtime/MANIFEST.json 后
#       拷为 android/runtime/runtime.tgz，再走 android/build-android.sh 重打 APK）。
#
# 流程（2026-09-29 首次跑通，产物即快照 v1.1.0）：
#   1. GitHub 下载 termux bootstrap-aarch64.zip（约 33MB；HTTP/2 被干扰时
#      脚本自动改走 ghfast 代理 + HTTP/1.1）
#   2. USTC 镜像取 aarch64 Packages 索引，解析依赖闭包（python 全家 +
#      lxml/pillow/cryptography + openssh + ca-certificates + proot，约 75 包）
#   3. 下载 .deb，dpkg-deb -x 解包后归位合并（嵌套 usr 树拍平进 prefix）
#   4. USTC pypi 镜像下技能轮子直接解压进 site-packages（纯 Python 轮子 +
#      pypdfium2 官方 android_23_arm64_v8a 轮子——绕开 Termux pip 只认
#      linux 标签、拒装安卓轮子的坑）
#   5. 烘焙 USTC 镜像源与 apt.conf（前缀保持 Termux 原生
#      /data/data/com.termux/files/usr，由引擎 termuxRelocate 装配期重写）
#   6. 剪枝（对齐 make-runtime.ps1）→ tar.gz → sha256
#
# 前缀保留 Termux 原生路径是刻意的：引擎 termuxRelocate 本就负责把
# termuxOldPrefix 二次重写为本机 prefix，无需制作期改写。
set -e
cd "$(dirname "$0")"
W=~/jxsnap-pc
mkdir -p "$W" && cd "$W"
BOOTSTRAP_TAG="bootstrap-2026.09.27-r1%2Bapt.android-7"
BOOTSTRAP_URL="https://github.com/termux/termux-packages/releases/download/$BOOTSTRAP_TAG/bootstrap-aarch64.zip"

echo "=== [1] bootstrap（aarch64） ==="
if [ ! -f bootstrap-aarch64.zip ]; then
  curl -L --http1.1 --retry 3 -sS -o bootstrap-aarch64.zip "$BOOTSTRAP_URL" ||
  curl -L --http1.1 --retry 3 -sS -o bootstrap-aarch64.zip "https://ghfast.top/$BOOTSTRAP_URL"
fi
ls -la bootstrap-aarch64.zip

echo "=== [2] 依赖解析（USTC aarch64 索引） ==="
cat > resolve.py <<'PYEOF'
import sys, urllib.request, gzip
base = "http://mirrors.ustc.edu.cn/termux/apt/termux-main/"
raw = urllib.request.urlopen(base + "dists/stable/main/binary-aarch64/Packages.gz", timeout=60).read()
text = gzip.decompress(raw).decode("utf-8")
open("Packages", "w").write(text)
pkgs, provides = {}, {}
for para in text.split("\n\n"):
    if not para.strip(): continue
    fields = {}
    for ln in para.splitlines():
        if ": " in ln:
            k, v = ln.split(": ", 1); fields[k] = v
    n = fields.get("Package")
    if not n: continue
    pkgs[n] = fields
    for prov in fields.get("Provides", "").split(","):
        prov = prov.split("(")[0].strip()
        if prov: provides.setdefault(prov, []).append(n)
roots = sys.argv[1:]
want, missing = {}, []
def real(name):
    if name in pkgs: return name
    c = provides.get(name, [])
    return c[0] if c else None
queue = list(roots)
while queue:
    name = queue.pop(0)
    rn = real(name)
    if rn is None:
        missing.append(name); continue
    if rn in want: continue
    want[rn] = pkgs[rn].get("Filename", "")
    for grp in pkgs[rn].get("Depends", "").split(","):
        grp = grp.strip()
        if not grp: continue
        first = grp.split("|")[0].strip().split("(")[0].strip().split()[0]
        if first: queue.append(first)
print(f"resolved {len(want)} packages, missing: {missing}")
if missing: sys.exit(1)
with open("deblist.txt", "w") as f:
    for n, fn in sorted(want.items()):
        f.write(fn + "\n")
print("python version:", pkgs.get("python", {}).get("Version", "?"))
PYEOF
python3 resolve.py python python-pip python-lxml python-pillow python-cryptography ca-certificates proot openssh

echo "=== [3] 下载 deb 并解包 ==="
mkdir -p debs && cd debs
while read -r f; do
  base="$(basename "$f")"
  [ -f "$base" ] || curl -fsS --retry 2 -O "http://mirrors.ustc.edu.cn/termux/apt/termux-main/$f"
done < ../deblist.txt
echo "debs: $(ls *.deb | wc -l)"
cd ..
rm -rf unpack prefix && mkdir unpack

echo "=== [4] bootstrap 解包 + 符号链接还原 + deb 归位 ==="
python3 - <<'PYEOF'
import os, stat, zipfile
zf = zipfile.ZipFile("bootstrap-aarch64.zip")
n_f = 0
for i in zf.infolist():
    name = i.filename; mode = i.external_attr >> 16
    if stat.S_ISLNK(mode) or name.endswith("/"):
        continue  # 符号链接统一走 SYMLINKS.txt（bootstrap zip 内无链接条目）
    p = os.path.join("prefix", name); os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "wb") as f: f.write(zf.read(i))
    os.chmod(p, (mode & 0o777) or 0o644); n_f += 1
print(f"bootstrap files={n_f}")
made = 0
with open("prefix/SYMLINKS.txt", encoding="utf-8") as f:
    for line in f:
        line = line.rstrip("\n")
        if not line or "←" not in line: continue
        tgt, link = line.split("←", 1)
        p = os.path.join("prefix", link)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        if os.path.lexists(p): os.remove(p)
        os.symlink(tgt, p); made += 1
print(f"symlinks={made}")
PYEOF
for d in debs/*.deb; do dpkg-deb -x "$d" unpack/; done
cp -a unpack/data/data/com.termux/files/usr/. prefix/
rm -rf unpack

echo "=== [5] 技能轮子（USTC pypi，直接解压进 site-packages） ==="
python3 - <<'PYEOF'
import os, re, urllib.request, zipfile
from urllib.parse import urljoin
BASE = "https://mirrors.ustc.edu.cn/pypi/simple/"
WANT = {
    "python-docx": r"py3-none-any\.whl$",
    "openpyxl": r"py3-none-any\.whl$",
    "python-pptx": r"py3-none-any\.whl$",
    "XlsxWriter": r"py3-none-any\.whl$",
    "et-xmlfile": r"py3-none-any\.whl$",
    "typing_extensions": r"py3-none-any\.whl$",
    "pdfplumber": r"py3-none-any\.whl$",
    "pdfminer.six": r"py3-none-any\.whl$",
    "pypdf": r"py3-none-any\.whl$",
    "charset-normalizer": r"py3-none-any\.whl$",
    "pypdfium2": r"py3-none-android_23_arm64_v8a\.whl$",
}
SP = "prefix/lib/python3.14/site-packages"
def vkey(fn):
    m = re.match(r"^[^-]+-([0-9][^-]*)-", fn)
    return tuple(int(x) if x.isdigit() else 0 for x in m.group(1).split(".")) if m else (0,)
os.makedirs("wheels", exist_ok=True)
for pkg, pat in WANT.items():
    idx = "wheels/" + pkg.replace(".", "_") + ".idx"
    if not os.path.exists(idx):
        urllib.request.urlretrieve(BASE + pkg + "/", idx)
    hrefs = re.findall(r'href="([^"]+)"', open(idx, encoding="utf-8").read())
    cands = [h for h in hrefs if re.search(pat, h.split("/")[-1].split("#")[0])]
    if not cands:
        raise SystemExit(f"no wheel for {pkg} matching {pat}")
    best = max(cands, key=lambda h: vkey(h.split("/")[-1]))
    fn = best.split("/")[-1].split("#")[0]
    dst = os.path.join("wheels", fn)
    if not os.path.exists(dst):
        print("download", fn)
        urllib.request.urlretrieve(urljoin(BASE + pkg + "/", best), dst)
    with zipfile.ZipFile(dst) as z:
        z.extractall(SP)
        for i in z.infolist():
            mode = i.external_attr >> 16
            if not i.filename.endswith("/") and mode & 0o777:
                os.chmod(os.path.join(SP, i.filename), mode & 0o777)
print("wheels done")
PYEOF

echo "=== [6] 镜像源与 apt 配置 ==="
echo "deb http://mirrors.ustc.edu.cn/termux/apt/termux-main stable main" > prefix/etc/apt/sources.list
if [ ! -f prefix/etc/apt/apt.conf ]; then
  cat > prefix/etc/apt/apt.conf <<'ACEOF'
Dir::Etc::sourcelist "/data/data/com.termux/files/usr/etc/apt/sources.list";
Dir::Etc::sourceparts "/data/data/com.termux/files/usr/etc/apt/sources.list.d";
Dir::Etc::main "/data/data/com.termux/files/usr/etc/apt/apt.conf";
Dir::Etc::parts "/data/data/com.termux/files/usr/etc/apt/apt.conf.d";
Dir::State "/data/data/com.termux/files/usr/var/lib/apt";
Dir::State::status "/data/data/com.termux/files/usr/var/lib/dpkg/status";
Dir::Cache "/data/data/com.termux/files/usr/var/cache/apt";
Dir::Log "/data/data/com.termux/files/usr/var/log/apt";
Dir::Etc::preferences "/data/data/com.termux/files/usr/etc/apt/preferences";
Dir::Etc::preferencesparts "/data/data/com.termux/files/usr/etc/apt/preferences.d";
Dir::Etc::trusted "/data/data/com.termux/files/usr/etc/apt/trusted.gpg";
Dir::Etc::trustedparts "/data/data/com.termux/files/usr/etc/apt/trusted.gpg.d";
Dir::Bin::methods "/data/data/com.termux/files/usr/lib/apt/methods";
Dir::Bin::apt-key "/data/data/com.termux/files/usr/bin/apt-key";
ACEOF
fi

echo "=== [7] 剪枝（对齐 make-runtime.ps1） ==="
rm -rf prefix/lib/clang prefix/lib/cmake prefix/include \
  prefix/share/doc prefix/share/man prefix/share/info prefix/share/gtk-doc \
  prefix/share/aclocal prefix/share/gettext
rm -rf prefix/lib/python3.14/test prefix/lib/python3.14/idlelib \
  prefix/lib/python3.14/tkinter prefix/lib/python3.14/turtledemo \
  prefix/lib/python3.14/lib2to3
rm -f prefix/lib/libLLVM* prefix/lib/libclang* prefix/lib/libcompiler_rt* \
  prefix/lib/liblldb* prefix/bin/clang* prefix/bin/llvm-* prefix/bin/ld.lld* \
  prefix/bin/lld* prefix/bin/wasm-ld*
find prefix -name __pycache__ -type d -prune -exec rm -rf {} + 2>/dev/null || true
find prefix -name '*.pyc' -delete 2>/dev/null || true

echo "=== [8] 架构校验 + 打包 ==="
m=$(od -A n -t x1 -j 18 -N 2 prefix/bin/bash | tr -d ' \n')
[ "$m" = "b700" ] || { echo "FATAL: bin/bash 不是 aarch64 (e_machine=$m)"; exit 1; }
echo "bash e_machine=$m (aarch64 OK)"
rm -f runtime-arm64.tar.gz
cd prefix && tar -cf - . | gzip -6 > ../runtime-arm64.tar.gz && cd ..
ls -la runtime-arm64.tar.gz
sha256sum runtime-arm64.tar.gz
echo "完成：更新 android/runtime/MANIFEST.json（version/size/sha256）后把 runtime-arm64.tar.gz 拷为 android/runtime/runtime.tgz"
