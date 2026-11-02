import { useState } from 'react'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import RecipeCollection from './RecipeCollection.jsx'
import useRecipeFeed from './useRecipeFeed.js'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function jsonResponse(data) {
  return { ok: true, json: async () => data }
}

function deferred() {
  let resolve
  const promise = new Promise(done => { resolve = done })
  return { promise, resolve }
}

function Harness({ initialSignedIn = false, onOpenRecipe = () => {} }) {
  const [signedIn, setSignedIn] = useState(initialSignedIn)
  const [query, setQuery] = useState('')
  const feed = useRecipeFeed({ signedIn, query })
  return <>
    <label>Signed in<input type="checkbox" checked={signedIn} onChange={event => setSignedIn(event.target.checked)} /></label>
    <label>Search recipes<input value={query} onChange={event => setQuery(event.target.value)} /></label>
    <RecipeCollection
      signedIn={signedIn}
      query={query}
      feed={feed}
      onOpenRecipe={onOpenRecipe}
      onCreateRecipe={() => {}}
      renderRecipe={(recipe, onOpen) => <button key={recipe.id} onClick={() => onOpen(recipe)}>{recipe.title}</button>}
    />
  </>
}

describe('RecipeCollection', () => {
  it('loads the public feed, switches to the signed-in endpoint, and resets on search', async () => {
    const requests = []
    const onOpenRecipe = vi.fn()
    vi.stubGlobal('fetch', vi.fn(url => {
      requests.push(url)
      return Promise.resolve(jsonResponse([{ id: requests.length, title: 'Apple pie' }]))
    }))
    render(<Harness onOpenRecipe={onOpenRecipe} />)

    expect(screen.getByText('Loading recipes…')).toBeTruthy()
    expect(await screen.findByRole('button', { name: 'Apple pie' })).toBeTruthy()
    expect(requests[0]).toBe('/api/public/recipes?limit=24&offset=0')
    fireEvent.click(screen.getByRole('button', { name: 'Apple pie' }))
    expect(onOpenRecipe).toHaveBeenCalledWith({ id: 1, title: 'Apple pie' })

    fireEvent.click(screen.getByRole('checkbox', { name: 'Signed in' }))
    await waitFor(() => expect(requests.at(-1)).toBe('/api/recipes?limit=24&offset=0'))
    await screen.findAllByRole('button', { name: 'Apple pie' })

    fireEvent.change(screen.getByRole('textbox', { name: 'Search recipes' }), { target: { value: '  pie  ' } })
    await waitFor(() => expect(requests.at(-1)).toBe('/api/recipes?limit=24&offset=0&q=pie'))
  })

  it('deduplicates later pages and ignores a page response after the search changes', async () => {
    const laterPage = deferred()
    const requests = []
    vi.stubGlobal('fetch', vi.fn(url => {
      requests.push(url)
      if (url.includes('q=next')) return Promise.resolve(jsonResponse([{ id: 50, title: 'New search result' }]))
      if (url.endsWith('offset=0')) {
        return Promise.resolve(jsonResponse(Array.from({ length: 24 }, (_, index) => ({ id: index + 1, title: `Recipe ${index + 1}` }))))
      }
      if (url.endsWith('offset=24')) return laterPage.promise
      return Promise.resolve(jsonResponse([]))
    }))
    render(<Harness />)
    await screen.findByRole('button', { name: 'Recipe 1' })
    fireEvent.click(screen.getByRole('button', { name: 'Load more recipes' }))
    await waitFor(() => expect(requests).toContain('/api/public/recipes?limit=24&offset=24'))

    fireEvent.change(screen.getByRole('textbox', { name: 'Search recipes' }), { target: { value: 'next' } })
    expect(await screen.findByRole('button', { name: 'New search result' })).toBeTruthy()
    laterPage.resolve(jsonResponse([{ id: 24, title: 'Recipe 24 duplicate' }, { id: 25, title: 'Stale recipe' }]))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Stale recipe' })).toBeNull())
    expect(screen.queryByRole('button', { name: 'Recipe 24 duplicate' })).toBeNull()
    expect(screen.getByRole('button', { name: 'New search result' })).toBeTruthy()
  })

  it('appends new page results while keeping duplicate recipe IDs once', async () => {
    const requests = []
    vi.stubGlobal('fetch', vi.fn(url => {
      requests.push(url)
      if (url.endsWith('offset=0')) {
        return Promise.resolve(jsonResponse(Array.from({ length: 24 }, (_, index) => ({ id: index + 1, title: `Recipe ${index + 1}` }))))
      }
      return Promise.resolve(jsonResponse([{ id: 24, title: 'Duplicate recipe' }, { id: 25, title: 'Recipe 25' }]))
    }))
    render(<Harness />)
    await screen.findByRole('button', { name: 'Recipe 1' })
    fireEvent.click(screen.getByRole('button', { name: 'Load more recipes' }))
    expect(await screen.findByRole('button', { name: 'Recipe 25' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Duplicate recipe' })).toBeNull()
    expect(requests.at(-1)).toBe('/api/public/recipes?limit=24&offset=24')
  })

  it('renders an initial failure', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false }))
    render(<Harness />)
    expect(await screen.findByText('Recipes are unavailable right now. Please try again later.')).toBeTruthy()
  })

  it('reports a load-more failure', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(jsonResponse(Array.from({ length: 24 }, (_, index) => ({ id: index + 1, title: `Recipe ${index + 1}` }))))
      .mockResolvedValueOnce({ ok: false }))
    render(<Harness />)
    await screen.findByRole('button', { name: 'Recipe 1' })
    fireEvent.click(screen.getByRole('button', { name: 'Load more recipes' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Could not load more recipes.')
  })

  it('renders the empty search state', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse([])))
    render(<Harness />)
    expect(await screen.findByText('No recipes match “”. Try another search.')).toBeTruthy()
  })
})
