import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const makefile = readFileSync(new URL('../../Makefile', import.meta.url), 'utf8')
const childTemplate = readFileSync(
  new URL('../../e2e/packer/demo-child.pkr.hcl', import.meta.url),
  'utf8',
)
const target = makefile.slice(
  makefile.indexOf('demo-publish:'),
  makefile.indexOf('\n.PHONY:', makefile.indexOf('demo-publish:')),
)

test('demo-publish lets Packer generate parent and child fingerprints', () => {
  assert.doesNotMatch(target, /HCP_PACKER_BUILD_FINGERPRINT/)
  assert.match(target, /run_label="dufflebag-sbom-demo-\$\$epoch"/)
  // Four lineage builds plus the per-distro loop, which passes the same label
  // so a demo run's images are identifiable together.
  assert.equal((target.match(/-var "run_label=\$\$run_label"/g) ?? []).length, 5)
})

test('demo-publish builds parent, child, parent, parent without moving the child pin', () => {
  const builds = [...target.matchAll(
    /\$\(PACKER_E2E_PACKER\) build[\s\S]*?\n\s*(e2e\/packer\/(?:demo-sbom|demo-child)\.pkr\.hcl);/g,
  )].map((match) => match[1])
  assert.deepEqual(builds, [
    'e2e/packer/demo-sbom.pkr.hcl',
    'e2e/packer/demo-child.pkr.hcl',
    'e2e/packer/demo-sbom.pkr.hcl',
    'e2e/packer/demo-sbom.pkr.hcl',
  ])
})

test('the demo publishes one bucket per corpus distro, each deliberately old', () => {
  // 'DEMO_ORG ' with the trailing space: a longer DEMO_ORG*-prefixed variable
  // earlier in the file would otherwise match first and collapse the slice.
  const matrix = makefile.slice(makefile.indexOf('DEMO_DISTROS ?='), makefile.indexOf('DEMO_ORG '))
  // Deliberately old: a current image would very likely show nothing, and a
  // console reading "no known findings" teaches the reader nothing about what
  // the scanner does.
  for (const [bucket, image] of [
    ['demo-alpine', 'alpine:3.17'],
    ['demo-debian', 'debian:11'],
    ['demo-ubi', 'registry.access.redhat.com/ubi8/ubi:latest'],
    ['demo-ubuntu', 'ubuntu:20.04'],
  ]) {
    assert.ok(matrix.includes(`${bucket}=${image}`), `DEMO_DISTROS is missing ${bucket}=${image}`)
  }
  // The loop must build the distro template, not the lineage one, or every
  // distro would land in the lineage bucket.
  assert.match(target, /for spec in \$\(DEMO_DISTROS\)/)
  assert.match(target, /-var "bucket_name=\$\$distro_bucket"/)
  assert.match(target, /e2e\/packer\/demo-distro\.pkr\.hcl/)
})

test('a failed distro build fails the target rather than being skipped', () => {
  assert.match(target, /FAIL demo-publish: \$\$distro_bucket build failed/)
})

test('UBI and Ubuntu publish v1, pin release through the compat API, then publish v2', () => {
  const unescaped = target.replaceAll('\\', '')
  assert.match(
    target,
    /for spec in \$\(DEMO_DISTROS\)[\s\S]*?build_distro "\$\$bucket" "\$\$image";[\s\S]*?case "\$\$bucket" in[\s\S]*?demo-ubi\|demo-ubuntu\)[\s\S]*?create_release_channel "\$\$bucket";[\s\S]*?build_distro "\$\$bucket" "\$\$image";/,
  )
  assert.match(unescaped, /"name":"release","restricted":false,"version_fingerprint":"\$\$v1_fingerprint"/)
  assert.match(target, /\$\$release_bucket\/channels\/latest/)
  assert.match(target, /\$\(DEMO_DIR\)\/root\.json/)
  assert.match(
    target,
    /SKIP demo-publish: \$\$release_bucket release channel needs publisher\+; provided HCP_CLIENT principal was refused/,
  )
})

test('the demo child correlates to the parent through the latest channel', () => {
  assert.match(
    childTemplate,
    /data "hcp-packer-version" "parent" {[\s\S]*?bucket_name\s+= "dufflebag-sbom-e2e"[\s\S]*?channel_name\s+= "latest"[\s\S]*?}/,
  )
  assert.match(
    childTemplate,
    /version_fingerprint\s+= data\.hcp-packer-version\.parent\.fingerprint/,
  )
  assert.match(
    childTemplate,
    /image\s+= data\.hcp-packer-artifact\.parent\.external_identifier/,
  )
})
