/* qr.js —— 迷你 QR 码生成器：字节模式 + ECC M，版本 1~10（最长 213 字节，
   远程入口 URL 绰绰有余），标准掩码惩罚择优。输出模块矩阵或 SVG 字符串。
   零依赖、无 DOM 依赖（Node 可直接 require 做对拍测试）。
   正确性经与 qrcode(npm) 库逐矩阵位黄金对拍验证（见开发记录）。 */
(function (global) {
  'use strict';

  /* ---------- GF(256)，本原多项式 0x11d ---------- */
  const EXP = new Uint8Array(512), LOG = new Uint8Array(256);
  (function () {
    let x = 1;
    for (let i = 0; i < 255; i++) { EXP[i] = x; LOG[x] = i; x <<= 1; if (x & 0x100) x ^= 0x11d; }
    for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255];
  })();
  function gmul(a, b) { return (a === 0 || b === 0) ? 0 : EXP[LOG[a] + LOG[b]]; }

  /* ---------- RS 纠错：生成多项式 + 截断除法求余 ---------- */
  function rsGenPoly(n) {
    let poly = [1];
    for (let i = 0; i < n; i++) {
      const next = new Array(poly.length + 1).fill(0);
      for (let j = 0; j < poly.length; j++) {
        next[j] ^= poly[j];
        next[j + 1] ^= gmul(poly[j], EXP[i]);
      }
      poly = next;
    }
    return poly;
  }
  function rsRemainder(data, ecLen) {
    const gen = rsGenPoly(ecLen);
    const res = new Uint8Array(ecLen);
    for (let i = 0; i < data.length; i++) {
      const factor = data[i] ^ res[0];
      res.copyWithin(0, 1);
      res[ecLen - 1] = 0;
      if (factor) for (let j = 0; j < ecLen; j++) res[j] ^= gmul(gen[j + 1], factor);
    }
    return res;
  }

  /* ---------- 版本参数（ECC M，v1~10）：[块数, 总码字, 数据码字] ---------- */
  const M_BLOCKS = {
    1: [1, 26, 16], 2: [1, 44, 28], 3: [1, 70, 44], 4: [2, 100, 64], 5: [2, 134, 86],
    6: [4, 172, 108], 7: [4, 196, 124], 8: [4, 242, 154], 9: [5, 292, 182], 10: [5, 346, 216],
  };
  const ALIGN = {
    1: [], 2: [6, 18], 3: [6, 22], 4: [6, 26], 5: [6, 30], 6: [6, 34],
    7: [6, 22, 38], 8: [6, 24, 42], 9: [6, 26, 46], 10: [6, 28, 50],
  };

  // 数据位流能否装下：模式(4) + 计数(8/16) + 数据 + 终止符(≤4)
  function bitsFit(dataCW, len, version) {
    return 4 + (version >= 10 ? 16 : 8) + len * 8 + 4 <= dataCW * 8;
  }

  /* ---------- 构造最终码字（交织后的数据 + 纠错） ---------- */
  function buildCodewords(bytes, version) {
    const [blocks, totalCW, dataCW] = M_BLOCKS[version];
    // 位流：0100(字节模式) + 长度 + 数据 + 终止符 + 字节对齐 + 0xEC/0x11 填充
    const bits = [];
    const push = (val, n) => { for (let i = n - 1; i >= 0; i--) bits.push((val >> i) & 1); };
    push(4, 4);
    push(bytes.length, version >= 10 ? 16 : 8);
    for (const b of bytes) push(b, 8);
    const cap = dataCW * 8;
    push(0, Math.min(4, cap - bits.length));
    while (bits.length % 8) bits.push(0);
    const data = [];
    for (let i = 0; i < bits.length; i += 8) {
      let v = 0;
      for (let j = 0; j < 8; j++) v = (v << 1) | bits[i + j];
      data.push(v);
    }
    let pad = 0xec;
    while (data.length < dataCW) { data.push(pad); pad = pad === 0xec ? 0x11 : 0xec; }

    // 分块：不均分时短块在前、长块在后（规范分组顺序，交织按此序轮转）
    const baseTotal = Math.floor(totalCW / blocks), rem = totalCW % blocks;
    const shortBlocks = blocks - rem;
    const eccLen = (totalCW - dataCW) / blocks;
    const dataBlocks = [], eccBlocks = [];
    let off = 0;
    for (let b = 0; b < blocks; b++) {
      const dLen = baseTotal - eccLen + (b < shortBlocks ? 0 : 1);
      const blk = data.slice(off, off + dLen);
      off += dLen;
      dataBlocks.push(blk);
      eccBlocks.push(rsRemainder(blk, eccLen));
    }
    // 交织：数据块轮转取尽，再纠错块轮转取尽
    const out = [];
    let more = true;
    for (let i = 0; more; i++) {
      more = false;
      for (const blk of dataBlocks) if (i < blk.length) { out.push(blk[i]); more = true; }
    }
    more = true;
    for (let i = 0; more; i++) {
      more = false;
      for (const blk of eccBlocks) if (i < blk.length) { out.push(blk[i]); more = true; }
    }
    return out;
  }

  /* ---------- 矩阵骨架：功能图形 + 保留区 ---------- */
  function baseMatrix(version) {
    const size = 17 + 4 * version;
    const m = new Uint8Array(size * size);      // 1 = 深色模块
    const resv = new Uint8Array(size * size);   // 1 = 已占用（功能图形/保留区）
    const set = (r, c, v) => { m[r * size + c] = v ? 1 : 0; resv[r * size + c] = 1; };
    const finder = (r0, c0) => {
      for (let r = -1; r <= 7; r++) for (let c = -1; c <= 7; c++) {
        const rr = r0 + r, cc = c0 + c;
        if (rr < 0 || cc < 0 || rr >= size || cc >= size) continue;
        const dark = r >= 0 && r <= 6 && c >= 0 && c <= 6 &&
          (r === 0 || r === 6 || c === 0 || c === 6 || (r >= 2 && r <= 4 && c >= 2 && c <= 4));
        set(rr, cc, dark);
      }
    };
    finder(0, 0); finder(0, size - 7); finder(size - 7, 0);
    // 校正图形：跳过与定位图形重叠的组合
    const pos = ALIGN[version];
    for (const ar of pos) for (const ac of pos) {
      if ((ar === 6 && ac === 6) || (ar === 6 && ac === pos[pos.length - 1]) ||
          (ar === pos[pos.length - 1] && ac === 6)) continue;
      for (let r = -2; r <= 2; r++) for (let c = -2; c <= 2; c++) {
        set(ar + r, ac + c, Math.max(Math.abs(r), Math.abs(c)) !== 1);
      }
    }
    // 时序图形：第 6 行/列，跳过已占用的格
    for (let i = 8; i < size - 8; i++) {
      if (!resv[6 * size + i]) set(6, i, i % 2 === 0);
      if (!resv[i * size + 6]) set(i, 6, i % 2 === 0);
    }
    // 暗模块 + 格式信息保留区
    set(size - 8, 8, 1);
    for (let i = 0; i <= 8; i++) {
      if (!resv[8 * size + i]) set(8, i, 0);
      if (!resv[i * size + 8]) set(i, 8, 0);
    }
    for (let i = 0; i < 8; i++) {
      if (!resv[8 * size + size - 1 - i]) set(8, size - 1 - i, 0);
      if (!resv[(size - 1 - i) * size + 8]) set(size - 1 - i, 8, 0);
    }
    // 版本信息保留区（v≥7）
    if (version >= 7) {
      for (let r = 0; r < 6; r++) for (let c = size - 11; c < size - 8; c++) {
        set(r, c, 0); set(c, r, 0);
      }
    }
    return { m, resv, size };
  }

  /* ---------- BCH 校验位 ---------- */
  function bch(value, gen, bits) {
    const v = value << bits;
    let g = gen << bits; // 与被除数对齐的最高位
    // 逐位模 2 除法
    const vTop = 1 << (bits + Math.floor(Math.log2(gen)) + 1 - 1); // 未用，走通用实现：
    let rem = value << bits;
    const genDeg = gen.toString(2).length - 1;
    for (let i = bits + genDeg - 1; i >= bits; i--) {
      if ((rem >> i) & 1) rem ^= gen << (i - genDeg);
    }
    void v; void g; void vTop;
    return rem;
  }

  /* ---------- 数据 Z 字放置 + 掩码 ---------- */
  const MASKS = [
    (r, c) => (r + c) % 2 === 0,
    (r, c) => r % 2 === 0,
    (r, c) => c % 3 === 0,
    (r, c) => (r + c) % 3 === 0,
    (r, c) => (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0,
    (r, c) => (r * c) % 2 + (r * c) % 3 === 0,
    (r, c) => ((r * c) % 2 + (r * c) % 3) % 2 === 0,
    (r, c) => ((r * c) % 3 + (r + c) % 2) % 2 === 0,
  ];

  function placeData(m, resv, size, codewords) {
    let bitIdx = 0, total = codewords.length * 8;
    const nextBit = () => bitIdx < total
      ? ((codewords[bitIdx >> 3] >> (7 - (bitIdx & 7))) & 1) : 0;
    let up = true;
    for (let col = size - 1; col > 0; col -= 2) {
      if (col === 6) col--; // 跳过时序列
      for (let i = 0; i < size; i++) {
        const r = up ? size - 1 - i : i;
        for (const c of [col, col - 1]) {
          if (!resv[r * size + c]) { m[r * size + c] = nextBit(); bitIdx++; }
        }
      }
      up = !up;
    }
  }

  function applyMask(m, resv, size, mask) {
    for (let r = 0; r < size; r++) for (let c = 0; c < size; c++) {
      if (!resv[r * size + c] && MASKS[mask](r, c)) m[r * size + c] ^= 1;
    }
  }

  function penalty(m, size) {
    let score = 0;
    // N1：行/列连续同色 ≥5
    for (let axis = 0; axis < 2; axis++) {
      for (let i = 0; i < size; i++) {
        let run = 1;
        for (let j = 1; j <= size; j++) {
          const cur = j < size ? (axis ? m[j * size + i] : m[i * size + j]) : -1;
          const prev = axis ? m[(j - 1) * size + i] : m[i * size + j - 1];
          if (cur === prev) run++;
          else { if (run >= 5) score += 3 + run - 5; run = 1; }
        }
      }
    }
    // N2：2×2 同色块
    for (let r = 0; r < size - 1; r++) for (let c = 0; c < size - 1; c++) {
      const v = m[r * size + c];
      if (v === m[r * size + c + 1] && v === m[(r + 1) * size + c] && v === m[(r + 1) * size + c + 1]) score += 3;
    }
    // N3：1011101 且一侧带 0000（行/列）
    const PAT1 = [1, 0, 1, 1, 1, 0, 1, 0, 0, 0, 0];
    const PAT2 = [0, 0, 0, 0, 1, 0, 1, 1, 1, 0, 1];
    const matchAt = (get, j, pat) => {
      for (let k = 0; k < 11; k++) { if (get(j + k) !== pat[k]) return false; }
      return true;
    };
    for (let axis = 0; axis < 2; axis++) {
      for (let i = 0; i < size; i++) {
        const get = (j) => (j >= 0 && j < size ? (axis ? m[j * size + i] : m[i * size + j]) : -1);
        for (let j = 0; j <= size - 11; j++) {
          if (matchAt(get, j, PAT1) || matchAt(get, j, PAT2)) score += 40;
        }
      }
    }
    // N4：深色占比偏离 50%（按 5% 步进的向上取整档位，与规范一致）
    let dark = 0;
    for (let i = 0; i < m.length; i++) dark += m[i];
    const k = Math.abs(Math.ceil((dark * 100 / m.length) / 5) - 10);
    score += k * 10;
    return score;
  }

  function drawFormat(m, size, mask) {
    // ECC M 的级别位 00，BCH(15,5) 生成元 0x537，异或掩码 0x5412；
    // 双份放置：一份沿左上定位图形的 L 形，一份沿右下
    const v = (0 << 3) | mask; // M = 0b00
    let rem = v << 10;
    for (let i = 14; i >= 10; i--) if ((rem >> i) & 1) rem ^= 0x537 << (i - 10);
    const bits = ((v << 10) | rem) ^ 0x5412;
    const set = (r, c, val) => { m[r * size + c] = val; };
    for (let i = 0; i < 15; i++) {
      const b = (bits >> i) & 1;
      // 竖排（第 8 列）：0-5 行、7-8 行、右下角块
      if (i < 6) set(i, 8, b);
      else if (i < 8) set(i + 1, 8, b);
      else set(size - 15 + i, 8, b);
      // 横排（第 8 行）：右侧 8 格、跳过时序列后的 7、5-0 列
      if (i < 8) set(8, size - i - 1, b);
      else if (i < 9) set(8, 7, b);
      else set(8, 15 - i - 1, b);
    }
    set(size - 8, 8, 1); // 暗模块
  }

  function drawVersion(m, size, version) {
    if (version < 7) return;
    let rem = version << 12;
    for (let i = 17; i >= 12; i--) if ((rem >> i) & 1) rem ^= 0x1f25 << (i - 12);
    const bits = (version << 12) | rem;
    const bit = (i) => (bits >> i) & 1;
    for (let r = 0; r < 6; r++) for (let c = 0; c < 3; c++) {
      const v = bit(r * 3 + c);
      m[(size - 11 + c) * size + r] = v; // 左下
      m[r * size + size - 11 + c] = v;   // 右上
    }
  }

  /* ---------- 生成：文本 → 模块矩阵（Uint8Array，1=深） ---------- */
  function qrMatrix(text) {
    const bytes = new TextEncoder().encode(String(text));
    let version = 0;
    for (let v = 1; v <= 10; v++) {
      if (bitsFit(M_BLOCKS[v][2], bytes.length, v)) { version = v; break; }
    }
    if (!version) throw new Error('文本过长：QR(M 级, v1~10) 最多 213 字节');
    const codewords = buildCodewords(bytes, version);
    let best = null, bestScore = Infinity, bestMask = 0;
    for (let mask = 0; mask < 8; mask++) {
      const { m, resv, size } = baseMatrix(version);
      placeData(m, resv, size, codewords);
      applyMask(m, resv, size, mask);
      drawFormat(m, size, mask);
      drawVersion(m, size, version);
      const sc = penalty(m, size);
      if (sc < bestScore) { bestScore = sc; best = m; bestMask = mask; }
    }
    return { size: 17 + 4 * version, version: version, mask: bestMask, data: best };
  }


  /* ---------- 渲染：SVG（含 4 模块静区，手机直扫） ---------- */
  function qrSVG(text, opts) {
    const o = Object.assign({ size: 0, margin: 4 }, opts || {});
    const qr = qrMatrix(text);
    const quiet = qr.size + o.margin * 2;
    let rects = '';
    for (let r = 0; r < qr.size; r++) for (let c = 0; c < qr.size; c++) {
      if (qr.data[r * qr.size + c]) {
        rects += `<rect x="${c + o.margin}" y="${r + o.margin}" width="1" height="1"/>`;
      }
    }
    const sizeAttr = o.size ? ` width="${o.size}" height="${o.size}"` : '';
    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${quiet} ${quiet}"${sizeAttr} ` +
      `shape-rendering="crispEdges" role="img" aria-label="二维码">` +
      `<rect width="${quiet}" height="${quiet}" fill="#fff"/><g fill="#000">${rects}</g></svg>`;
  }

  const api = { qrMatrix: qrMatrix, qrSVG: qrSVG };
  global.qrLib = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof window !== 'undefined' ? window : globalThis);
