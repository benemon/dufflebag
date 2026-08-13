import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { createServer } from 'vite'

let vite
let TypedConfirmModalView

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
})

after(async () => {
  await vite.close()
})

const findElement = (node, predicate) => {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findElement(child, predicate)
      if (found) return found
    }
    return null
  }
  if (!React.isValidElement(node)) return null
  if (predicate(node)) return node
  return findElement(node.props.children, predicate)
}

const view = (confirmation, over = {}) => TypedConfirmModalView({
  title: 'Delete images?', body: React.createElement('p', null, 'Permanent.'),
  expected: 'images', verb: 'Delete bucket', busy: false, confirmation,
  onConfirmationChange: () => {}, onConfirm: () => {}, onCancel: () => {}, ...over,
})

const danger = (tree) => findElement(tree, (element) => element.props.variant === 'danger')
const cancel = (tree) => findElement(tree, (element) => element.props.children === 'Cancel')

test('typed confirmation enables danger only for the exact case-sensitive match', () => {
  assert.equal(danger(view('')).props.isDisabled, true)
  assert.equal(danger(view('Images')).props.isDisabled, true)
  assert.equal(danger(view('images ')).props.isDisabled, true)
  assert.equal(danger(view('images')).props.isDisabled, false)
})

test('busy typed confirmation disables both buttons and loads danger', () => {
  const modal = view('images', { busy: true })
  assert.equal(danger(modal).props.isDisabled, true)
  assert.equal(danger(modal).props.isLoading, true)
  assert.equal(cancel(modal).props.isDisabled, true)
})
