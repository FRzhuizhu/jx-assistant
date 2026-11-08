// 组件 user-memory「AI 记忆与拟人化」的浏览器半边：
//   - 设置 → AI 记忆与拟人化 独立设置页（CX.registerSettings pane）：AI 对话
//     气泡颜色选择 + 用户称呼 / 用户身份与喜好 / AI 助理名字 / AI 助理身份；
//   - 长度约束（1000 字/字段）在前端把关：输入超长红字告知，保存按截断版
//     提交（保存成功但提示被截断；浏览器里保留完整草稿不动——存不下的
//     部分下次打开设置页时看到的是已存的截断版）；
//   - 气泡颜色是本地生效的前端设置：读记忆文档里的 bubbleColor，往
//     <html> 注入 CSS 变量 --ai-bubble（web/style.css 的 .msg.user.ai 气泡
//     引用它，缺省回退主题灰）。保存后即时应用，组件卸载即还原默认灰。
// 设置页 UI 全部复用主站全局类，不依赖其它组件的样式（也不带 client.css）：
// 色板=主题色那套（.acc-swatch/.cur），输入框=网络访问页那套
// （.set-field/.sf-label/.pf-input），按钮 .mc-btn、说明 .sf-desc/.pf-note——
// 深浅色主题随全站 CSS 变量自动协调。
// 数据全部走主程序的 GET/POST /api/user-memory（usermemory.go，同一份
// md 文档，数字经理经 call_api 也走它；服务端对超长字段整单报错——前端
// 提交前已截断，正常不会触发）。
(function () {
  'use strict';

  var LIMIT = 1000; // 与后端 userMemoryFieldLimit 一致
  var CSS_VAR = '--ai-bubble';

  // 预设色板（浅色系与亮色主题搭；暗色主题下自定义色自担对比度）；空串=默认灰。
  // 交互与 设置→常规→主题色 同一套色板组件（style.css 的 .acc-swatch 圆点 +
  // .cur 选中描边 + 自定义取色器），仅色值清单不同：气泡色第一位是灰（跟随
  // 主题），主题色板第一位是主题蓝。
  var PRESETS = [
    { c: '', n: '默认灰（跟随主题）' },
    { c: '#dbeafe', n: '浅蓝' },
    { c: '#e0f2fe', n: '浅青' },
    { c: '#dcfce7', n: '浅绿' },
    { c: '#fef3c7', n: '浅黄' },
    { c: '#ffedd5', n: '浅橙' },
    { c: '#ede9fe', n: '浅紫' },
    { c: '#fce7f3', n: '浅粉' },
  ];

  function runeLen(s) { return Array.from(s == null ? '' : String(s)).length; }

  function clamp(s) {
    var a = Array.from(String(s == null ? '' : s));
    return a.length > LIMIT ? a.slice(0, LIMIT).join('') : a.join('');
  }

  function apiErr(res, text) {
    try {
      var j = JSON.parse(text || '');
      if (j && j.error) return j.error;
    } catch (e) { /* 非 JSON 错误体 */ }
    return 'HTTP ' + res.status;
  }

  // 气泡颜色注入/还原（记在 <html> 上，主对话/子对话/任务会话全部视图共享）
  function setColor(c) {
    var root = document.documentElement;
    c = (c || '').trim();
    if (/^#[0-9a-fA-F]{3,8}$/.test(c)) root.style.setProperty(CSS_VAR, c);
    else root.style.removeProperty(CSS_VAR);
  }

  function loadMemory() {
    return fetch('/api/user-memory').then(function (res) {
      if (!res.ok) throw new Error(apiErr(res, ''));
      return res.json();
    });
  }

  function saveMemory(body) {
    return fetch('/api/user-memory', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(function (res) {
      if (!res.ok) return res.text().then(function (t) { throw new Error(apiErr(res, t)); });
      return res.json();
    });
  }

  /* ================= 设置页 ================= */

  function mountSettings(el) {
    el.innerHTML = '';

    // 说明行 / 分区：全站样式（网络访问页同款结构）
    var note = function (text) {
      var p = document.createElement('p');
      p.className = 'sf-desc';
      p.textContent = text;
      el.appendChild(p);
      return p;
    };
    var group = function (title) {
      var g = document.createElement('div');
      g.className = 'gen-section';
      var h = document.createElement('h4');
      h.textContent = title;
      g.appendChild(h);
      el.appendChild(g);
      return g;
    };

    var state = { userName: '', userMemory: '', aiName: '', aiIdentity: '', bubbleColor: '' };

    // ---- AI 对话气泡颜色（本地生效的前端设置，也存记忆文档） ----
    group('AI 对话气泡颜色');
    note('AI 发出的消息气泡当前是默认灰，可换成喜欢的颜色；只影响本地界面的显示。');

    // 即时预览块：一个左对齐「AI 消息」气泡，跟随当前选择变色
    var preview = document.createElement('div');
    preview.style.cssText = 'margin:6px 0 10px;max-width:320px;padding:10px 15px;border-radius:14px 14px 14px 4px;line-height:1.6;font-size:.95em;background:var(--ai-bubble, var(--chip));transition:background .2s;';
    preview.textContent = '小助理：这是 AI 消息气泡的颜色预览。';
    el.appendChild(preview);

    // 色板 + 自定义取色器：与 设置→常规→主题色 同一套色板组件
    // （.acc-swatch 圆点 + .cur 选中描边，style.css 全局类；容器布局照
    // #accent-row 复刻——它是按 id 选择器写的，这里不占那个 id）
    var pickerBox = document.createElement('div');
    pickerBox.style.cssText = 'display:flex;gap:8px;align-items:center;flex-wrap:wrap;padding:2px 0 4px;';
    var markSel = function () {
      pickerBox.querySelectorAll('.acc-swatch').forEach(function (b) {
        if (b.dataset.cxp === state.bubbleColor) b.classList.add('cur');
        else b.classList.remove('cur');
      });
    };
    PRESETS.forEach(function (p) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'acc-swatch';
      b.dataset.cxp = p.c;
      b.title = p.n;
      b.setAttribute('aria-label', '气泡颜色：' + p.n);
      b.style.background = p.c || 'var(--chip)';
      b.onclick = function () { setDraft({ bubbleColor: p.c }); };
      pickerBox.appendChild(b);
    });
    var custom = document.createElement('input');
    custom.type = 'color';
    custom.title = '自定义颜色';
    custom.value = /^#[0-9a-fA-F]{6}$/.test(state.bubbleColor) ? state.bubbleColor : '#dbeafe';
    // 取色器外观与主题色板那枚同款（#accent-row input[type=color] 的规则）
    custom.style.cssText = 'width:24px;height:24px;border:1px solid var(--border);border-radius:6px;background:none;padding:0;cursor:pointer;';
    custom.oninput = function () { setDraft({ bubbleColor: custom.value }); };
    pickerBox.appendChild(custom);
    el.appendChild(pickerBox);
    note('色板第一格「默认灰」= 跟随主题；色板外可用取色器自定义。');

    // ---- 记忆文本字段 ----
    group('用户与 AI 的长期设定');
    note('这些内容会注入每个对话的环境提示词：AI 据此称呼你、自称与定位。各栏上限 1000 字：超出的部分保存时按前 1000 字入库（这里会提示被截断，输入框保留你的完整草稿）；AI 写入超长会直接报错。任务对话中 AI 可用 user_memory 工具更新「用户身份与喜好」一栏；数字经理可读写整个记忆文件。');

    // 字段行：网络访问页同款（.set-field + .sf-label + .pf-input，圆角边框、
    // 聚焦描边主题色，深浅色主题随全站变量自动协调）
    function textField(label, key, multiLine) {
      var box = document.createElement('div');
      box.className = 'set-field';
      var lab = document.createElement('div');
      lab.className = 'sf-label';
      lab.textContent = label;
      box.appendChild(lab);
      var ta;
      if (multiLine) {
        ta = document.createElement('textarea');
        ta.rows = 4;
        ta.style.cssText = 'resize:vertical;min-height:70px;line-height:1.6;font-family:inherit;';
      } else {
        ta = document.createElement('input');
        ta.type = 'text';
        ta.autocomplete = 'off';
      }
      ta.className = 'pf-input';
      ta.addEventListener('input', function () { updCount(); });
      box.appendChild(ta);
      var cnt = document.createElement('p');
      cnt.className = 'sf-desc';
      cnt.style.cssText = 'text-align:right;margin:4px 0 0;';
      box.appendChild(cnt);
      var updCount = function () {
        var n = runeLen(ta.value);
        cnt.textContent = n + ' / ' + LIMIT;
        // 超长红字：告知保存口径（截断入库），草稿原样保留
        if (n > LIMIT) {
          cnt.textContent = n + ' / ' + LIMIT + '（超出 ' + (n - LIMIT) + ' 字：保存时只保留前 ' + LIMIT + ' 字）';
          cnt.style.color = 'var(--danger)';
        } else {
          cnt.style.color = '';
        }
      };
      updCount();
      el.appendChild(box);
      fields[key] = { el: ta, cnt: cnt, upd: updCount };
    }

    var fields = {};
    textField('用户称呼（想 AI 怎么称呼你？）', 'userName', false);
    textField('用户身份与喜好（记忆：身份、偏好、习惯、项目背景，AI 可在对话中更新这一栏）', 'userMemory', true);
    textField('AI 助理名字（你想怎么称呼 AI）', 'aiName', false);
    textField('AI 助理身份（AI 的定位与人设）', 'aiIdentity', true);

    // ---- 草稿状态：色板与取色器即时改草稿值（预览同步），保存才落库 ----
    function setDraft(patch) {
      Object.keys(patch).forEach(function (k) { state[k] = patch[k]; });
      markSel();
      preview.style.background = state.bubbleColor
        ? state.bubbleColor
        : 'var(--chip)';
    }

    // ---- 保存 ----
    var actions = document.createElement('div');
    actions.style.cssText = 'display:flex;gap:10px;align-items:center;margin-top:12px;';
    var save = document.createElement('button');
    save.type = 'button';
    save.className = 'mc-btn';
    save.textContent = '保存';
    var status = document.createElement('span');
    status.className = 'pf-note';
    actions.appendChild(save);
    actions.appendChild(status);
    el.appendChild(actions);

    save.onclick = function () {
      save.disabled = true;
      status.textContent = '保存中…';
      status.style.color = '';
      // 前端把关长度：超长按截断版提交（服务端对超长整单报错，不截断存储）
      var body = {
        userName: fields.userName.el.value,
        userMemory: fields.userMemory.el.value,
        aiName: fields.aiName.el.value,
        aiIdentity: fields.aiIdentity.el.value,
        bubbleColor: state.bubbleColor,
      };
      var clipped = [];
      Object.keys(body).forEach(function (k) {
        if (typeof body[k] === 'string' && runeLen(body[k]) > LIMIT) {
          clipped.push(k);
          body[k] = clamp(body[k]);
        }
      });
      saveMemory(body).then(function (d) {
        setColor(d.bubbleColor); // 立即应用于对话区气泡
        if (clipped.length) {
          status.style.color = 'var(--danger)';
          status.textContent = '已保存（' + clipped.length + ' 个字段超出 1000 字，已按前 1000 字入库；完整草稿仍在本页输入框里）';
        } else {
          status.textContent = '已保存，下一轮对话起对全部会话生效';
        }
        // 刻意不回填：输入框保留用户完整草稿（下次打开设置页看到的才是已存版本）
      }).catch(function (e) {
        status.style.color = 'var(--danger)';
        status.textContent = '保存失败：' + (e && e.message ? e.message : e);
      }).then(function () {
        save.disabled = false;
        setTimeout(function () { if (status.isConnected) { status.textContent = ''; status.style.color = ''; } }, 6000);
      });
    };

    // 用已保存的数据回填（仅初始加载；保存后不回填——保留完整草稿）
    function fill(d) {
      state.bubbleColor = d.bubbleColor || '';
      fields.userName.el.value = d.userName || '';
      fields.userMemory.el.value = d.userMemory || '';
      fields.aiName.el.value = d.aiName || '';
      fields.aiIdentity.el.value = d.aiIdentity || '';
      Object.keys(fields).forEach(function (k) { fields[k].upd(); });
      if (/^#[0-9a-fA-F]{6}$/.test(state.bubbleColor)) custom.value = state.bubbleColor;
      setDraft({});
    }

    fill({ userName: '', userMemory: '', aiName: '', aiIdentity: '', bubbleColor: '' });
    loadMemory().then(function (d) {
      if (!el.isConnected) return; // 设置页已收起：结果作废
      fill(d);
    }).catch(function (e) {
      status.textContent = '读取失败：' + (e && e.message ? e.message : e);
    });
  }

  /* ================= 注册 ================= */

  CX.register({
    id: 'user-memory',

    init() {
      // 颜色尽早应用（对话区一打开就是所选色，而不是先灰后变色）
      loadMemory().then(function (d) { setColor(d.bubbleColor); }).catch(function () { /* 组件关闭等，忽略 */ });

      CX.registerSettings({
        id: 'user-memory',
        title: 'AI 记忆与拟人化',
        pane: {
          label: 'AI 记忆与拟人化',
          icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/><path d="M12 12v0"/></svg>',
        },
        mount: mountSettings,
      });
    },

    dispose() {
      CX.unregisterSettings('user-memory');
      // 还原默认灰：组件关掉，自定义色不再生效
      document.documentElement.style.removeProperty(CSS_VAR);
    },
  });
})();
