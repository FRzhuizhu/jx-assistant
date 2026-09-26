#!/usr/bin/env node
/**
 * 用官方仓库自带的 tsdown 构建宿主半边（lib/index.js）与浏览器半边（lib/client.js）。
 * 不需要独立安装任何依赖。
 */
import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'

const PKG_DIR = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(PKG_DIR, '../..')
const TSDOWN = join(ROOT, 'node_modules', 'tsdown', 'dist', 'run.mjs')

if (!existsSync(TSDOWN)) {
  console.error(`[build] 找不到官方仓库的 tsdown：${TSDOWN}，请先编译官方仓库。`)
  process.exit(1)
}

// 构建期解析依赖：把 .pnpm 仓库里的 zod 链进本包（幂等，仅构建用）。
const pnpmStore = join(ROOT, 'node_modules', '.pnpm')
const zodDir = readdirSync(pnpmStore).find(name => /^zod@\d/.test(name))
if (zodDir !== undefined) {
  const localModules = join(PKG_DIR, 'node_modules')
  const linkPath = join(localModules, 'zod')
  // lstat 判断链接本体：安装目录改名或 zod 升级后链接会悬空（existsSync 为 false），
  // 此时直接 symlinkSync 会报 EEXIST——删掉悬空链接再建即可自愈。
  let linkExists = true
  try { lstatSync(linkPath) } catch { linkExists = false }
  if (!linkExists || !existsSync(linkPath)) {
    if (linkExists) rmSync(linkPath, { recursive: true, force: true })
    mkdirSync(localModules, { recursive: true })
    symlinkSync(join(pnpmStore, zodDir, 'node_modules', 'zod'), linkPath, 'junction')
  }
}

rmSync(join(PKG_DIR, 'lib'), { recursive: true, force: true })
mkdirSync(join(PKG_DIR, 'lib'), { recursive: true })

for (const config of ['tsdown.host.ts', 'tsdown.client.ts']) {
  const result = spawnSync(process.execPath, [TSDOWN, '--config', join(PKG_DIR, config)], {
    cwd: PKG_DIR,
    stdio: 'inherit',
  })
  if (result.status !== 0) {
    console.error(`[build] ${config} 构建失败。`)
    process.exit(result.status ?? 1)
  }
}

// 去行尾空白：产物入库，上游 lefthook 的 whitespace 钩子（git diff --check）会拦
// 内联依赖带来的行尾空格；只影响行尾，行号不变，源码映射仍有效。
for (const file of ['lib/index.js', 'lib/client.js']) {
  const path = join(PKG_DIR, file)
  if (!existsSync(path)) continue
  const text = readFileSync(path, 'utf8')
  const trimmed = text.replace(/[ \t]+$/gmu, '')
  if (trimmed !== text) writeFileSync(path, trimmed)
}

console.log('[build] dsh-speech 构建完成：lib/index.js + lib/client.js')
