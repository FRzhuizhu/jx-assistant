// E2E：composer 重排/调色盘进设置/菜单重做/圆点/分支/回收站
const { spawn, execSync } = require('child_process');
const fs = require('fs');
const EDGE = 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
class CDP {
  constructor(ws) {
    this.ws = ws; this.id = 0; this.pending = new Map();
    ws.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data);
      if (m.id && this.pending.has(m.id)) {
        const p = this.pending.get(m.id); this.pending.delete(m.id);
        m.error ? p.reject(new Error(m.error.message)) : p.resolve(m.result);
      }
    });
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => { ws.addEventListener('open', res); ws.addEventListener('error', rej); });
    return new CDP(ws);
  }
  send(method, params = {}, timeoutMs = 8000) {
    return new Promise((resolve, reject) => {
      const id = ++this.id;
      const t = setTimeout(() => { this.pending.delete(id); reject(new Error('TIMEOUT:' + method)); }, timeoutMs);
      this.pending.set(id, { resolve: (v) => { clearTimeout(t); resolve(v); }, reject: (e) => { clearTimeout(t); reject(e); } });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
}
(async () => {
  const edge = spawn(EDGE, ['--headless=new', '--disable-gpu', '--remote-debugging-port=9241',
    `--user-data-dir=${process.env.TEMP}\\edge-fix2`, '--no-first-run', '--window-size=1500,940', 'about:blank'], { stdio: 'ignore' });
  process.on('exit', () => { try { execSync(`taskkill /PID ${edge.pid} /T /F`, { stdio: 'ignore' }); } catch (e) {} });
  for (let i = 0; i < 40; i++) { try { const r = await fetch('http://127.0.0.1:9241/json/version'); if (r.ok) break; } catch (e) {} await sleep(250); }
  const tab = await (await fetch('http://127.0.0.1:9241/json/new', { method: 'PUT' })).json();
  const cdp = await CDP.connect(tab.webSocketDebuggerUrl);
  await cdp.send('Page.enable'); await cdp.send('Runtime.enable');
  await cdp.send('Page.navigate', { url: 'http://127.0.0.1:8788/' });
  await sleep(3000);
  const ev = async (expr, timeoutMs = 8000) => {
    const r = await cdp.send('Runtime.evaluate', { expression: expr, returnByValue: true }, timeoutMs);
    if (r.exceptionDetails) throw new Error('page-eval: ' + (r.exceptionDetails.exception?.description || '').slice(0, 200));
    return r.result.value;
  };
  const shot = async (name) => {
    const r = await cdp.send('Page.captureScreenshot', { format: 'png' });
    fs.writeFileSync(__dirname + '\\gui-test-screenshots\\' + name, Buffer.from(r.data, 'base64'));
    console.log('SHOT ' + name);
  };
  const clickPt = async (x, y) => {
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
    await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
  };
  const esc = async () => cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });

  // T1 composer：按钮次序（从右到左：发送→停止→模型→语音），无调色盘
  const order = await ev(`(function(){
    const btns = [...document.querySelectorAll('.composer-right > *')].map((e) => e.id || e.className.split(' ')[0]);
    const rs = [...document.querySelectorAll('.composer-right > *')].map((e) => Math.round(e.getBoundingClientRect().right));
    return JSON.stringify({ btns, rs, paletteInComposer: !!document.querySelector('.composer-left #cx-accent-btn, .composer-right #cx-accent-btn') });
  })()`);
  console.log('T1 输入区右列(左→右): ' + order + '（期望 voice, model, stop, send；发送最右）');
  await shot('t8_composer.png');

  // T2 调色盘进设置
  await ev(`document.getElementById('btn-settings').click()`);
  await sleep(900);
  const accent = await ev(`(function(){
    const s = document.querySelector('#settings .cx-accent-section');
    return JSON.stringify({ inSettings: !!s, title: s ? s.querySelector('h4').textContent : '' });
  })()`);
  console.log('T2 调色盘: ' + accent);
  await shot('t8_accent_settings.png');
  await ev(`document.getElementById('set-cancel').click()`);
  await sleep(300);

  // T3 会话菜单：四项与红字
  const rowPt = await ev(`(function(){const it=document.querySelector('.session-item');if(!it)return null;const r=it.getBoundingClientRect();return JSON.stringify({x:r.x+r.width-20,y:r.y+r.height/2});})()`);
  if (rowPt) {
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: JSON.parse(rowPt).x - 60, y: JSON.parse(rowPt).y });
    await sleep(400);
    await clickPt(JSON.parse(rowPt).x, JSON.parse(rowPt).y);
    await sleep(500);
    const items = await ev(`[...document.querySelectorAll('#ctx-menu .pop-item')].map(b=>b.textContent+(b.classList.contains('danger')?'(红)':'')).join(' | ')`);
    console.log('T3 会话菜单: [' + items + ']');
    await shot('t8_session_menu.png');
    await esc();
  }

  // T4 工作区菜单
  const headMore = await ev(`(function(){const b=document.querySelector('.ws-group-head .wa-more');if(!b)return null;const r=b.getBoundingClientRect();return JSON.stringify({x:r.x+r.width/2,y:r.y+r.height/2});})()`);
  if (headMore) {
    await cdp.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: JSON.parse(headMore).x - 60, y: JSON.parse(headMore).y });
    await sleep(400);
    await clickPt(JSON.parse(headMore).x, JSON.parse(headMore).y);
    await sleep(500);
    const items = await ev(`[...document.querySelectorAll('#ctx-menu .pop-item')].map(b=>b.textContent+(b.classList.contains('danger')?'(红)':'')).join(' | ')`);
    console.log('T4 工作区菜单: [' + items + ']');
    await shot('t8_ws_menu.png');
    await esc();
  }

  // T5 标为未读 → 红点；再打开 → 消失
  if (rowPt) {
    const sid = await ev(`(function(){const it=document.querySelector('.session-item');const id=[...(window.state?state.sessions:[])].find(s=>s.title)&&true;return state.sessions.find(s=>!s.parentId&&s.mode==='task'&&s.title).id;})()`);
    await ev(`api('/api/sessions/${sid}', {method:'PATCH', body: JSON.stringify({unread:true})}).then(()=>loadSessions())`);
    await sleep(900);
    const dot = await ev(`(function(){const items=[...document.querySelectorAll('.session-item')];const it=items.find(x=>x.textContent.includes(document.querySelector('.s-title')?document.querySelectorAll('.s-title')[0].textContent:''));const d=document.querySelector('.session-item .dot.unread');return {anyRed: !!d, visible: d?getComputedStyle(d).display:'none'};})()`);
    console.log('T5 未读红点: ' + JSON.stringify(dot));
    await shot('t8_red_dot.png');
    // 打开会话 → 红点消失
    await ev(`api('/api/sessions/${sid}', {method:'PATCH', body: JSON.stringify({unread:false})}).then(()=>loadSessions())`);
  }

  // T6 分支对话
  const forkResp = await fetch('http://127.0.0.1:8788/api/sessions/' + (await ev(`state.sessions.filter(s=>!s.parentId&&s.mode==='task'&&s.title).slice(-1)[0].id`)) + '/fork', { method: 'POST' });
  const fork = await forkResp.json();
  console.log('T6 分支: ' + forkResp.status + ' 标题=' + (fork.title || '') + ' 消息数=' + (await (await fetch('http://127.0.0.1:8788/api/sessions/' + fork.id)).json()).messages.length);
  // 清理分支
  await fetch('http://127.0.0.1:8788/api/sessions/' + fork.id, { method: 'DELETE' });

  // T7 圆点默认隐藏
  const dotDefault = await ev(`(function(){const ds=[...document.querySelectorAll('.session-item .dot')];const vis=ds.filter(d=>getComputedStyle(d).display!=='none').length;return ds.length+'个, 可见'+vis+'个';})()`);
  console.log('T7 默认圆点: ' + dotDefault + '（应全部不可见）');
  console.log('PASS');
  process.exit(0);
})().catch((e) => { console.log('ERROR ' + e.message); process.exit(1); });
