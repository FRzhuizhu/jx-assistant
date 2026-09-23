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
 * 全程不需要刷新页面、不需要重启服务。 */
(function () {
  if (window.CX) return;

  const CX = {
    _list: [],        // /api/components 的清单
    _active: {},      // id -> { dispose }（已 init 的组件）
    _gen: {},         // id -> 代数（防快速反复开关的旧请求串台）

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

    // 注入一个组件的资产（带代数防串台；重注入前先清旧标签）
    _inject(c) {
      if (!c) return;
      const gen = (this._gen[c.id] = (this._gen[c.id] || 0) + 1);
      this._removeTags(c.id);
      const base = '/_components/' + c.id + '/';
      if (c.hasCss) {
        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.dataset.cx = c.id;
        link.href = base + 'client.css?t=' + gen;
        document.head.appendChild(link);
      }
      if (c.hasJs) {
        const s = document.createElement('script');
        s.dataset.cx = c.id;
        s.src = base + 'client.js?t=' + gen;
        s.onerror = () => console.warn('[CX] 组件 ' + c.id + ' 脚本加载失败');
        document.body.appendChild(s);
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
      if (on) {
        this._inject(c);
        return { hot: true };
      }
      // 关闭：有 dispose 则热卸载，否则提示刷新
      const rec = this._active[id];
      if (rec && typeof rec.dispose === 'function') {
        try { rec.dispose(); } catch (e) { console.warn('[CX] 组件 ' + id + ' dispose 失败', e); }
        delete this._active[id];
        this._removeTags(id);
        return { hot: true };
      }
      this._removeTags(id);
      delete this._active[id];
      return { hot: false };
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
