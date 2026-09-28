/* remote-boot.js —— 远程模式引导：仅由 /api/p2p-shell 拼装的外壳注入 <head>，
   本地直开前端时该文件根本不会被引用（index.html 不含它），零影响。

   工作方式（与 210 网关 gate.html 的帧协议逐字节兼容）：
   1. gate 换页时注入 window.P2P_GATE=true 与 P2P_APP_ID，配对信息已存
      localStorage['p2p_cfg_v1_<设备号>']
   2. 本脚本劫持 window.fetch：同源 /api/* 请求 → 封 {t:'req'} 帧 AES-GCM 加密后
      POST /relay（NDJSON 流式响应）→ 还原成带流的 Response（对话流/事件流可用）；
      请求附 x-p2p-gzip 头（浏览器支持 DecompressionStream 时），服务端只对
      一次性 JSON 响应整体 gzip（先压再 base64/加密，膨胀只作用于压缩后字节），
      接收端按 head.gz 流式解压；SSE/流式响应不压（gzip 缓冲会破坏实时性）
   3. 劫持 window.EventSource：/api/events 的 SSE 长连接改为「流式 fetch + 自动重连」
      （210 对单请求有 150s 上限，断开后 3s 重连；会话执行流的接回由应用既有
      「接回执行流」机制自动完成）
   4. FormData 上传自动改走 multipart 帧协议；window.JX_REMOTE 提供附件/下载的
      blob 化助手（供 app-chat.js 的少量兜底调用）

   加密：非安全上下文（http 网关）无 WebCrypto，内置纯 JS AES-256-GCM
   （与 gate.html 同源代码；密钥 = SHA-256(配对密钥)，网关只见密文）。 */
(function () {
  'use strict';
  if (window.P2P_GATE !== true) return; // 本地直开/桌面窗口：什么都不做

  var APP_ID = window.P2P_APP_ID || '';
  var cfg = null;
  try { cfg = JSON.parse(localStorage.getItem('p2p_cfg_v1' + (APP_ID ? '_' + APP_ID : '')) || 'null'); } catch (e) {}
  if (!cfg || !cfg.peerId || !cfg.pairKey) {
    document.documentElement.innerHTML = '<body style="font-family:system-ui;padding:40px;color:#d43c3c">' +
      '<h3>缺少配对信息</h3><p>请从配对入口重新打开（地址形如 …/a/' + (APP_ID || '设备号') + '#pk=配对密钥）。</p></body>';
    return;
  }

  var realFetch = window.fetch.bind(window);
  var HAS_SUBTLE = typeof crypto !== 'undefined' && !!crypto.subtle;
  var HAS_DECOMPRESS = typeof DecompressionStream === 'function'; // 响应 gzip 解压能力（现代浏览器）

  /* ---------- base64 工具 ---------- */
  function b64ToBytes(b64) {
    var bin = atob(b64), u8 = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
    return u8;
  }
  function bytesToB64(u8) {
    var s = '';
    for (var i = 0; i < u8.length; i++) s += String.fromCharCode(u8[i]);
    return btoa(s);
  }

  /* ---------- 纯 JS AES-256-GCM（http 非安全上下文无 WebCrypto 时的降级） ---------- */
  var _PJ_K = new Uint32Array([
0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
  0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
  0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
  0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
  0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
  0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
  0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
  0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2,
  ]);
  function pjSha256(msg) {
    var len = msg.length;
    var padded = new Uint8Array((((len + 9) + 63) >> 6) << 6);
    padded.set(msg); padded[len] = 0x80;
    new DataView(padded.buffer).setUint32(padded.length - 4, len << 3);
    var h = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];
    var w = new Array(64);
    var rr = function (x, n) { return (x >>> n) | (x << (32 - n)); };
    for (var o = 0; o < padded.length; o += 64) {
      var dv = new DataView(padded.buffer, o, 64);
      for (var i = 0; i < 16; i++) w[i] = dv.getUint32(i * 4);
      for (var i2 = 16; i2 < 64; i2++) {
        var s0 = rr(w[i2-15],7) ^ rr(w[i2-15],18) ^ (w[i2-15] >>> 3);
        var s1 = rr(w[i2-2],17) ^ rr(w[i2-2],19) ^ (w[i2-2] >>> 10);
        w[i2] = (w[i2-16] + s0 + w[i2-7] + s1) | 0;
      }
      var a=h[0],b=h[1],c=h[2],d=h[3],e=h[4],f=h[5],g=h[6],hh=h[7];
      for (var i3 = 0; i3 < 64; i3++) {
        var S1 = rr(e,6) ^ rr(e,11) ^ rr(e,25);
        var ch = (e & f) ^ (~e & g);
        var t1 = (hh + S1 + ch + _PJ_K[i3] + w[i3]) | 0;
        var S0 = rr(a,2) ^ rr(a,13) ^ rr(a,22);
        var mj = (a & b) ^ (a & c) ^ (b & c);
        var t2 = (S0 + mj) | 0;
        hh=g; g=f; f=e; e=(d+t1)|0; d=c; c=b; b=a; a=(t1+t2)|0;
      }
      h[0]=(h[0]+a)|0; h[1]=(h[1]+b)|0; h[2]=(h[2]+c)|0; h[3]=(h[3]+d)|0;
      h[4]=(h[4]+e)|0; h[5]=(h[5]+f)|0; h[6]=(h[6]+g)|0; h[7]=(h[7]+hh)|0;
    }
    var out = new Uint8Array(32);
    var dv2 = new DataView(out.buffer);
    for (var i4 = 0; i4 < 8; i4++) dv2.setUint32(i4 * 4, h[i4] >>> 0);
    return out;
  }
  var _PJ_SBOX = new Uint8Array([
    0x63,0x7c,0x77,0x7b,0xf2,0x6b,0x6f,0xc5,0x30,0x01,0x67,0x2b,0xfe,0xd7,0xab,0x76,
    0xca,0x82,0xc9,0x7d,0xfa,0x59,0x47,0xf0,0xad,0xd4,0xa2,0xaf,0x9c,0xa4,0x72,0xc0,
    0xb7,0xfd,0x93,0x26,0x36,0x3f,0xf7,0xcc,0x34,0xa5,0xe5,0xf1,0x71,0xd8,0x31,0x15,
    0x04,0xc7,0x23,0xc3,0x18,0x96,0x05,0x9a,0x07,0x12,0x80,0xe2,0xeb,0x27,0xb2,0x75,
    0x09,0x83,0x2c,0x1a,0x1b,0x6e,0x5a,0xa0,0x52,0x3b,0xd6,0xb3,0x29,0xe3,0x2f,0x84,
    0x53,0xd1,0x00,0xed,0x20,0xfc,0xb1,0x5b,0x6a,0xcb,0xbe,0x39,0x4a,0x4c,0x58,0xcf,
    0xd0,0xef,0xaa,0xfb,0x43,0x4d,0x33,0x85,0x45,0xf9,0x02,0x7f,0x50,0x3c,0x9f,0xa8,
    0x51,0xa3,0x40,0x8f,0x92,0x9d,0x38,0xf5,0xbc,0xb6,0xda,0x21,0x10,0xff,0xf3,0xd2,
    0xcd,0x0c,0x13,0xec,0x5f,0x97,0x44,0x17,0xc4,0xa7,0x7e,0x3d,0x64,0x5d,0x19,0x73,
    0x60,0x81,0x4f,0xdc,0x22,0x2a,0x90,0x88,0x46,0xee,0xb8,0x14,0xde,0x5e,0x0b,0xdb,
    0xe0,0x32,0x3a,0x0a,0x49,0x06,0x24,0x5c,0xc2,0xd3,0xac,0x62,0x91,0x95,0xe4,0x79,
    0xe7,0xc8,0x37,0x6d,0x8d,0xd5,0x4e,0xa9,0x6c,0x56,0xf4,0xea,0x65,0x7a,0xae,0x08,
    0xba,0x78,0x25,0x2e,0x1c,0xa6,0xb4,0xc6,0xe8,0xdd,0x74,0x1f,0x4b,0xbd,0x8b,0x8a,
    0x70,0x3e,0xb5,0x66,0x48,0x03,0xf6,0x0e,0x61,0x35,0x57,0xb9,0x86,0xc1,0x1d,0x9e,
    0xe1,0xf8,0x98,0x11,0x69,0xd9,0x8e,0x94,0x9b,0x1e,0x87,0xe9,0xce,0x55,0x28,0xdf,
    0x8c,0xa1,0x89,0x0d,0xbf,0xe6,0x42,0x68,0x41,0x99,0x2d,0x0f,0xb0,0x54,0xbb,0x16,
  ]);
  function pjAesExpand(key) {
    var w = new Uint32Array(60);
    for (var i = 0; i < 8; i++) w[i] = ((key[4*i]<<24) | (key[4*i+1]<<16) | (key[4*i+2]<<8) | key[4*i+3]) >>> 0;
    var rcon = 1;
    var sub = function (t) { return (((_PJ_SBOX[t >>> 24] << 24) | (_PJ_SBOX[(t >>> 16) & 255] << 16) | (_PJ_SBOX[(t >>> 8) & 255] << 8) | _PJ_SBOX[t & 255]) >>> 0); };
    for (var i2 = 8; i2 < 60; i2++) {
      var t = w[i2 - 1];
      if (i2 % 8 === 0) {
        t = (((t << 8) | (t >>> 24)) >>> 0);
        t = sub(t);
        t = (t ^ (rcon << 24)) >>> 0;
        rcon = ((rcon << 1) ^ ((rcon & 0x80) ? 0x11b : 0)) & 0xff;
      } else if (i2 % 8 === 4) {
        t = sub(t);
      }
      w[i2] = (w[i2 - 8] ^ t) >>> 0;
    }
    return w;
  }
  function pjAesEncBlock(w, in16, out16) {
    var s = new Uint8Array(16), o = new Uint8Array(16);
    for (var i = 0; i < 16; i++) s[i] = in16[i] ^ ((w[i >> 2] >>> (24 - 8 * (i & 3))) & 255);
    var xt = function (x) { return ((x << 1) ^ ((x & 0x80) ? 0x1b : 0)) & 0xff; };
    for (var r = 1; r <= 14; r++) {
      for (var c = 0; c < 4; c++) for (var j = 0; j < 4; j++)
        o[4 * c + j] = _PJ_SBOX[s[4 * ((c + j) & 3) + j]];
      if (r < 14) for (var c2 = 0; c2 < 4; c2++) {
        var a0 = o[4*c2], a1 = o[4*c2+1], a2 = o[4*c2+2], a3 = o[4*c2+3];
        o[4*c2]   = xt(a0) ^ (xt(a1) ^ a1) ^ a2 ^ a3;
        o[4*c2+1] = a0 ^ xt(a1) ^ (xt(a2) ^ a2) ^ a3;
        o[4*c2+2] = a0 ^ a1 ^ xt(a2) ^ (xt(a3) ^ a3);
        o[4*c2+3] = (xt(a0) ^ a0) ^ a1 ^ a2 ^ xt(a3);
      }
      for (var i2 = 0; i2 < 16; i2++) s[i2] = o[i2] ^ ((w[4*r + (i2 >> 2)] >>> (24 - 8 * (i2 & 3))) & 255);
    }
    for (var i3 = 0; i3 < 16; i3++) out16[i3] = s[i3];
  }
  function pjShr1(z) {
    var lsb = z[15] & 1, carry = 0;
    for (var i = 0; i < 16; i++) { var c = z[i] & 1; z[i] = (z[i] >> 1) | carry; carry = c ? 0x80 : 0; }
    if (lsb) z[0] ^= 0xe1;
  }
  function pjGhashBlocks(H, blocks) {
    var Y = new Uint8Array(16), X = new Uint8Array(16), V = new Uint8Array(16);
    for (var bi = 0; bi < blocks.length; bi++) {
      var blk = blocks[bi];
      for (var i = 0; i < 16; i++) X[i] = Y[i] ^ blk[i];
      Y.fill(0);
      V.set(H);
      for (var b = 0; b < 128; b++) {
        if ((X[b >> 3] >> (7 - (b & 7))) & 1) { for (var k = 0; k < 16; k++) Y[k] ^= V[k]; }
        pjShr1(V);
      }
    }
    return Y;
  }
  function pjGcm(key, iv, data, decrypt) {
    var w = pjAesExpand(key);
    var J0 = new Uint8Array(16); J0.set(iv); J0[15] = 1;
    var zeros = new Uint8Array(16), ej0 = new Uint8Array(16), ks = new Uint8Array(16);
    pjAesEncBlock(w, zeros, ej0);
    var H = ej0.slice();
    pjAesEncBlock(w, J0, ej0);
    var body = data.subarray(0, data.length - (decrypt ? 16 : 0));
    var tag = data.subarray(data.length - 16);
    var out = new Uint8Array(body.length);
    var ctr = J0.slice();
    for (var off = 0; off < body.length; off += 16) {
      for (var i = 15; i >= 12; i--) { ctr[i] = (ctr[i] + 1) & 255; if (ctr[i]) break; }
      pjAesEncBlock(w, ctr, ks);
      var n = Math.min(16, body.length - off);
      for (var j = 0; j < n; j++) out[off + j] = body[off + j] ^ ks[j];
    }
    var blocks = [];
    for (var off2 = 0; off2 < out.length; off2 += 16) {
      var b = new Uint8Array(16);
      b.set(decrypt ? body.subarray(off2, off2 + 16) : out.subarray(off2, off2 + 16));
      blocks.push(b);
    }
    var lenb = new Uint8Array(16);
    new DataView(lenb.buffer).setUint32(12, body.length << 3);
    blocks.push(lenb);
    var S = pjGhashBlocks(H, blocks);
    var T = new Uint8Array(16);
    for (var k2 = 0; k2 < 16; k2++) T[k2] = S[k2] ^ ej0[k2];
    if (decrypt) {
      for (var k3 = 0; k3 < 16; k3++) if (T[k3] !== tag[k3]) throw new Error('GCM auth fail');
      return out;
    }
    var res = new Uint8Array(out.length + 16);
    res.set(out); res.set(T, out.length);
    return res;
  }

  /* ---------- 中继封包（与服务端 sealJSON/openPack 格式一致） ---------- */
  var _relayKeyObj = null;
  function relayKeyBytes() { return pjSha256(new TextEncoder().encode(cfg.pairKey)); }
  function seal(obj) {
    var data = new TextEncoder().encode(JSON.stringify(obj));
    var iv = crypto.getRandomValues(new Uint8Array(12));
    if (HAS_SUBTLE) {
      return crypto.subtle.encrypt({ name: 'AES-GCM', iv: iv }, _relayKeyObj, data).then(function (ct) {
        return { iv: bytesToB64(iv), ct: bytesToB64(new Uint8Array(ct)) };
      });
    }
    return Promise.resolve({ iv: bytesToB64(iv), ct: bytesToB64(pjGcm(relayKeyBytes(), iv, data, false)) });
  }
  function openPack(ivB64, ctB64) {
    var iv = b64ToBytes(ivB64), ct = b64ToBytes(ctB64);
    if (HAS_SUBTLE) {
      return crypto.subtle.decrypt({ name: 'AES-GCM', iv: iv }, _relayKeyObj, ct).then(function (pt) {
        return JSON.parse(new TextDecoder().decode(pt));
      });
    }
    return Promise.resolve(JSON.parse(new TextDecoder().decode(pjGcm(relayKeyBytes(), iv, ct, true))));
  }
  function prepareKey() {
    if (!HAS_SUBTLE) return Promise.resolve();
    if (_relayKeyObj) return Promise.resolve();
    var d = new TextEncoder().encode(cfg.pairKey);
    return crypto.subtle.digest('SHA-256', d).then(function (dg) {
      return crypto.subtle.importKey('raw', dg, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
    }).then(function (k) { _relayKeyObj = k; });
  }

  /* ---------- 请求体序列化（含 FormData → 手工 multipart） ---------- */
  function normalizeBody(init) {
    var b = init && init.body;
    if (b == null) return null;
    if (typeof b === 'string') {
      return { u8: new TextEncoder().encode(b) };
    }
    if (b instanceof FormData) {
      var boundary = '----p2pjx' + Date.now().toString(36) + Math.random().toString(36).slice(2);
      var enc = new TextEncoder();
      var partPromises = [];
      var entries = b.entries();
      for (var it = entries.next(); !it.done; it = entries.next()) {
        (function (kv) {
          var name = kv[0], v = kv[1];
          if (v instanceof Blob) { // File 继承 Blob：文件名与类型从对象上取
            partPromises.push(v.arrayBuffer().then(function (ab) {
              var fname = (v instanceof File && v.name) ? String(v.name).replace(/"/g, '') : 'blob';
              var head = enc.encode('--' + boundary + '\r\nContent-Disposition: form-data; name="' +
                name + '"; filename="' + fname + '"\r\nContent-Type: ' +
                (v.type || 'application/octet-stream') + '\r\n\r\n');
              var tail = enc.encode('\r\n');
              var content = new Uint8Array(ab);
              var out = new Uint8Array(head.length + content.length + tail.length);
              out.set(head, 0); out.set(content, head.length); out.set(tail, head.length + content.length);
              return out;
            }));
          } else {
            partPromises.push(Promise.resolve(enc.encode('--' + boundary +
              '\r\nContent-Disposition: form-data; name="' + name + '"\r\n\r\n' + v + '\r\n')));
          }
        })(it.value);
      }
      partPromises.push(Promise.resolve(enc.encode('--' + boundary + '--\r\n')));
      return Promise.all(partPromises).then(function (parts) {
        var len = 0;
        parts.forEach(function (x) { len += x.length; });
        var outU8 = new Uint8Array(len), off = 0;
        parts.forEach(function (x) { outU8.set(x, off); off += x.length; });
        return { u8: outU8, contentType: 'multipart/form-data; boundary=' + boundary };
      });
    }
    if (b instanceof Blob) {
      return b.arrayBuffer().then(function (ab) { return { u8: new Uint8Array(ab) }; });
    }
    if (b instanceof ArrayBuffer) return Promise.resolve({ u8: new Uint8Array(b) });
    if (ArrayBuffer.isView(b)) return Promise.resolve({ u8: new Uint8Array(b.buffer, b.byteOffset, b.byteLength) });
    return null;
  }

  var seq = 0;

  /* ---------- 隧道 fetch：/api/* → 加密中继，返回带流的 Response ---------- */
  function tunnelFetch(input, init) {
    init = init || {};
    var urlStr = typeof input === 'string' ? input : (input && input.url) || String(input);
    var u;
    try { u = new URL(urlStr, location.href); } catch (e) { return realFetch(input, init); }
    if (u.origin !== location.origin || u.pathname.indexOf('/api/') !== 0) {
      return realFetch(input, init); // 非同源或非 API：原样放行
    }
    return prepareKey().then(function () {
      return normalizeBody(init);
    }).then(function (body) {
      var headers = {};
      var hIn = init.headers || (typeof input === 'object' && input && input.headers) || {};
      var getH = function (n) {
        if (typeof hIn.get === 'function') return hIn.get(n); // Headers 实例
        var ln = n.toLowerCase();
        for (var k in hIn) { if (k.toLowerCase() === ln) return hIn[k]; }
        return null;
      };
      var ct = getH('content-type') || (body && body.contentType);
      if (typeof ct === 'string' && ct) headers['content-type'] = ct;
      if (HAS_DECOMPRESS) headers['x-p2p-gzip'] = '1'; // 声明可解压；服务端按响应类型决定是否压
      var method = (init.method || 'GET').toUpperCase();
      var id = 'r' + (++seq);
      var frame = { t: 'req', id: id, method: method, path: u.pathname + u.search, headers: headers };
      if (body && body.u8) frame.body = bytesToB64(body.u8);
      return seal({ pk: cfg.pairKey, ts: Math.floor(Date.now() / 1000), frame: frame }).then(function (pack) {
        return realFetch('/relay', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-Relay-Peer': cfg.peerId },
          body: JSON.stringify(pack),
        });
      }).then(function (resp) {
        if (!resp.ok || !resp.body) {
          return resp.text().catch(function () { return ''; }).then(function (t) {
            var msg = t;
            try { msg = JSON.parse(t).error || t; } catch (e) {}
            throw new Error('中继错误 HTTP ' + resp.status + (msg ? '：' + msg : ''));
          });
        }
        // NDJSON 逐帧解密 → 流式 Response
        var reader = resp.body.getReader();
        var dec = new TextDecoder();
        var buf = '';
        var controllerRef = null;
        var aborted = false;
        if (init.signal) {
          var onAbort = function () {
            aborted = true;
            try { reader.cancel(); } catch (e) {}
            if (controllerRef) { try { controllerRef.error(new DOMException('Aborted', 'AbortError')); } catch (e) {} }
          };
          if (init.signal.aborted) { onAbort(); return Promise.reject(new DOMException('Aborted', 'AbortError')); }
          init.signal.addEventListener('abort', onAbort);
        }
        return new Promise(function (resolve, reject) {
          var settled = false; // 已拿到 head（外层 Response 已 resolve）
          var fail = function (e) {
            if (settled) { if (controllerRef) { try { controllerRef.error(e); } catch (e2) {} } }
            else { settled = true; reject(e); }
          };
          var handleFrame = function (fr) {
            if (fr.t === 'head' && !settled) {
              settled = true;
              // head.gz：响应体是 gzip 流（服务端已压缩）——chunk 帧仍进 raw 控制器，
              // 外层套 DecompressionStream 流式解压给消费方；解压错误沿管道传播
              var raw = new ReadableStream({
                start: function (c) { controllerRef = c; },
              });
              var stream = (fr.gz && HAS_DECOMPRESS)
                ? raw.pipeThrough(new DecompressionStream('gzip'))
                : raw;
              var hdrs = new Headers();
              if (fr.contentType) hdrs.set('Content-Type', fr.contentType);
              resolve(new Response(stream, { status: fr.status || 200, headers: hdrs }));
            } else if (fr.t === 'chunk') {
              if (controllerRef) controllerRef.enqueue(b64ToBytes(fr.data));
            } else if (fr.t === 'end') {
              if (controllerRef) { try { controllerRef.close(); } catch (e) {} }
              controllerRef = null;
            } else if (fr.t === 'err') {
              fail(new Error(fr.msg || '远程错误'));
              controllerRef = null;
            } else if (fr.t === 'authfail') {
              fail(new Error('配对密钥错误'));
              controllerRef = null;
            }
          };
          // 单条 async 循环：读一块 → 按序逐行解密（帧解密是异步的，串行等待
          // 才不会与流结束信号赛跑）→ 流尽后处理无换行尾行 → 终局判定。
          // 210 的 NDJSON 每行都以换行符结尾，尾行兜底只是防御。
          (async function () {
            for (;;) {
              var r;
              try { r = await reader.read(); }
              catch (e) { fail(aborted ? new DOMException('Aborted', 'AbortError') : e); return; }
              if (r.done) break;
              buf += dec.decode(r.value, { stream: true });
              var nl;
              while ((nl = buf.indexOf('\n')) >= 0) {
                var line = buf.slice(0, nl).trim();
                buf = buf.slice(nl + 1);
                if (!line) continue;
                var f = null;
                try { f = JSON.parse(line); } catch (e) { continue; }
                if (!f.iv || !f.ct) continue;
                var fr;
                try { fr = await Promise.resolve(openPack(f.iv, f.ct)); }
                catch (e) { fail(e); return; }
                handleFrame(fr);
                if (fr.t === 'end' || fr.t === 'err' || fr.t === 'authfail') return;
              }
            }
            if (buf.trim()) { // 无结尾换行的最后一帧
              var tf = null;
              try { tf = JSON.parse(buf.trim()); } catch (e) { tf = null; }
              if (tf && tf.iv && tf.ct) {
                try { handleFrame(await Promise.resolve(openPack(tf.iv, tf.ct))); }
                catch (e) { fail(e); return; }
              }
            }
            if (!settled) { settled = true; reject(new Error('中继无响应：配对密钥错误或目标异常')); }
            else if (controllerRef) { try { controllerRef.close(); } catch (e) {} }
          })();
        });
      });
    });
  }

  window.fetch = function (input, init) {
    return tunnelFetch(input, init);
  };

  /* ---------- EventSource 垫片：SSE 长连接改为「流式隧道 fetch + 自动重连」 ----------
     210 对单请求有 150s 上限：流被掐即重连（间隔 3s），事件是广播无回放，
     会话内容流的接回由应用既有机制（/api/sessions/{id}/stream）兜底。 */
  function RemoteEventSource(url) {
    this.url = url;
    this.onmessage = null;
    this.onerror = null;
    this.onopen = null;
    this.readyState = 0;
    this._stop = false;
    var self = this;
    prepareKey().then(function () { self._loop(); });
  }
  RemoteEventSource.prototype._loop = function () {
    var self = this;
    if (this._stop) return;
    window.fetch(this.url).then(function (resp) {
      if (!resp.ok || !resp.body) throw new Error('HTTP ' + resp.status);
      self.readyState = 1;
      if (self.onopen) self.onopen({});
      var reader = resp.body.getReader();
      var dec = new TextDecoder();
      var buf = '';
      var pump = function () {
        reader.read().then(function (r) {
          if (r.done) throw new Error('stream end');
          buf += dec.decode(r.value, { stream: true });
          var idx;
          while ((idx = buf.indexOf('\n\n')) >= 0) {
            var frame = buf.slice(0, idx).trim();
            buf = buf.slice(idx + 2);
            if (frame.indexOf('data:') !== 0) continue;
            if (self.onmessage) self.onmessage({ data: frame.slice(5).replace(/^ /, '') });
          }
          pump();
        }).catch(function (e) {
          self.readyState = 0;
          if (self.onerror) self.onerror(e);
          if (!self._stop) setTimeout(function () { self._loop(); }, 3000);
        });
      };
      pump();
    }).catch(function (e) {
      self.readyState = 0;
      if (self.onerror) self.onerror(e);
      if (!self._stop) setTimeout(function () { self._loop(); }, 3000);
    });
  };
  RemoteEventSource.prototype.close = function () { this._stop = true; this.readyState = 2; };
  window.EventSource = RemoteEventSource;

  /* ---------- 资产助手：附件/下载/导出经隧道 blob 化（供 app-chat.js 兜底） ---------- */
  var blobCache = {};
  window.JX_REMOTE = {
    on: true,
    peerId: cfg.peerId,
    assetURL: function (path) {
      if (blobCache[path]) return Promise.resolve(blobCache[path]);
      return window.fetch(path).then(function (r) {
        if (!r.ok) return path; // 退化：原路径（虽会 404，但不至于卡死界面）
        return r.blob().then(function (b) {
          blobCache[path] = URL.createObjectURL(b);
          return blobCache[path];
        });
      });
    },
    downloadURL: function (path, filename) {
      return window.fetch(path).then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.blob();
      }).then(function (b) {
        var a = document.createElement('a');
        a.href = URL.createObjectURL(b);
        a.download = filename || path.split('/').pop() || 'download';
        document.body.appendChild(a);
        a.click();
        setTimeout(function () { a.remove(); }, 1000);
      });
    },
    openURL: function (path) {
      return window.JX_REMOTE.assetURL(path).then(function (u) { window.open(u, '_blank'); });
    },
  };

  /* ---------- 外壳缓存自愈 ----------
     210 配对页（gate.html）为省首开流量把整个外壳（内联全部 CSS/JS）缓存在
     localStorage['p2p_shell_cache_v3_<设备号>']，有效期 12 小时——TTL 内不问家里
     直接用缓存。家端前端更新后，手机最长 12 小时跑旧样式（远程「样式和电脑上
     不一样」的根源）。这里开机用 /api/shell-hash（几十字节，走中继）对一下本
     外壳的内容哈希：不一致 = 家端外壳已变，清掉配对页的缓存并整页刷新——
     配对页发现缓存没了会重新连家拉新外壳。稳态（哈希一致）零额外流量；
     sessionStorage 旗标保证一次会话最多自动刷新一回，不会循环。 */
  if (window.JX_SHELL_HASH) {
    var shellRefreshed = false;
    try { shellRefreshed = !!sessionStorage.getItem('jx_shell_refreshed'); } catch (e) {}
    if (!shellRefreshed) {
      window.fetch('/api/shell-hash')
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) {
          if (d && d.hash && d.hash !== window.JX_SHELL_HASH) {
            try { localStorage.removeItem('p2p_shell_cache_v3' + (APP_ID ? '_' + APP_ID : '')); } catch (e) {}
            try { sessionStorage.setItem('jx_shell_refreshed', '1'); } catch (e) {}
            location.reload();
          }
        })
        .catch(function () { /* 比对失败（通道未就绪/离线）：留给下一次打开 */ });
    }
  }
})();
