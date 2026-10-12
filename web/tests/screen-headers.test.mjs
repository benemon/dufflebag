import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { test } from 'node:test'

// A screen header carries a title, crumbs and actions. A subtitle is for
// live data only (counts, who started a job, a bucket's own description);
// exposition belongs in the guides. Ruled 2026-10-12 after the audit of
// every header (duf-czaq); a new subtitle needs a reason here.
const liveDataHeaders = new Set(['Buckets.tsx', 'Build.tsx', 'PluginImportJob.tsx', 'Versions.tsx'])

test('screen headers carry no exposition', () => {
  const dir = new URL('../src/screens/', import.meta.url)
  const offenders = []
  for (const file of readdirSync(dir).filter((name) => name.endsWith('.tsx'))) {
    const source = readFileSync(new URL(file, dir), 'utf8')
    for (const match of source.matchAll(/<ScreenHeader[\s\S]*?\/>|<ScreenHeader[\s\S]*?>[\s\S]*?<\/ScreenHeader>/g)) {
      if (/\bdescription=/.test(match[0]) && !liveDataHeaders.has(file)) offenders.push(file)
    }
  }
  assert.deepEqual(offenders, [], 'a header subtitle outside the live-data set; move the words to the guide')
})
