import type {
  LibraryCookbookItem,
  LibraryCourseItem,
  LibraryPage,
  LibraryRecipeItem,
  LibraryType,
  RecipeBookmarkView,
} from "../../apiModels";
import RecipeCardContents from "../recipes/RecipeCardContents.tsx";
import BookmarkAction from "./BookmarkAction.tsx";
import CookbookBookmarkAction from "../cookbooks/CookbookBookmarkAction.tsx";
import CreatorCredits from "../creator/CreatorCredits.tsx";
import CourseBookmarkAction from "../courses/CourseBookmarkAction.tsx";

type LibraryItem = LibraryRecipeItem | LibraryCookbookItem | LibraryCourseItem;

export default function LibraryCollection({
  type,
  page,
  userID,
  busyBookmarkID,
  onBookmarkChange,
  onMutationUncertain,
  onRemoveUnavailable,
}: {
  type: LibraryType;
  page: LibraryPage<LibraryItem>;
  userID: string;
  busyBookmarkID: string;
  onBookmarkChange: (bookmarkID: string | null) => void;
  onMutationUncertain: () => void;
  onRemoveUnavailable: (bookmarkID: string) => void;
}) {
  const label = type;
  return (
    <section className="cookbook-dashboard" aria-labelledby="library-collection-title">
      <div className="section-heading">
        <div>
          <span className="eyebrow">My Library</span>
          <h2 id="library-collection-title">Your {label}</h2>
        </div>
      </div>
      <p role="status">Showing {page.items.length} of {page.total} {label}.</p>
      {page.items.length === 0 ? (
        <p className="cookbook-empty">My Library includes items you own, can access through a cookbook, or bookmark.</p>
      ) : (
        <div className="cookbook-grid">
          {page.items.map((item, index) => type === "recipes"
            ? "target_kind" in item ? <RecipeLibraryCard key={libraryKey(item, index)} item={item} userID={userID} busy={busyBookmarkID === item.bookmark_id} onChange={onBookmarkChange} onMutationUncertain={onMutationUncertain} onRemoveUnavailable={onRemoveUnavailable} /> : null
            : type === "cookbooks" ? ! ("target_kind" in item) && !("course" in item) ? <CookbookLibraryCard key={libraryKey(item, index)} item={item as LibraryCookbookItem} userID={userID} onChange={onBookmarkChange} onMutationUncertain={onMutationUncertain} onRemoveUnavailable={onRemoveUnavailable} /> : null
            : "course" in item || item.availability === "unavailable" && "relationships" in item && Array.isArray(item.relationships) && item.relationships.includes("saved") ? <CourseLibraryCard key={libraryKey(item, index)} item={item as LibraryCourseItem} userID={userID} busy={busyBookmarkID === item.bookmark_id} onChange={onBookmarkChange} onMutationUncertain={onMutationUncertain} onRemoveUnavailable={onRemoveUnavailable} /> : null)}
        </div>
      )}
    </section>
  );
}

function RecipeLibraryCard({
  item, userID, busy, onChange, onMutationUncertain, onRemoveUnavailable,
}: {
  item: LibraryRecipeItem;
  userID: string;
  busy: boolean;
  onChange: (bookmarkID: string | null) => void;
  onMutationUncertain: () => void;
  onRemoveUnavailable: (bookmarkID: string) => void;
}) {
  if (item.availability === "unavailable") {
    return <UnavailableCard kind="recipe" createdAt={item.created_at} bookmarkID={item.bookmark_id ?? ""} busy={busy} onRemove={onRemoveUnavailable} />;
  }
  if (item.target_kind === "remote" && item.remote_recipe) {
    const remote = item.remote_recipe;
    return (
      <article className="cookbook-card">
        <span className="eyebrow">Saved remote snapshot</span>
        <a className="recipe-card" href={`/recipe?remote_object_uri=${encodeURIComponent(remote.object_uri)}`} aria-label={`Open ${remote.recipe.title}`}>
          <RecipeCardContents title={remote.recipe.title} creatorName={remote.creator_name} stepCount={remote.recipe.steps.length} relationshipLabel={`From ${remote.source_origin}`} />
        </a>
        <p>Cached snapshot from {remote.source_origin}; updated {formatDate(remote.updated_at)}. This is not a live feed.</p>
        <RelationshipFacts relationships={item.relationships ?? []} ownedByCurrentUser={false} />
      <BookmarkAction currentUserID={userID} target={{ remote_object_uri: remote.object_uri }} bookmark={bookmarkForRecipe(item, remote.object_uri)} initialStatus="saved" busy={busy} libraryContext onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} />
      </article>
    );
  }
  const recipe = item.recipe;
  if (!recipe) return null;
  const bookmark = item.bookmark_id ? recipeBookmark(item.bookmark_id, recipe) : undefined;
  return (
    <article className="cookbook-card">
      <a className="recipe-card" href={`/recipe?id=${encodeURIComponent(recipe.id)}`} aria-label={`Open ${recipe.title}`}>
        <RecipeCardContents title={recipe.title} creatorName={recipe.creator_name} creatorAttributions={recipe.creator_attributions} stepCount={recipe.steps.length} relationshipLabel={recipe.visibility === "public" ? "Public recipe" : "Recipe you can access"} />
      </a>
      <RelationshipFacts relationships={item.relationships ?? []} ownedByCurrentUser={recipe.owner_id === userID} inOwnedCookbook={item.in_owned_cookbook === true} membershipRole={item.membership_role} />
      {item.cookbook_contexts?.map((context) => <span className="eyebrow" key={context.id}>Cookbook context{context.membership_role ? ` · ${context.membership_role}` : ""}</span>)}
      <BookmarkAction currentUserID={userID} target={{ local_recipe_id: recipe.id }} bookmark={bookmark} initialStatus={item.relationships?.includes("saved") ? "saved" : "unsaved"} busy={busy} libraryContext onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} />
    </article>
  );
}

function CookbookLibraryCard({ item, userID, onChange, onMutationUncertain, onRemoveUnavailable }: { item: LibraryCookbookItem; userID: string; onChange: (id: string | null) => void; onMutationUncertain: () => void; onRemoveUnavailable: (bookmarkID: string) => void }) {
  if (item.availability === "unavailable")
    return <UnavailableCard kind="cookbook" createdAt={item.created_at} bookmarkID={item.bookmark_id ?? ""} busy={false} onRemove={onRemoveUnavailable} />;
  const book = item.cookbook;
  if (!book) return null;
  return (
    <article className="cookbook-card">
      <a href={`/cookbook?id=${encodeURIComponent(book.id)}`} aria-label={`Open ${book.title}`}>
        <span className="eyebrow">{book.visibility} cookbook</span>
        <h3>{book.title}</h3>
        {book.description && <p>{book.description}</p>}
      </a>
      {book.owner_name && <p>Cookbook owner: {book.owner_name}</p>}
      <CreatorCredits attributions={book.creator_attributions} resourceKind="cookbook" currentUserID={userID} />
      <RelationshipFacts relationships={item.relationships ?? []} ownedByCurrentUser={book.owner_id === userID} membershipRole={item.membership_role} />
      <CookbookBookmarkAction cookbookID={book.id} title={book.title} currentUserID={userID} initialBookmarkID={item.relationships?.includes("saved") ? item.bookmark_id ?? null : null} libraryContext onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} />
    </article>
  );
}

function CourseLibraryCard({ item, userID, busy, onChange, onMutationUncertain, onRemoveUnavailable }: { item: LibraryCourseItem; userID: string; busy: boolean; onChange: (id: string | null) => void; onMutationUncertain: () => void; onRemoveUnavailable: (bookmarkID: string) => void }) {
  if (item.availability === "unavailable") return <UnavailableCard kind="course" createdAt={item.created_at} bookmarkID={item.bookmark_id} busy={busy} onRemove={onRemoveUnavailable} />;
  const course = item.course;
  return <article className="cookbook-card"><a href={`/course?id=${encodeURIComponent(course.id)}`} aria-label={`Open ${course.title}`}><span className="eyebrow">{course.visibility} course · {course.lesson_count} lessons</span><h3>{course.title}</h3>{course.description && <p>{course.description}</p>}</a><p>Owner: {course.owner_name}</p><RelationshipFacts relationships={item.relationships} ownedByCurrentUser={item.relationships.includes("owned")} /><CourseBookmarkAction courseID={course.id} title={course.title} currentUserID={userID} initialBookmarkID={item.relationships.includes("saved") ? item.bookmark_id ?? null : undefined} onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} /></article>;
}

function UnavailableCard({ kind, createdAt, bookmarkID, busy, onRemove }: { kind: "recipe" | "cookbook" | "course"; createdAt?: string; bookmarkID: string; busy: boolean; onRemove: (bookmarkID: string) => void }) {
  return (
    <article className="cookbook-card">
      <span className="eyebrow">Saved reference</span>
      <h3>Unavailable {kind}</h3>
      <p>This saved reference is not currently available.</p>
      {createdAt && <p>Saved <time dateTime={createdAt}>{formatDate(createdAt)}</time></p>}
      <button className="text-link" type="button" disabled={busy || !bookmarkID} onClick={() => onRemove(bookmarkID)}>
        {busy ? "Removing…" : `Remove unavailable ${kind} reference`}
      </button>
    </article>
  );
}

function RelationshipFacts({ relationships, ownedByCurrentUser, membershipRole, inOwnedCookbook = false }: { relationships: string[]; ownedByCurrentUser: boolean; membershipRole?: string; inOwnedCookbook?: boolean }) {
  const labels: string[] = [];
  if (ownedByCurrentUser) labels.push("Owned by you");
  if (relationships.includes("shared")) labels.push(membershipRole ? `Shared with you · ${membershipRole}` : "Shared with you");
  if (relationships.includes("saved")) labels.push("Saved to Library");
  if (inOwnedCookbook) labels.push("In a cookbook you own");
  return labels.length ? <p>{labels.join(" · ")}</p> : null;
}

function libraryKey(item: LibraryItem, index: number): string {
  if (item.availability === "unavailable") return `${item.bookmark_id ?? "unavailable"}:${index}`;
  if ("target_kind" in item && item.target_kind === "remote" && item.remote_recipe) return item.remote_recipe.object_uri;
  if ("recipe" in item && item.recipe) return `recipe:${item.recipe.id}`;
  if ("cookbook" in item && item.cookbook) return `cookbook:${item.cookbook.id}`;
  if ("course" in item && item.course) return `course:${item.course.id}`;
  return `library:${index}`;
}

function bookmarkForRecipe(item: LibraryRecipeItem, uri: string): RecipeBookmarkView | undefined {
  if (!item.bookmark_id) return undefined;
  return { id: item.bookmark_id, target_kind: "remote", target: { remote_object_uri: uri }, availability: "available", created_at: item.created_at ?? "" };
}

function recipeBookmark(id: string, recipe: NonNullable<LibraryRecipeItem["recipe"]>): RecipeBookmarkView {
  return { id, target_kind: "local", target: { local_recipe_id: recipe.id }, availability: "available", local_recipe: recipe, created_at: "" };
}

function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "an unknown date" : date.toLocaleDateString();
}
