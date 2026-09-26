import { defineConfig } from 'tsdown'

/** 宿主半边：ESM，宿主包保持 external（由愈合池 junction 提供）。 */
export default defineConfig({
  name: 'dsh-speech/host',
  entry: { index: 'src/index.ts' },
  outDir: 'lib',
  format: 'esm',
  fixedExtension: false,
  platform: 'node',
  target: 'node20',
  dts: false,
  clean: false,
  external: ['@deepseek-ai/cordis', '@deepseek-ai/dsh-typert-protocol', '@deepseek-ai/schemastery'],
})
