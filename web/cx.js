/* CX：组件运行时 —— 发现、注入、注册、热开关。
 *
 * 组件是本栈原生的轻量外围扩展（组件/<名字>/：manifest.json + client.js），
 * 与插件（插件兼容层，DSH 适配）相互独立。本运行时随页面加载：
 *   1. boot：GET /api/components，把已启用组件的 js/css 注入页面；
 *   2. 组件脚本执行时调用 CX.register({id, init, dispose}) 注册自身，
 *      init() 立即执行；dispose() 是热关闭的关键——撤销 init 的全部副作用；
 *   3. 设置页开关 → CX.toggle(id, on)：
 *      开 = 重新注入脚本（立即生效）；
 *      关 = 调 dispose() + 移除标签。组件没提供 dispose 时返回 needsRefresh。
 * 全程不需要刷新页面、不需要重启服务。
 *
 * 组件设置区：组件在 init 里调 CX.registerSettings({id, title, mount}) 把自己的
 * 设置区交给 CX 保管；设置页（「内置组件」页）按 CX.settingsSections() 渲染，
 * 组件激活状态一变（注册/注销/热开关）CX 就派发 window 事件 cx:change，设置页
 * 收到后重挂——于是关掉组件时它那段设置连同按钮一起消失。 */

(function () {
  if (window.CX) return;

  const CX = {
    _list: [],        // /api/components 的清单
    _active: {},      // id -> { dispose }（已 init 的组件）
    _gen: {},         // id -> 代数（防快速反复开关的旧请求串台）
    _sections: [],    // {id, title, mount}：组件设置区（设置页「内置组件」渲染）

    async boot() {
      try {
        const res = await fetch('/api/components');
        const data = await res.json();
        this._list = data.components || [];
      } catch (e) {
        console.warn('[CX] 清单读取失败', e);
        return;
      }
      for (const c of this._list) {
        if (c.enabled) this._inject(c);
      }
    },

    _manifest(id) {
      return this._list.find((c) => c.id === id) || null;
    },

    // 注入一个组件的资产（带代数防串台；重注入前先清旧标签）。
    // 资产统一经 /api/components/ 用 fetch 拿文本再注入（CSS 进 <style>、JS 用
    // text 立即执行）：本地 fetch 直连本机服务；远程模式下 /api/ 前缀被
    // remote-boot 的 fetch 劫持自动封帧走中继——组件加载因此天然兼容远程，
    // 不需要任何远程特判。（旧做法插相对 src 的 <link>/<script> 标签，远程时
    // 相对网关 origin 解析必 404，组件全体消失。）异步回来先查代数，快速开关
    // 时旧请求作废，防止旧代码覆盖新一轮注入。
    _inject(c) {
      if (!c) return;
      const gen = (this._gen[c.id] = (this._gen[c.id] || 0) + 1);
      this._removeTags(c.id);
      const base = '/api/components/' + c.id + '/';
      if (c.hasCss) {
        fetch(base + 'client.css?t=' + gen)
          .then((r) => { if (!r.ok) throw new Error('HTTP ' + r.status); return r.text(); })
          .then((css) => {
            if (this._gen[c.id] !== gen) return;
            const style = document.createElement('style');
            style.dataset.cx = c.id;
            style.textContent = css;
            document.head.appendChild(style);
          })
          .catch((e) => console.warn('[CX] 组件 ' + c.id + ' 样式加载失败', e));
      }
      if (c.hasJs) {
        fetch(base + 'client.js?t=' + gen)
          .then((r) => { if (!r.ok) throw new Error('HTTP ' + r.status); return r.text(); })
          .then((code) => {
            if (this._gen[c.id] !== gen) return;
            const s = document.createElement('script');
            s.dataset.cx = c.id;
            // sourceURL 让报错堆栈按组件文件名显示（text 注入默认是匿名 inline）
            s.text = code + '\n//# sourceURL=' + base + 'client.js';
            document.body.appendChild(s);
          })
          .catch((e) => console.warn('[CX] 组件 ' + c.id + ' 脚本加载失败', e));
      }
    },

    _removeTags(id) {
      document.querySelectorAll('[data-cx="' + id + '"]').forEach((el) => el.remove());
    },

    // 组件脚本调用：注册并立即 init（重复注册按新 dispose 覆盖、不重复 init）
    register(def) {
      if (!def || !def.id) return;
      if (this._active[def.id]) {
        // 已激活（脚本因重新注入再次执行）：只刷新 dispose 引用
        this._active[def.id].dispose = def.dispose || null;
        return;
      }
      this._active[def.id] = { dispose: def.dispose || null };
      this._signal(); // 激活状态变了：通知设置页重挂（含 init 里刚注册的设置区）
      if (def.init) {
        try { def.init(); } catch (e) { console.warn('[CX] 组件 ' + def.id + ' init 失败', e); }
      }
    },

    // 设置页开关：POST 状态 + 前端即时加载/卸载
    async toggle(id, on) {
      const res = await fetch('/api/components', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id, enabled: on }),
      });
      if (!res.ok) {
        const d = await res.json().catch(() => ({}));
        throw new Error(d.error || res.statusText);
      }
      const c = this._manifest(id);
      if (c) c.enabled = !!on; // 清单跟着更新：兜底提示与组件互查看的就是它
      if (on) {
        this._inject(c);
        return { hot: true };
      }
      // 关闭：有 dispose 则热卸载，否则提示刷新
      const rec = this._active[id];
      if (rec && typeof rec.dispose === 'function') {
        try { rec.dispose(); } catch (e) { console.warn('[CX] 组件 ' + id + ' dispose 失败', e); }
        delete this._active[id];
        this._signal(); // 卸载完成：设置页重挂，该组件的设置区随之消失
        this._removeTags(id);
        return { hot: true };
      }
      this._removeTags(id);
      delete this._active[id];
      this._signal(); // 卸载完成：设置页重挂，该组件的设置区随之消失
      return { hot: false };
    },

    // 重拉清单并对账（市场安装/卸载组件后由 app-market.js 调用）：
    // 新出现且启用的组件立即注入、消失的组件热卸载，全程不刷新页面。
    async refresh() {
      let fresh;
      try {
        const res = await fetch('/api/components');
        const data = await res.json();
        fresh = data.components || [];
      } catch (e) {
        console.warn('[CX] 清单刷新失败', e);
        return;
      }
      const oldIds = new Set(this._list.map((c) => c.id));
      const newIds = new Set(fresh.map((c) => c.id));
      for (const id of oldIds) {
        if (newIds.has(id)) continue;
        const rec = this._active[id];
        if (rec && typeof rec.dispose === 'function') {
          try { rec.dispose(); } catch (e) { console.warn('[CX] 组件 ' + id + ' dispose 失败', e); }
        }
        delete this._active[id];
        this._removeTags(id);
      }
      this._list = fresh;
      for (const c of fresh) {
        if (c.enabled && !oldIds.has(c.id)) this._inject(c);
      }
      this._signal(); // 增删都会影响设置页「内置组件」清单与组件设置区
    },

    // 组件交出（或更新）自己的设置区；通常在 init 里调用。
    // 设置区随组件激活状态显示：没激活的组件那段不会渲染出来。
    registerSettings(def) {
      if (!def || !def.id || typeof def.mount !== 'function') return;
      const i = this._sections.findIndex((s) => s.id === def.id);
      if (i >= 0) this._sections[i] = def; else this._sections.push(def);
      this._signal();
    },

    unregisterSettings(id) {
      const i = this._sections.findIndex((s) => s.id === id);
      if (i >= 0) this._sections.splice(i, 1);
      this._signal();
    },

    // 当前该渲染的组件设置区（只有激活中的组件；设置页照这个清单排）
    settingsSections() {
      return this._sections.filter((s) => this.isActive(s.id));
    },

    // 激活状态/设置区有变 → 通知页面（设置页监听 cx:change 重挂组件设置区）
    _signal() {
      try { window.dispatchEvent(new CustomEvent('cx:change')); } catch (e) { /* 老引擎忽略 */ }
    },

    // 当前是否已激活（供组件互查）
    isActive(id) { return !!this._active[id]; },
  };

  window.CX = CX;
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => CX.boot());
  } else {
    CX.boot();
  }
})();
