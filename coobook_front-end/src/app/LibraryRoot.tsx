import { useCallback, useEffect, useRef, useState } from "react";
import type { LibraryCookbookItem, LibraryCourseItem, LibraryPage, LibraryRecipeItem, LibraryType } from "../apiModels";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import { SessionCheckError } from "./AppSession.tsx";
import LibraryCollection from "../features/library/LibraryCollection.tsx";
import { fetchLibraryCourses, fetchLibraryPage, removeCookbookBookmark, removeCourseBookmark, removeRecipeBookmark } from "../features/library/libraryApi.ts";
import ViewSwitcher from "../components/ViewSwitcher.tsx";
import Toast from "../shared/Toast.tsx";

type LibraryItem = LibraryRecipeItem | LibraryCookbookItem | LibraryCourseItem;
type LibraryPageState = LibraryPage<LibraryItem> & { type: LibraryType; actorID: string; query: string };

function readType(): LibraryType {
  const requested = new URLSearchParams(window.location.search).get("type");
  return requested === "cookbooks" || requested === "courses" ? requested : "recipes";
}

function readQuery(): string {
  return new URLSearchParams(window.location.search).get("q") ?? "";
}

const libraryTypeOptions = [
  { value: "recipes", label: "Recipes" },
  { value: "cookbooks", label: "Cookbooks" },
  { value: "courses", label: "Courses" },
] as const;

function SignInRequired() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="library-signin-title">
      <h2 id="library-signin-title">Sign in to see your Library</h2>
      <p>Your recipes and cookbooks are tied to your account.</p>
      <a className="button button--green" href="/?signin=1">Sign in</a>
    </section>
  );
}

function LibraryPageView() {
  const { state, currentUser, refreshSession } = useAppSession();
  const authenticatedActorID = state.status === "authenticated" ? state.currentUser.id : "";
  const [type, setType] = useState<LibraryType>("recipes");
  const [query, setQuery] = useState("");
  const typeRef = useRef<LibraryType>("recipes");
  const queryRef = useRef("");
  const [page, setPage] = useState<LibraryPageState | null>(null);
  const [nextOffset, setNextOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [moreError, setMoreError] = useState("");
  const [notice, setNotice] = useState<{ actorID: string; message: string } | null>(null);
  const noticeRevision = useRef(0);
  const [mutationError, setMutationError] = useState("");
  const [busyBookmarkID, setBusyBookmarkID] = useState("");
  const [coursesEnabled, setCoursesEnabled] = useState(false);
  const [hasSavedCourses, setHasSavedCourses] = useState(false);
  const generation = useRef(0);
  const probeGeneration = useRef(0);
  const loadingMoreRef = useRef(false);
  const actorRef = useRef(authenticatedActorID);
  actorRef.current = authenticatedActorID;

  useEffect(() => {
    setNotice(null);
    setMutationError("");
  }, [authenticatedActorID]);

  useEffect(() => {
    const synchronize = () => {
      const queryType = new URLSearchParams(window.location.search).get("type");
      const nextType = readType();
      if (queryType !== "recipes" && queryType !== "cookbooks" && queryType !== "courses") {
        const params = new URLSearchParams(window.location.search);
        params.set("type", nextType);
        window.history.replaceState(null, "", `${window.location.pathname}?${params}`);
      }
      typeRef.current = nextType;
      const nextQuery = nextType === "recipes" ? readQuery() : "";
      queryRef.current = nextQuery;
      setQuery(nextQuery);
      if (nextType !== "recipes" && new URLSearchParams(window.location.search).has("q")) {
        const params = new URLSearchParams(window.location.search);
        params.delete("q");
        window.history.replaceState(null, "", `${window.location.pathname}?${params}`);
      }
      setType(nextType);
    };
    synchronize();
    window.addEventListener("popstate", synchronize);
    return () => window.removeEventListener("popstate", synchronize);
  }, []);

  const loadFirstPage = useCallback(async (requestedType: LibraryType, actorID: string, requestedQuery: string, keepNotice = false) => {
    const request = ++generation.current;
    loadingMoreRef.current = false;
    setLoading(true);
    setLoadingMore(false);
    setError("");
    setMoreError("");
    if (!keepNotice) setNotice(null);
    try {
      const result = requestedType === "courses"
        ? await fetchLibraryCourses(0)
        : requestedType === "recipes"
        ? await fetchLibraryPage("recipes", 0, requestedQuery)
        : await fetchLibraryPage("cookbooks", 0);
      if (generation.current !== request || typeRef.current !== requestedType || queryRef.current.trim() !== requestedQuery.trim() || actorRef.current !== actorID) return;
      setPage({ ...result, type: requestedType, actorID, query: requestedQuery.trim() });
      setNextOffset(result.offset + result.items.length);
    } catch (caught: unknown) {
      if (generation.current === request && typeRef.current === requestedType && queryRef.current.trim() === requestedQuery.trim() && actorRef.current === actorID)
        setError(`${caught instanceof Error ? caught.message : `Could not load your ${requestedType} Library.`}${requestedQuery.trim() ? ` Search: “${requestedQuery.trim()}”.` : ""}`);
    } finally {
      if (generation.current === request && typeRef.current === requestedType && queryRef.current.trim() === requestedQuery.trim() && actorRef.current === actorID) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (state.status !== "authenticated") {
      generation.current += 1;
      setPage(null);
      setNextOffset(0);
      setLoading(false);
      setError("");
      return;
    }
    void loadFirstPage(type, authenticatedActorID, type === "recipes" ? query.trim() : "");
    return () => { generation.current += 1; };
  }, [authenticatedActorID, loadFirstPage, state.status, type, query]);

  useEffect(() => {
    if (state.status !== "authenticated") { setHasSavedCourses(false); setCoursesEnabled(false); return; }
    const actorID = authenticatedActorID;
    let active = true;
    const request = ++probeGeneration.current;
    fetch("/api/public/config", { cache: "no-store" })
      .then(async (response) => response.ok ? response.json() as Promise<{ courses_enabled?: boolean }> : { courses_enabled: false })
      .then(async (config) => {
        if (!active || probeGeneration.current !== request || actorRef.current !== actorID) return;
        setCoursesEnabled(config.courses_enabled === true);
        if (config.courses_enabled === true) { setHasSavedCourses(false); return; }
        const probe = await fetchLibraryCourses(0, 1);
        if (active && probeGeneration.current === request && actorRef.current === actorID) setHasSavedCourses(probe.total > 0);
      })
      .catch(() => { if (active && probeGeneration.current === request && actorRef.current === actorID) { setCoursesEnabled(false); setHasSavedCourses(false); } });
    return () => { active = false; probeGeneration.current += 1; };
  }, [authenticatedActorID, state.status]);

  function selectType(nextType: LibraryType): void {
    if (typeRef.current === nextType) return;
    typeRef.current = nextType;
    const params = new URLSearchParams(window.location.search);
    params.set("type", nextType);
    if (nextType !== "recipes") params.delete("q");
    else if (query.trim()) params.set("q", query.trim());
    window.history.pushState(null, "", `${window.location.pathname}?${params}`);
    setType(nextType);
    const nextQuery = nextType === "recipes" ? query : "";
    queryRef.current = nextQuery;
    setQuery(nextQuery);
  }

  function updateQuery(nextQuery: string): void {
    const params = new URLSearchParams(window.location.search);
    params.set("type", "recipes");
    if (nextQuery.trim()) params.set("q", nextQuery.trim());
    else params.delete("q");
    window.history.replaceState(null, "", `${window.location.pathname}?${params}`);
    queryRef.current = nextQuery;
    setQuery(nextQuery);
  }

  async function loadMore(): Promise<void> {
    const currentPage = page;
    const currentQuery = type === "recipes" ? queryRef.current.trim() : "";
    if (!currentPage || currentPage.type !== type || currentPage.actorID !== actorRef.current || currentPage.query !== currentQuery || loading || loadingMoreRef.current || nextOffset >= currentPage.total) return;
    const request = generation.current;
    const requestedType = type;
    const requestedActor = currentPage.actorID;
    const requestedOffset = nextOffset;
    loadingMoreRef.current = true;
    setLoadingMore(true);
    setMoreError("");
    try {
      const result = requestedType === "courses"
        ? await fetchLibraryCourses(requestedOffset)
        : requestedType === "recipes"
          ? await fetchLibraryPage("recipes", requestedOffset, currentPage.query)
          : await fetchLibraryPage("cookbooks", requestedOffset);
      if (generation.current !== request || typeRef.current !== requestedType || (requestedType === "recipes" && queryRef.current.trim() !== currentPage.query) || actorRef.current !== requestedActor) return;
      setPage((current) => {
        if (!current || current.type !== requestedType) return current;
        const known = new Set(current.items.map(libraryIdentity));
        return { ...current, items: [...current.items, ...result.items.filter((item) => !known.has(libraryIdentity(item)))], total: result.total, limit: result.limit, offset: result.offset };
      });
      setNextOffset(result.offset + result.items.length);
    } catch (caught: unknown) {
      if (generation.current === request && typeRef.current === requestedType && (requestedType !== "recipes" || queryRef.current.trim() === currentPage.query) && actorRef.current === requestedActor)
        setMoreError(`${caught instanceof Error ? caught.message : `Could not load more ${requestedType}.`}${requestedType === "recipes" && currentPage.query ? ` Search: “${currentPage.query}”.` : ""}`);
    } finally {
      if (generation.current === request && typeRef.current === requestedType && (requestedType !== "recipes" || queryRef.current.trim() === currentPage.query) && actorRef.current === requestedActor) {
        loadingMoreRef.current = false;
        setLoadingMore(false);
      }
    }
  }

  async function removeUnavailable(bookmarkID: string): Promise<void> {
    const request = ++generation.current;
    const selectedType = type;
    const selectedActor = currentUser?.id ?? "";
    setBusyBookmarkID(bookmarkID);
    setNotice(null);
    setMutationError("");
    try {
      if (selectedType === "recipes") await removeRecipeBookmark(bookmarkID);
      else if (selectedType === "cookbooks") await removeCookbookBookmark(bookmarkID);
      else await removeCourseBookmark(bookmarkID);
      if (generation.current !== request || typeRef.current !== selectedType || actorRef.current !== selectedActor) return;
      noticeRevision.current += 1;
      setNotice({ actorID: selectedActor, message: "Saved reference removed." });
      await loadFirstPage(selectedType, selectedActor, selectedType === "recipes" ? queryRef.current.trim() : "", true);
    } catch {
      if (generation.current !== request || typeRef.current !== selectedType || actorRef.current !== selectedActor) return;
      setMutationError("Could not remove that saved reference. The current Library was reloaded.");
      await loadFirstPage(selectedType, selectedActor, selectedType === "recipes" ? queryRef.current.trim() : "", true);
    } finally {
      if (typeRef.current === selectedType && actorRef.current === selectedActor) setBusyBookmarkID("");
    }
  }

  const activeQuery = type === "recipes" ? query.trim() : "";
  const activePage = page?.type === type && page.actorID === currentUser?.id && page.query === activeQuery ? page : null;
  const hasMore = Boolean(activePage && nextOffset < activePage.total);
  const showCourses = coursesEnabled || hasSavedCourses || type === "courses";

  return (
    <div className="app-content">
      <AppNavigation currentUser={currentUser} pathname="/library" sessionStatus={state.status} onRetrySession={() => void refreshSession()} />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div><span className="eyebrow">Your kitchen</span><h1>My Library</h1><p>Your personal collection includes things you own, can access through a cookbook, or bookmark.</p></div>
        </section>
        {state.status === "loading" ? <p className="cookbook-dashboard" role="status">Checking your session…</p> :
          state.status === "error" ? <SessionCheckError message={state.message} onRetry={() => void refreshSession()} /> :
            state.status === "anonymous" ? <SignInRequired /> :
              <>
                <div className="library-type-controls">
                  <ViewSwitcher label="Library type" value={type} options={libraryTypeOptions.filter((option) => option.value !== "courses" || showCourses)} onChange={selectType} />
                  {type !== "courses" && <a className="text-link" href={`/create?type=${type}`}>Create {type === "recipes" ? "recipe" : "cookbook"}</a>}
                </div>
                {type === "recipes" && <div className="library-search"><label htmlFor="library-recipe-search">Search your recipes</label><input id="library-recipe-search" type="search" value={query} onChange={(event) => updateQuery(event.currentTarget.value)} placeholder="Title, description, or tag" autoComplete="off" /></div>}
                {notice?.actorID === currentUser.id && <Toast actorID={currentUser.id} message={notice.message} revision={noticeRevision.current} onDismiss={() => setNotice(null)} />}
                {mutationError && <p role="alert" className="form-message">{mutationError}</p>}
                {loading && !activePage && <p role="status">Loading your {type} Library{activeQuery ? ` for “${activeQuery}”` : ""}…</p>}
                {error && <div><p role="alert" className="form-message">{error}</p><button type="button" className="text-link" onClick={() => { if (currentUser) void loadFirstPage(type, currentUser.id, activeQuery); }}>Retry loading {type}{activeQuery ? ` for “${activeQuery}”` : ""}</button></div>}
                {activePage && currentUser && <>{type === "recipes" && activeQuery && activePage.items.length === 0 && <p className="cookbook-empty">No recipes match “{activeQuery}” in your Library.</p>}<LibraryCollection type={type} page={activePage} userID={currentUser.id} busyBookmarkID={busyBookmarkID} onBookmarkChange={() => { setMutationError(""); noticeRevision.current += 1; setNotice({ actorID: currentUser.id, message: "Bookmark updated." }); void loadFirstPage(type, currentUser.id, activeQuery, true); }} onMutationUncertain={() => void loadFirstPage(type, currentUser.id, activeQuery, true)} onRemoveUnavailable={(id) => void removeUnavailable(id)} />{type === "recipes" && activeQuery && activePage.items.length === 0 && <p className="cookbook-empty">Unavailable saved references cannot be searched because their source content is unavailable.</p>}</>}
                {moreError && <div><p role="alert" className="form-message">{moreError}</p><button type="button" className="text-link" disabled={loadingMore} onClick={() => void loadMore()}>Retry loading more</button></div>}
                {hasMore && !loading && <button className="button button--green" type="button" onClick={() => void loadMore()} disabled={loadingMore}>{loadingMore ? "Loading…" : "Load more"}</button>}
                {loadingMore && <p role="status">Loading more {type}…</p>}
              </>}
      </main>
    </div>
  );
}

function libraryIdentity(item: LibraryItem): string {
  if (item.availability === "unavailable") return `bookmark:${item.bookmark_id ?? ""}`;
  if ("course" in item) return `course:${item.course.id}`;
  if ("target_kind" in item && item.target_kind === "remote") return `remote:${item.remote_recipe?.object_uri ?? ""}`;
  if ("target_kind" in item) return `local:${item.recipe?.id ?? ""}`;
  return `cookbook:${item.cookbook?.id ?? ""}`;
}

export default function LibraryRoot() {
  return <AppSessionProvider><LibraryPageView /></AppSessionProvider>;
}
