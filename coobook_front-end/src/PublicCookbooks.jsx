import { useEffect, useRef, useState } from 'react';
import useModalFocus from './useModalFocus.js';

export default function PublicCookbooks() {
  const [query, setQuery] = useState('');
  const [cookbooks, setCookbooks] = useState([]);
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState(null);
  const queryRef = useRef('');
  queryRef.current = query.trim();

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    const params = new URLSearchParams({ limit: '24', offset: '0' });
    if (query.trim()) params.set('q', query.trim());
    fetch(`/api/public/cookbooks?${params}`)
      .then(async (response) => {
        if (!response.ok) throw new Error('Public cookbooks are unavailable.');
        return response.json();
      })
      .then((data) => {
        if (!active) return;
        setCookbooks(data);
        setOffset(data.length);
        setHasMore(data.length === 24);
      })
      .catch((loadError) => {
        if (active) {
          setError(loadError.message);
          setCookbooks([]);
          setHasMore(false);
        }
      })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [query]);

  async function loadMore() {
    if (loadingMore) return;
    const requestedQuery = queryRef.current;
    setLoadingMore(true);
    setError('');
    try {
      const params = new URLSearchParams({ limit: '24', offset: String(offset) });
      if (query.trim()) params.set('q', query.trim());
      const response = await fetch(`/api/public/cookbooks?${params}`);
      if (!response.ok) throw new Error('Could not load more cookbooks.');
      const data = await response.json();
      if (queryRef.current !== requestedQuery) return;
      setCookbooks((current) => [...current, ...data]);
      setOffset((current) => current + data.length);
      setHasMore(data.length === 24);
    } catch (loadError) {
      setError(loadError.message);
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <section className="public-cookbooks" id="cookbooks" aria-labelledby="public-cookbooks-title">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Shared by their keepers</span>
          <h2 id="public-cookbooks-title">Browse public cookbooks</h2>
        </div>
        <label className="cookbook-search">
          <span className="visually-hidden">Search public cookbooks</span>
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search cookbooks…" />
        </label>
      </div>
      {loading ? <p className="cookbook-empty">Loading public cookbooks…</p>
        : error && cookbooks.length === 0 ? <p className="form-message" role="alert">{error}</p>
          : cookbooks.length === 0 ? <p className="cookbook-empty">No public cookbooks are available here yet.</p>
            : <div className="cookbook-grid">
              {cookbooks.map((book) => (
                <article className="cookbook-card" key={book.id}>
                  <span className="notebook-banner__icon" aria-hidden="true">✿</span>
                  <h3>{book.title}</h3>
                  {book.description && <p>{book.description}</p>}
                  <span className="recipe-card__category">{book.recipe_count} public {book.recipe_count === 1 ? 'recipe' : 'recipes'}</span>
                  <button className="text-link" type="button" onClick={() => setSelected(book)}>Open cookbook</button>
                </article>
              ))}
            </div>}
      {error && cookbooks.length > 0 && <p className="form-message" role="alert">{error}</p>}
      {hasMore && <button className="button button--green" type="button" onClick={loadMore} disabled={loadingMore}>
        {loadingMore ? 'Loading…' : 'Load more cookbooks'}
      </button>}
      {selected && <PublicCookbookDialog key={selected.id} book={selected} onClose={() => setSelected(null)} />}
    </section>
  );
}

function PublicCookbookDialog({ book, onClose }) {
  const dialogRef = useRef(null);
  useModalFocus(dialogRef, onClose);
  const [query, setQuery] = useState('');
  const [recipes, setRecipes] = useState([]);
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState('');
  const [selectedRecipe, setSelectedRecipe] = useState(null);
  const queryRef = useRef('');
  queryRef.current = query.trim();

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    const params = new URLSearchParams({ limit: '24', offset: '0' });
    if (query.trim()) params.set('q', query.trim());
    fetch(`/api/public/cookbooks/${encodeURIComponent(book.id)}/recipes?${params}`)
      .then(async (response) => {
        if (!response.ok) throw new Error('Could not load this cookbook.');
        return response.json();
      })
      .then((data) => {
        if (!active) return;
        setRecipes(data);
        setOffset(data.length);
        setHasMore(data.length === 24);
      })
      .catch((loadError) => {
        if (active) {
          setError(loadError.message);
          setRecipes([]);
          setHasMore(false);
        }
      })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [book.id, query]);

  async function loadMore() {
    if (loadingMore) return;
    const requestedQuery = queryRef.current;
    setLoadingMore(true);
    setError('');
    try {
      const params = new URLSearchParams({ limit: '24', offset: String(offset) });
      if (query.trim()) params.set('q', query.trim());
      const response = await fetch(`/api/public/cookbooks/${encodeURIComponent(book.id)}/recipes?${params}`);
      if (!response.ok) throw new Error('Could not load more recipes.');
      const data = await response.json();
      if (queryRef.current !== requestedQuery) return;
      setRecipes((current) => [...current, ...data]);
      setOffset((current) => current + data.length);
      setHasMore(data.length === 24);
    } catch (loadError) {
      setError(loadError.message);
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className="dialog-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section ref={dialogRef} tabIndex={-1} className="signin-dialog public-cookbook-dialog" role="dialog" aria-modal="true" aria-labelledby="public-cookbook-dialog-title">
        <button className="icon-button dialog-close" type="button" aria-label="Close cookbook" onClick={onClose}>×</button>
        {selectedRecipe ? <>
          <button className="text-link" type="button" onClick={() => setSelectedRecipe(null)}>Back to {book.title}</button>
          {selectedRecipe.image && <img className="recipe-dialog__image" src={selectedRecipe.image} alt={selectedRecipe.title} />}
          <span className="eyebrow">{selectedRecipe.creator_name}</span>
          <h2 id="public-cookbook-dialog-title">{selectedRecipe.title}</h2>
          {selectedRecipe.description && <p>{selectedRecipe.description}</p>}
          {(selectedRecipe.servings || selectedRecipe.prep_time_minutes || selectedRecipe.cook_time_minutes) && <p className="dialog-note">
            {[selectedRecipe.servings && `Serves ${selectedRecipe.servings}`, selectedRecipe.prep_time_minutes && `Prep ${selectedRecipe.prep_time_minutes} min`, selectedRecipe.cook_time_minutes && `Cook ${selectedRecipe.cook_time_minutes} min`].filter(Boolean).join(' · ')}
          </p>}
          <div className="recipe-columns">
            <div><h3>Ingredients</h3><ul>{selectedRecipe.ingredients.map((item, index) => <li key={`${item.name}-${index}`}>{[item.quantity, item.name].filter(Boolean).join(' ')}{item.notes && <small> — {item.notes}</small>}</li>)}</ul></div>
            <div><h3>Instructions</h3><ol>{selectedRecipe.steps.map((step, index) => <li key={index}>{step.text}</li>)}</ol></div>
          </div>
        </> : <>
          <span className="eyebrow">Public cookbook</span>
          <h2 id="public-cookbook-dialog-title">{book.title}</h2>
          {book.description && <p>{book.description}</p>}
          <label htmlFor="public-recipe-search">Search recipes in this cookbook</label>
          <input id="public-recipe-search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search recipes…" />
          {loading ? <p>Loading recipes…</p> : error && recipes.length === 0 ? <p className="form-message" role="alert">{error}</p>
            : recipes.length === 0 ? <p>No public recipes are available in this cookbook.</p>
              : <div className="passkey-list">
                {recipes.map((recipe) => <button className="public-recipe-row" key={recipe.id} type="button" onClick={() => setSelectedRecipe(recipe)}>
                  <span>{recipe.title}<small>{recipe.creator_name} · {recipe.description}</small></span>
                  <span aria-hidden="true">→</span>
                </button>)}
              </div>}
          {error && recipes.length > 0 && <p className="form-message" role="alert">{error}</p>}
          {hasMore && <button className="text-link" type="button" onClick={loadMore} disabled={loadingMore}>{loadingMore ? 'Loading…' : 'Load more recipes'}</button>}
        </>}
      </section>
    </div>
  );
}
