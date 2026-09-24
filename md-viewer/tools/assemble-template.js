// Assemble viewer-template.html: inject the pinned marked.js build from the
// legacy template into the new skeleton, then smoke-test the render chain.
// Usage: node tools/assemble-template.js
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const root = path.join(__dirname, '..');
const legacyPath = path.join(root, 'viewer-template.legacy.html');
const srcPath = path.join(root, 'viewer-template.src.html');
const outPath = path.join(root, 'viewer-template.html');

// 1) extract marked from the legacy template
const legacy = fs.readFileSync(legacyPath, 'utf8');
const m = legacy.match(/\/\*\*\s*\n \* marked v[\s\S]*?<\/script>/);
if (!m) throw new Error('marked block not found in legacy template');
const markedJs = m[0].replace(/<\/script>\s*$/, '').trim();
if (!markedJs.startsWith('/**') || !/\}\)\);?\s*$/.test(markedJs) || markedJs.length < 5000) {
  throw new Error('marked block looks truncated');
}

// 2) inject into skeleton
let out = fs.readFileSync(srcPath, 'utf8');
if (!out.includes('/*__MARKED__*/')) throw new Error('skeleton missing MARKED placeholder');
out = out.replace('/*__MARKED__*/', markedJs);
for (const ph of ['__TITLE__', '__META__', '__B64__', '__KEEP_RUNNING__']) {
  if (!out.includes(ph)) throw new Error('output missing placeholder ' + ph);
}
fs.writeFileSync(outPath, out, 'utf8');
console.log('OK  assembled ->', outPath, `(${out.length} chars)`);

// 3) smoke test: run marked in a vm, parse a sample, extract headings the way
//    the viewer does (querySelectorAll('h1..h6') ~ regex over rendered HTML)
const sandbox = { exports: {}, module: { exports: {} }, globalThis: {} };
vm.createContext(sandbox);
vm.runInContext(markedJs, sandbox);
const marked = (sandbox.marked && sandbox.marked.parse)
  ? sandbox.marked
  : ((sandbox.exports && sandbox.exports.parse) ? sandbox.exports
  : (sandbox.module.exports && sandbox.module.exports.parse ? sandbox.module.exports : null));
if (!marked) throw new Error('marked did not load in vm');

const sample = [
  '# 一级标题 Main',
  '',
  '## 1.1 二级 `code` 标题',
  '正文段落，包含 **加粗** 与 [链接](https://example.com)。',
  '',
  '### 三级 <b>html</b> 标题',
  '',
  '## 1.2 中文标点：测试——目录',
  '',
  '#### 四级',
  '##### 五级',
  '###### 六级',
  '',
  '代码块中的 # 不是标题：',
  '',
  '    # not a heading',
  '',
  '| a | b |',
  '|---|---|',
  '| 1 | 2 |',
].join('\n');
const html = marked.parse(sample);

function extractHeadings(rendered) {
  const out = [];
  const re = /<h([1-6])[^>]*>([\s\S]*?)<\/h\1>/g;
  let m2;
  while ((m2 = re.exec(rendered)) !== null) {
    out.push({ level: +m2[1], text: m2[2].replace(/<[^>]+>/g, '').trim() });
  }
  return out;
}
const heads = extractHeadings(html);
const expect = [
  [1, '一级标题 Main'],
  [2, '1.1 二级 code 标题'],
  [3, '三级 html 标题'],
  [2, '1.2 中文标点：测试——目录'],
  [4, '四级'],
  [5, '五级'],
  [6, '六级'],
];
if (heads.length !== expect.length) {
  throw new Error(`heading count mismatch: got ${JSON.stringify(heads)}`);
}
expect.forEach(([lv, tx], i) => {
  if (heads[i].level !== lv || heads[i].text !== tx) {
    throw new Error(`heading ${i} mismatch: got ${JSON.stringify(heads[i])}, want [${lv},"${tx}"]`);
  }
});
if (/<h[1-6][^>]*># not a heading/.test(html)) throw new Error('code block leaked as heading');
console.log('OK  smoke test: marked + heading extraction,', heads.length, 'headings verified');
