import assert from 'node:assert/strict'
import test from 'node:test'
import { fetchRecipePage } from './recipeFeedApi.js'

test('requests public and signed-in recipe pages with the existing query shape', async t => {
  const calls = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = async url => {
    calls.push(url)
    return { ok: true, json: async () => [] }
  }
  t.after(() => { globalThis.fetch = originalFetch })

  await fetchRecipePage({ signedIn: false, query: '  apple pie  ' })
  await fetchRecipePage({ signedIn: true, query: '', offset: 24 })

  assert.deepEqual(calls, [
    '/api/public/recipes?limit=24&offset=0&q=apple+pie',
    '/api/recipes?limit=24&offset=24',
  ])
})

test('uses the distinct initial and later page failures', async t => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => ({ ok: false })
  t.after(() => { globalThis.fetch = originalFetch })

  await assert.rejects(fetchRecipePage({ signedIn: false }), { message: 'Recipes unavailable' })
  await assert.rejects(fetchRecipePage({ signedIn: true, offset: 24 }), { message: 'Could not load more recipes.' })
})
