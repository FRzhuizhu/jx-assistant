# -*- coding: utf-8 -*-
"""counter_worker.py —— input-counter 组件的自包含后端（桥 worker）。

协议：一行一答（json-lines，与语音 asr_worker 同规约）。
  {"cmd":"stats","text":"..."} → {"ok":true,"chars":n,"cjk":n,"words":n,"bytes":n}
纯统计逻辑，无模型无状态——这个组件的存在意义是验证声明式桥的
发现/拉起/转发与市场免编译安装链路，业务上确实没啥用。"""
import json
import re
import sys


def stats(text):
    return {
        "ok": True,
        "chars": len(text),
        "cjk": len(re.findall(r"[\u4e00-\u9fff\u3400-\u4dbf]", text)),
        "words": len(re.findall(r"[A-Za-z0-9]+", text)),
        "bytes": len(text.encode("utf-8")),
    }


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
            if req.get("cmd") == "stats":
                out = stats(req.get("text") or "")
            elif req.get("cmd") == "shutdown":
                print(json.dumps({"ok": True}, ensure_ascii=False), flush=True)
                return
            else:
                out = {"ok": False, "err": f"未知命令: {req.get('cmd')}"}
        except Exception as e:  # noqa: BLE001
            out = {"ok": False, "err": f"{type(e).__name__}: {e}"}
        print(json.dumps(out, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
