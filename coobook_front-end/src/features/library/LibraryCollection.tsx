import type {
  LibraryCookbookItem,
  LibraryCourseItem,
  LibraryMenuItem,
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

type LibraryItem = LibraryRecipeItem | LibraryCookbookItem | LibraryCourseItem | LibraryMenuItem;

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
            : "menu" in item ? <MenuLibraryCard key={libraryKey(item, index)} item={item} />
            : "course" in item || item.availability === "unavailable" && "relationships" in item && Array.isArray(item.relationships) && item.relationships.includes("saved") ? <CourseLibraryCard key={libraryKey(item, index)} item={item as LibraryCourseItem} userID={userID} busy={busyBookmarkID === item.bookmark_id} onChange={onBookmarkChange} onMutationUncertain={onMutationUncertain} onRemoveUnavailable={onRemoveUnavailable} /> : null)}
        </div>
      )}
    </section>
  );
}

function MenuLibraryCard({ item }: { item: LibraryMenuItem }) {
  return (
    <article className="cookbook-card">
      <a href={`/menu?id=${encodeURIComponent(item.menu.id)}`} aria-label={`Open ${item.menu.title}`}>
        <span className="eyebrow">Private menu</span>
        <h3>{item.menu.title}</h3>
        {item.menu.note && <p>{item.menu.note}</p>}
      </a>
      <p>Owned by you · {item.menu.entries.length} {item.menu.entries.length === 1 ? "recipe" : "recipes"}</p>
    </article>
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
    return <UnavailableCard kind="recipe" createdAt={item.created_at} bookmarkID={item.bookmark_id ?? ""} busy={busy} onRemove={onRemoveUnavailable} saveState={item.save_state} moderatedAt={item.moderated_at} moderationReason={item.moderation_reason} sourceState={item.source_state} />;
  }
  if (item.target_kind === "remote" && item.snapshot_recipe) {
    const snapshot = item.snapshot_recipe;
    const bookmark = bookmarkForRecipe(item, snapshot.object_uri);
    return (
      <article className="cookbook-card">
        <span className="eyebrow">Private saved copy{snapshot.sequence ? ` · source sequence ${snapshot.sequence}` : ""}</span>
        <a className="recipe-card" href={`/recipe?bookmark_id=${encodeURIComponent(item.bookmark_id ?? "")}`} aria-label={`Open ${snapshot.recipe.title}`}>
          <RecipeCardContents title={snapshot.recipe.title} creatorName={snapshot.creator_name} stepCount={snapshot.recipe.steps.length} relationshipLabel={`From ${snapshot.source_origin}`} />
        </a>
        {item.provenance_state === "user_imported_unverified" ? (
          <p>Imported copy · source not verified. This label does not verify authorship. Source changes do not update this copy.</p>
        ) : (
          <p>Frozen private copy from {snapshot.source_origin}. Source changes do not update this copy.</p>
        )}
        {item.source_state && <p>Source status: {formatSourceState(item.source_state)}.</p>}
        <RelationshipFacts relationships={item.relationships ?? []} ownedByCurrentUser={false} />
        <BookmarkAction currentUserID={userID} target={{ remote_object_uri: snapshot.object_uri }} bookmark={bookmark} initialStatus="saved" busy={busy} libraryContext onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} />
      </article>
    );
  }
  if (item.target_kind === "remote" && item.remote_recipe) {
    const remote = item.remote_recipe;
    const bookmark = bookmarkForRecipe(item, remote.object_uri);
    return (
      <article className="cookbook-card">
        <span className="eyebrow">Saved reference</span>
        <a className="recipe-card" href={`/recipe?remote_object_uri=${encodeURIComponent(remote.object_uri)}`} aria-label={`Open ${remote.recipe.title}`}>
          <RecipeCardContents title={remote.recipe.title} creatorName={remote.creator_name} stepCount={remote.recipe.steps.length} relationshipLabel={`From ${remote.source_origin}`} />
        </a>
        <p>Reference to {remote.source_origin}; current cached content may change or become unavailable.</p>
        <RelationshipFacts relationships={item.relationships ?? []} ownedByCurrentUser={false} />
      <BookmarkAction currentUserID={userID} target={{ remote_object_uri: remote.object_uri }} bookmark={bookmark} initialStatus="saved" busy={busy} libraryContext onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} />
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
  return <article className="cookbook-card"><a href={`/course?id=${encodeURIComponent(course.id)}`} aria-label={`Open ${course.title}`}><span className="eyebrow">{course.visibility} course · {course.lesson_count} lessons</span><h3>{course.title}</h3>{course.description && <p>{course.description}</p>}</a><p>Owner: {course.owner_name}</p>{course.creator_name && <p>Unverified creator name: {course.creator_name}</p>}<CreatorCredits attributions={course.creator_attributions} resourceKind="course" currentUserID={userID} /><RelationshipFacts relationships={item.relationships} ownedByCurrentUser={item.relationships.includes("owned")} /><CourseBookmarkAction courseID={course.id} title={course.title} currentUserID={userID} initialBookmarkID={item.relationships.includes("saved") ? item.bookmark_id ?? null : undefined} onMutation={() => onChange(null)} onMutationUncertain={onMutationUncertain} /></article>;
}

function UnavailableCard({ kind, createdAt, bookmarkID, busy, onRemove, saveState, moderatedAt, moderationReason, sourceState }: { kind: "recipe" | "cookbook" | "course"; createdAt?: string; bookmarkID: string; busy: boolean; onRemove: (bookmarkID: string) => void; saveState?: string; moderatedAt?: string; moderationReason?: string; sourceState?: string }) {
  const blocked = saveState === "restricted" || saveState === "removed";
  return (
    <article className="cookbook-card">
      <span className="eyebrow">{blocked ? "Private copy blocked" : "Saved reference"}</span>
      <h3>{blocked ? "Blocked private copy" : `Unavailable ${kind}`}</h3>
      <p>{saveState === "restricted"
        ? "This private copy is restricted by this instance and cannot be read."
        : saveState === "removed"
        ? "This private copy was removed by this instance and cannot be read."
        : "This saved reference is not currently available."}</p>
      {blocked && moderationReason && <p>Reason: {moderationReason}{moderatedAt ? ` · ${formatDate(moderatedAt)}` : ""}</p>}
      {blocked && sourceState && <p>Source status: {formatSourceState(sourceState)}.</p>}
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
  if ("menu" in item) return `menu:${item.menu.id}`;
  if (item.availability === "unavailable") return `${item.bookmark_id ?? "unavailable"}:${index}`;
  if ("target_kind" in item && item.target_kind === "remote" && item.remote_recipe) return item.remote_recipe.object_uri;
  if ("recipe" in item && item.recipe) return `recipe:${item.recipe.id}`;
  if ("cookbook" in item && item.cookbook) return `cookbook:${item.cookbook.id}`;
  if ("course" in item && item.course) return `course:${item.course.id}`;
  return `library:${index}`;
}

function bookmarkForRecipe(item: LibraryRecipeItem, uri: string): RecipeBookmarkView | undefined {
  if (!item.bookmark_id) return undefined;
  return {
    id: item.bookmark_id, target_kind: "remote", target: { remote_object_uri: uri }, availability: "available", created_at: item.created_at ?? "",
    ...(item.save_state ? { save_state: item.save_state } : {}),
    ...(item.version_id ? { version_id: item.version_id } : {}),
    ...(item.source_state ? { source_state: item.source_state } : {}),
    ...(item.provenance_state ? { provenance_state: item.provenance_state } : {}),
    ...(item.publisher_retention_claim ? { publisher_retention_claim: item.publisher_retention_claim } : {}),
  };
}

function recipeBookmark(id: string, recipe: NonNullable<LibraryRecipeItem["recipe"]>): RecipeBookmarkView {
  return { id, target_kind: "local", target: { local_recipe_id: recipe.id }, availability: "available", local_recipe: recipe, created_at: "" };
}

function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "an unknown date" : date.toLocaleDateString();
}

function formatSourceState(value: string): string {
  switch (value) {
    case "published": return "available at last check";
    case "withdrawn": return "withdrawn by the source";
    case "disconnected": return "source disconnected";
    case "unreachable": return "source unreachable";
    case "unknown_legacy": return "unavailable or unknown";
    default: return "status unavailable";
  }
}
