/* qr.js —— 远程入口二维码：标准库 qrcode(npm, MIT) 的薄封装。
   本体在 qrcode.lib.js（官方浏览器入口 lib/browser.js 的单文件打包，源码逐字未改，
   已与 npm 原包做过 create 矩阵逐位、toString SVG 逐字节一致性验证）。
   输出 SVG 字符串（含静区，单 path、crispEdges，手机直扫）。 */
(function (global) {
  'use strict';

  async function qrSVG(text, opts) {
    const o = Object.assign({ size: 208, margin: 4, level: 'M' }, opts || {});
    return await QRCode.toString(String(text), {
      type: 'svg',
      margin: o.margin,
      width: o.size,
      errorCorrectionLevel: o.level,
    });
  }

  global.qrLib = { qrSVG: qrSVG };
})(typeof window !== 'undefined' ? window : globalThis);
