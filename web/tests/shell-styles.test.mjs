import assert from 'node:assert/strict'
import { readFileSync, readdirSync, rmSync } from 'node:fs'
import { after, before, test } from 'node:test'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { build } from 'vite'

const theme = readFileSync(new URL('../src/theme.css', import.meta.url), 'utf8')
const root = fileURLToPath(new URL('..', import.meta.url))
const outDir = join(tmpdir(), `dufflebag-shell-styles-${process.pid}`)
let builtCSS

before(async () => {
  await build({ root, logLevel: 'silent', build: { outDir, emptyOutDir: true } })
  const css = readdirSync(join(outDir, 'assets')).find((name) => name.endsWith('.css'))
  assert.ok(css, 'Vite emitted no CSS bundle')
  builtCSS = readFileSync(join(outDir, 'assets', css), 'utf8')
})

after(() => rmSync(outDir, { recursive: true, force: true }))

const rule = (selector) => {
  const start = theme.indexOf(`${selector} {`)
  assert.notEqual(start, -1, `missing ${selector} rule`)
  return theme.slice(start, theme.indexOf('}', start) + 1).replaceAll(/\s+/g, ' ')
}

// The spans of every `@layer patternfly{...}` block, by brace matching. The
// property under test is placement, not concatenation shape: vite 7 wrapped
// each PatternFly file in its own block, vite 8 merges them into one, and both
// are correct as long as the rules land inside.
const layerBlocks = (css) => {
  const blocks = []
  const open = /@layer patternfly\s*\{/g
  let match
  while ((match = open.exec(css)) !== null) {
    let depth = 1
    let end = open.lastIndex
    while (depth > 0 && end < css.length) {
      if (css[end] === '{') depth += 1
      else if (css[end] === '}') depth -= 1
      end += 1
    }
    blocks.push([open.lastIndex, end])
    open.lastIndex = end
  }
  return blocks
}

test('the built bundle puts PatternFly component CSS in a lower cascade layer', () => {
  const blocks = layerBlocks(builtCSS)
  assert.ok(blocks.length > 0, 'no patternfly cascade layer in the bundle')
  const layered = (selector) => {
    const at = builtCSS.indexOf(selector)
    assert.notEqual(at, -1, `missing ${selector} rule`)
    return blocks.some(([start, end]) => start <= at && at < end)
  }
  assert.ok(layered('.pf-v6-c-page{'), 'PatternFly Page CSS escaped the patternfly cascade layer')
  assert.ok(layered('.pf-v6-c-nav{'), 'PatternFly Nav CSS escaped the patternfly cascade layer')
  // The console's own rules must stay unlayered so they win over PatternFly's.
  assert.ok(!layered('.pf-v6-c-page.app-page{'), 'app shell CSS was swallowed by the patternfly layer')
  assert.ok(!layered('.app-global-nav .pf-v6-c-nav__link{'), 'app nav CSS was swallowed by the patternfly layer')
})

test('global navigation uses the settled type hierarchy', () => {
  const heading = rule('.app-global-nav .pf-v6-c-nav__section-title')
  const link = rule('.app-global-nav .pf-v6-c-nav__link')
  const current = rule('.app-global-nav .pf-v6-c-nav__link.pf-m-current')
  assert.match(heading, /font: 500 14px\/1\.4 "Red Hat Display", sans-serif;/)
  assert.match(link, /font: 400 14px\/1\.4 "Red Hat Text", sans-serif;/)
  assert.match(current, /font-weight: 500;/)
})

test('the console palette is scoped to light theme so PatternFly dark tokens win', () => {
  const palette = rule(':root:not(.pf-v6-theme-dark)')
  assert.match(palette, /--pf-t--global--background--color--100: #fff;/)
  assert.match(palette, /--pf-t--global--text--color--100: #151515;/)
  assert.doesNotMatch(rule(':root'), /--pf-t--global--background--color--100:/)
  assert.match(builtCSS, /:root:not\(\.pf-v6-theme-dark\)\{[^}]*--pf-t--global--background--color--100:#fff/)
  assert.match(builtCSS, /:root:where\(\.pf-v6-theme-dark\)\{/)
})

test('the sidebar surface follows semantic tokens in both themes', () => {
  const page = rule('.pf-v6-c-page.app-page')
  const navTitle = rule('.app-global-nav .pf-v6-c-nav__section-title')
  assert.match(
    page,
    /--pf-v6-c-page__sidebar--BackgroundColor: var\(--pf-t--global--background--color--floating--secondary--default\);/,
  )
  assert.match(
    page,
    /--pf-v6-c-page__sidebar--BorderInlineEndColor: var\(--pf-t--global--border--color--subtle\);/,
  )
  assert.match(navTitle, /color: var\(--pf-t--global--text--color--subtle\);/)
  assert.doesNotMatch(theme, /--sf[01]:|--tx1:|--bd:/)
})
