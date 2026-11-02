export const RECIPE_PAGE_SIZE = 24

export interface RecipeFeedEntry {
  id: string
  title: string
  description?: string | null
}

export interface RecipePageRequest {
  signedIn: boolean
  query?: string
  offset?: number
}

export async function fetchRecipePage({ signedIn, query = '', offset = 0 }: RecipePageRequest): Promise<RecipeFeedEntry[]> {
  const endpoint = signedIn ? '/api/recipes' : '/api/public/recipes'
  const params = new URLSearchParams({ limit: String(RECIPE_PAGE_SIZE), offset: String(offset) })
  if (query.trim()) params.set('q', query.trim())

  const response = await fetch(`${endpoint}?${params}`)
  if (!response.ok) {
    throw new Error(offset === 0 ? 'Recipes unavailable' : 'Could not load more recipes.')
  }

  return response.json() as Promise<RecipeFeedEntry[]>
}
