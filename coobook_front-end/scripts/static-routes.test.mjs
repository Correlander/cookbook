import assert from 'node:assert/strict'
import { readdir } from 'node:fs/promises'
import { join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { staticRoutes } from './static-routes.mjs'

const pagesRoot = fileURLToPath(new URL('../src/pages/', import.meta.url))

async function astroPages(directory = pagesRoot) {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = await Promise.all(entries.map(async (entry) => {
    const path = join(directory, entry.name)
    return entry.isDirectory() ? astroPages(path) : entry.isFile() && entry.name.endsWith('.astro')
      ? [relative(pagesRoot, path).split(sep).join('/')]
      : []
  }))
  return files.flat().sort()
}

test('explicit static route manifest covers every Astro page exactly once', async () => {
  const declared = staticRoutes.map(({ source }) => source).sort()
  assert.deepEqual(declared, await astroPages())
  assert.equal(new Set(declared).size, declared.length, 'each Astro source is registered once')
})

test('static route manifest has unique explicit document aliases and policy routes', () => {
  const aliases = staticRoutes.flatMap(({ aliases: paths }) => paths)
  assert.equal(new Set(aliases).size, aliases.length, 'each URL alias maps to one document')
  assert.equal(new Set(staticRoutes.map(({ route }) => route)).size, staticRoutes.length, 'each document has one policy route')
  for (const page of staticRoutes) {
    assert.ok(page.aliases.includes(page.route), `${page.route} is served`)
    assert.ok(page.aliases.includes(`/${page.file}`), `${page.file} URL is served`)
  }
})
