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
  const requestGenerationRef = useRef(0)
  listKeyRef.current = listKey

  useEffect(() => {
    const requestGeneration = ++requestGenerationRef.current
    let active = true
    setLoading(true)
    setLoadingMore(false)
    setLoadMoreError('')
    const isCurrent = () => active && requestGenerationRef.current === requestGeneration
    fetchRecipePage({ signedIn, query, offset: 0 })
      .then(data => {
        if (!isCurrent()) return
        setItems(data)
        setLoadedCount(data.length)
        setHasMore(data.length === RECIPE_PAGE_SIZE)
        setLoadError(false)
      })
      .catch(() => {
        if (!isCurrent()) return
        setLoadError(true)
        setItems([])
        setHasMore(false)
      })
      .finally(() => {
        if (isCurrent()) setLoading(false)
      })
    return () => { active = false }
  }, [signedIn, query])

  async function loadMore() {
    if (loadingMore) return
    const requestedListKey = listKey
    const requestGeneration = requestGenerationRef.current
    const isCurrent = () => requestGenerationRef.current === requestGeneration && listKeyRef.current === requestedListKey
    setLoadingMore(true)
    setLoadMoreError('')
    try {
      const data = await fetchRecipePage({ signedIn, query, offset: loadedCount })
      if (!isCurrent()) return
      setItems(current => {
        const known = new Set(current.map(recipe => recipe.id))
        return [...current, ...data.filter(recipe => !known.has(recipe.id))]
      })
      setLoadedCount(count => count + data.length)
      setHasMore(data.length === RECIPE_PAGE_SIZE)
    } catch (error) {
      if (isCurrent()) setLoadMoreError(error.message)
    } finally {
      if (isCurrent()) setLoadingMore(false)
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
