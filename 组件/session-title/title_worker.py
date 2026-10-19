# -*- coding: utf-8 -*-
"""title_worker.py —— 「AI 自动会话标题」组件的后端 worker（C 形态，帧协议）。

机制（对齐老 DSH session-title 的文档决策，节奏 = all-prompts：每条新用户
提示词处理完后改写一次）：
  · 每轮对话结束（run.end 通知型钩子，一轮 = 一条用户消息触发的执行含排队
    续跑）后，取「截至当前的全部用户消息」，经主程序 POST /api/llm/ask
    独立调一次模型生成/修订标题——一次发送、一次输出（单轮，不与模型多轮
    对话），不进会话上下文；worker 经反向调用主程序 API 干活，自身不持有
    任何模型密钥；
  · 标题增量演化：保留最初核心，语义合并旧标题与新主题，合不拢用「＋」连接，
    开头保留最初主题代表词、结尾保留最新主题代表词——规则写进系统提示词
    TITLE_SYSTEM_PROMPT；
  · 用户手动改过标题的会话（主程序 PATCH 打钉）不再自动改：改标题请求带
    autoTitle 标记，主程序对钉住会话回 409，本 worker 静默跳过。

帧协议（见 组件/README.md）——stdin 逐行收：
  {"type":"event","id":..,"event":"run.end","data":{"sessionID":..,"status":..}}  钩子事件
  {"type":"hostres","id":..,"status":200,"body":{..}}                            反向调用应答
worker 发：
  {"type":"host","id":..,"method":"GET|POST|PATCH","path":"/api/..","body":{..}}  反向调用
通知型事件不需要回 decision。全部业务在工作线程异步串行：同一会话只保留
最新一次待处理（去抖），任何失败只记 stderr（桥转主程序日志），绝不影响
主流程。
"""
import json
import sys
import threading

try:  # Windows 管道下默认编码不确定，显式钉死 UTF-8（桥按 UTF-8 逐行读写）
    sys.stdin.reconfigure(encoding="utf-8", errors="replace")
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

# ---------- 提示词（增量演化规则在这里，改规则只改本组件） ----------

TITLE_SYSTEM_PROMPT = """你是会话标题撰写员。根据一个会话的用户消息列表（按时间先后）与当前标题，输出该会话的新标题。
规则：
- 用用户消息的主要语言书写（中文为主时写中文标题）。
- 标题保持简短：中文不超过 20 个字，英文不超过 12 个单词。
- 当前标题为空（首次命名）：概括会话的核心意图，优先反映第一条用户消息的主题。
- 已有标题（增量修订）：在旧标题的基础上演化，不要推倒重来——
  · 旧标题中仍然成立的核心要保留，尤其是最初的主题词（保持标题开头的延续）；
  · 后续对话引入了新主题时，把新主题合并进标题：能自然合并成一句就合并成一句；
  · 无法自然合并的，用全角加号「＋」连接，如「旧主题＋新主题」；
  · 无论哪种写法，开头保留最初主题的代表词，结尾保留最新主题的代表词；
  · 已被合并表述覆盖的旧内容可以删去，控制总长不超上限。
- 只输出标题本身：不要引号、不要书名号、不要句末标点、不要解释、不要任何多余文字。"""

# 输入预算：单条用户消息截断（字符）、全部消息总预算（字符）。超总预算时
# 丢中间的旧消息——首条（最初主题）与最新的若干条必须保留。
PER_MSG_CHARS = 400
TOTAL_BUDGET_CHARS = 9000
# 标题清洗上限：与主程序 PATCH 的 truncateRunes(_, 40) 同口径。
TITLE_MAX_CHARS = 40

# ---------- 帧协议 ----------

_out_lock = threading.Lock()
_waiters = {}          # host 帧 id -> [threading.Event, hostres dict]
_waiters_lock = threading.Lock()
_seq = 0


def _next_id():
    global _seq
    with _waiters_lock:
        _seq += 1
        return "h%d" % _seq


def log(msg):
    print("[title-worker] %s" % msg, file=sys.stderr, flush=True)


def send_frame(obj):
    line = json.dumps(obj, ensure_ascii=False)
    with _out_lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def host_call(method, path, body=None, timeout=60):
    """反向调用主程序 API（桥侧 60 秒兜底，这里同档）。返回 hostres dict 或 None。"""
    hid = _next_id()
    ev = threading.Event()
    with _waiters_lock:
        _waiters[hid] = [ev, None]
    try:
        send_frame({"type": "host", "id": hid, "method": method, "path": path,
                    "body": body if body is not None else {}})
        if not ev.wait(timeout):
            log("反向调用超时: %s %s" % (method, path))
            return None
        with _waiters_lock:
            return _waiters.pop(hid)[1]
    finally:
        with _waiters_lock:
            _waiters.pop(hid, None)


def dispatch_hostres(frame):
    hid = frame.get("id")
    with _waiters_lock:
        rec = _waiters.get(hid)
    if rec:
        rec[1] = frame
        rec[0].set()


# ---------- 业务 ----------

_pending = {}            # sessionID -> True（去抖：同会话只留一次待处理）
_pending_lock = threading.Lock()
_wake = threading.Condition()


def enqueue(sid):
    with _pending_lock:
        _pending[sid] = True
    with _wake:
        _wake.notify()


def take_one():
    with _wake:
        while not _pending:
            _wake.wait()
    with _pending_lock:
        sid = next(iter(_pending))
        del _pending[sid]
    return sid


def clean_title(raw):
    t = (raw or "").strip()
    wrap = "\"'‘’“”「」『』《》*#`"
    while t and t[0] in wrap:
        t = t[1:]
    while t and t[-1] in wrap:
        t = t[:-1]
    t = " ".join(t.split())  # 折叠换行与多余空白
    return t[:TITLE_MAX_CHARS]


def build_user_prompt(old_title, texts):
    def cut(t):
        t = " ".join(t.split())
        return t if len(t) <= PER_MSG_CHARS else t[:PER_MSG_CHARS] + "…"

    first = cut(texts[0])
    rest = [cut(t) for t in texts[1:]]
    chosen = []
    budget = TOTAL_BUDGET_CHARS - len(first)
    for t in reversed(rest):  # 从最新往回装，装不下即止
        if budget - len(t) < 0:
            break
        chosen.insert(0, t)
        budget -= len(t) + 4
    dropped = len(rest) - len(chosen)
    seq = [first] + (["（中间 %d 条较早消息省略）" % dropped] if dropped > 0 else []) + chosen
    lines = ["当前标题：" + (old_title or "（无）"),
             "用户消息（按时间先后，过长的已截断）："]
    lines += ["[%d] %s" % (i + 1, t) for i, t in enumerate(seq)]
    return "\n".join(lines)


def process_session(sid):
    try:
        info = host_call("GET", "/api/sessions/" + sid)
        if not info or info.get("status") != 200:
            return  # 会话不存在/服务忙：静默（下一轮会再试）
        body = info.get("body") or {}
        meta = body.get("meta") or {}
        if meta.get("titlePinned"):
            return  # 用户手动命名钉住：不自动改（主程序侧 409 也会兜底）
        old_title = (meta.get("title") or "").strip()

        mr = host_call("GET", "/api/sessions/%s/messages?role=user" % sid)
        if not mr or mr.get("status") != 200:
            return
        texts = [m.get("text") or "" for m in ((mr.get("body") or {}).get("messages") or [])]
        texts = [t for t in texts if t.strip()]
        if not texts:
            return  # 还没有可概括的用户消息

        ar = host_call("POST", "/api/llm/ask", {
            "session": sid, "system": TITLE_SYSTEM_PROMPT,
            "user": build_user_prompt(old_title, texts)})
        if not ar or ar.get("status") != 200:
            log("生成标题失败: %s" % (ar and ar.get("status")))
            return
        title = clean_title((ar.get("body") or {}).get("text") or "")
        if not title or title == old_title:
            return  # 空/未变：不落库

        pr = host_call("PATCH", "/api/sessions/" + sid,
                       {"title": title, "autoTitle": True})
        if pr and pr.get("status") == 200:
            log("会话 %s 标题 → %s" % (sid, title))
        elif pr and pr.get("status") == 409:
            pass  # 钉住拒绝：正常静默
        else:
            log("改标题失败: %s" % (pr and pr.get("status")))
    except Exception as e:  # 任何异常都不能带崩 worker
        log("处理会话 %s 异常: %r" % (sid, e))


def worker_loop():
    while True:
        sid = take_one()
        process_session(sid)


# ---------- 主线程：读 stdin 分发帧 ----------

def main():
    threading.Thread(target=worker_loop, daemon=True).start()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            f = json.loads(line)
        except Exception:
            continue
        t = f.get("type")
        if t == "event" and f.get("event") == "run.end":
            sid = (f.get("data") or {}).get("sessionID")
            if sid:
                enqueue(sid)  # done/stopped/error 都处理：用户消息已存在即该反映
        elif t == "hostres":
            dispatch_hostres(f)
        elif t == "req":
            # 本 worker 未声明 call 端点；兜底应答防误配
            send_frame({"type": "res", "id": f.get("id"), "body": {"ok": True}})


if __name__ == "__main__":
    main()
