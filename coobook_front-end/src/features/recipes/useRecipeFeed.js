import { useEffect, useRef, useState } from 'react'
import { fetchRecipePage, RECIPE_PAGE_SIZE } from './recipeFeedApi.js'

export default function useRecipeFeed({ signedIn, query }) {
  const [items, setItems] = useState([])
  const [loadedCount, setLoadedCount] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [loadMoreError, setLoadMoreError] = useState('')
  const listKey = `${signedIn}:${query.trim()}`
  const listKeyRef = useRef(listKey)
  listKeyRef.current = listKey

  useEffect(() => {
    let active = true
    setLoading(true)
    setLoadMoreError('')
    fetchRecipePage({ signedIn, query, offset: 0 })
      .then(data => {
        if (!active) return
        setItems(data)
        setLoadedCount(data.length)
        setHasMore(data.length === RECIPE_PAGE_SIZE)
        setLoadError(false)
      })
      .catch(() => {
        if (!active) return
        setLoadError(true)
        setItems([])
        setHasMore(false)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => { active = false }
  }, [signedIn, query])

  async function loadMore() {
    if (loadingMore) return
    const requestedListKey = listKey
    setLoadingMore(true)
    setLoadMoreError('')
    try {
      const data = await fetchRecipePage({ signedIn, query, offset: loadedCount })
      if (listKeyRef.current !== requestedListKey) return
      setItems(current => {
        const known = new Set(current.map(recipe => recipe.id))
        return [...current, ...data.filter(recipe => !known.has(recipe.id))]
      })
      setLoadedCount(count => count + data.length)
      setHasMore(data.length === RECIPE_PAGE_SIZE)
    } catch (error) {
      setLoadMoreError(error.message)
    } finally {
      setLoadingMore(false)
    }
  }

  function addRecipe(recipe) {
    setItems(current => [recipe, ...current])
  }

  function replaceRecipe(recipe) {
    setItems(current => current.map(item => item.id === recipe.id ? recipe : item))
  }

  function removeRecipe(id) {
    setItems(current => current.filter(item => item.id !== id))
  }

  function restoreRecipe(recipe) {
    if (query.trim() && !`${recipe.title} ${recipe.description}`.toLowerCase().includes(query.trim().toLowerCase())) return
    setItems(current => [recipe, ...current.filter(item => item.id !== recipe.id)].slice(0, RECIPE_PAGE_SIZE))
  }

  return { items, loading, loadingMore, loadError, loadMoreError, hasMore, loadMore, addRecipe, replaceRecipe, removeRecipe, restoreRecipe }
}
