export default function RecipeCollection({ signedIn, query, feed, onOpenRecipe, onCreateRecipe, renderRecipe, addIcon, searchIcon }) {
  return <section className="recipe-section" id="recipes">
    <div className="section-heading">
      <div>
        <span className="eyebrow">{signedIn ? 'From your kitchen' : 'A few good things'}</span>
        <h2>{signedIn ? 'Your recipes' : 'Find your next favorite'}</h2>
      </div>
      {signedIn && <button className="text-link" onClick={onCreateRecipe}>Add a recipe {addIcon}</button>}
    </div>
    {feed.loading ? <p className="empty-state">Loading recipes…</p> : feed.loadError ? <div className="empty-state"><p>Recipes are unavailable right now. Please try again later.</p></div> : feed.items.length ? <>
      <div className="recipe-grid">{feed.items.map(recipe => renderRecipe(recipe, onOpenRecipe))}</div>
      {feed.loadMoreError && <p className="form-message" role="alert">{feed.loadMoreError}</p>}
      {feed.hasMore && <button className="button button--green" type="button" onClick={feed.loadMore} disabled={feed.loadingMore}>{feed.loadingMore ? 'Loading…' : 'Load more recipes'}</button>}
    </> : <div className="empty-state">{searchIcon}<p>No recipes match “{query}”. Try another search.</p></div>}
  </section>
}
