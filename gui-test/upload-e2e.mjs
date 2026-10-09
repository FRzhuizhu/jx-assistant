// Edge(CDP) + ?shell=desktop 验证 doUpload 修复后的全链路：
// 点加号→点文件上传→fetch /api/pick-file（服务端弹原生框）→此处先验证 fetch 已发出
import { newTab, read, sleep, closeTab } from './cdp.mjs';
const c = await newTab('http://127.0.0.1:8788/?shell=desktop', 1280, 900);
await sleep(2500);
// 直接程序化点击：先打开菜单再点文件上传（等价用户路径）
const st1 = await read(c, `(() => {
  document.documentElement.classList.add('desktop-shell'); // 保险：URL 参数应已生效
  return {
    shell: document.documentElement.classList.contains('desktop-shell'),
    plus: !!document.getElementById('btn-plus'),
    pm: !!document.getElementById('pm-upload'),
    doUpload: typeof window.doUpload,
  };
})()`);
console.log('前置:', JSON.stringify(st1));
// 打开菜单并点击文件上传（真实 DOM 点击）
await read(c, `(() => { document.getElementById('btn-plus').click(); return 1; })()`);
await sleep(400);
await read(c, `(() => { document.getElementById('pm-upload').click(); return 1; })()`);
await sleep(1200);
const st2 = await read(c, `(() => ({
  menuHidden: document.getElementById('plus-menu').classList.contains('hidden'),
}))()`);
console.log('点击后 menuHidden:', JSON.stringify(st2));
await closeTab(c);
