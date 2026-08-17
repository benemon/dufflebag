import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let PlatformLabel, PlatformList

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ PlatformLabel, PlatformList } =
    await vite.ssrLoadModule('/src/components/PlatformLabel.tsx'))
})

after(async () => {
  await vite.close()
})

const render = (element) => renderToStaticMarkup(element)

test('a mapped platform renders a glyph whose accessible text names it', () => {
  for (const platform of ['docker', 'aws']) {
    const markup = render(React.createElement(PlatformLabel, { platform }))
    assert.match(markup, /<svg/, `no glyph rendered for ${platform}`)
    assert.match(
      markup,
      new RegExp(`<title[^>]*>${platform}<\\/title>`),
      `${platform} does not match Docker's accessible title semantics`,
    )
    assert.doesNotMatch(markup, /aria-hidden="true"/, 'a titled glyph must not be hidden from readers')
  }
})

// Packer's platform values are an open set; the literal-name path is the one
// that actually fires in the field, so it is asserted with a value absent
// from the mapping.
test('an unmapped platform renders its literal name, exactly as before', () => {
  const markup = render(React.createElement(PlatformLabel, { platform: 'some-future-builder' }))
  assert.equal(markup, 'some-future-builder')
})

test('a platform list renders each entry and an empty list renders a dash', () => {
  const markup = render(React.createElement(PlatformList, { platforms: ['docker', 'metal'] }))
  assert.match(markup, /<svg/)
  assert.match(markup, /metal/)
  assert.equal(render(React.createElement(PlatformList, { platforms: [] })), '—')
})
