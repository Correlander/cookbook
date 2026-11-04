import { useEffect, useRef, useState } from "react";
import type {
  CreatorCookbookItem,
  CreatorCourseItem,
  CreatorProfile,
  CreatorProfileList,
  CreatorRecipeItem,
} from "../../apiModels";
import {
  CREATOR_PROFILE_PAGE_SIZE,
  CreatorProfileApiError,
  getCreatorProfile,
  listCreatorCookbooks,
  listCreatorCourses,
  listCreatorRecipes,
} from "./creatorProfileApi.ts";
import usePublicAuthConfig from "../auth/usePublicAuthConfig.ts";

interface CollectionPage<T> {
  items: T[];
  offset: number;
  hasMore: boolean;
  loadingMore: boolean;
  error: string;
}

function emptyPage<T>(): CollectionPage<T> {
  return {
    items: [],
    offset: 0,
    hasMore: false,
    loadingMore: false,
    error: "",
  };
}

function asPage<T>(page: CreatorProfileList<T>): CollectionPage<T> {
  return {
    items: page.items,
    offset: page.offset + page.items.length,
    hasMore: page.items.length === page.limit,
    loadingMore: false,
    error: "",
  };
}

function roleLabel(role: string, target: "recipe" | "cookbook" | "course") {
  if (role === "creator" && target === "cookbook") return "Cookbook creator";
  if (role === "creator" && target === "course") return "Course creator";
  if (role === "creator") return "Creator";
  if (role === "adapter") return "Adapter";
  if (role === "contributor") return "Contributor";
  return `Other role: ${role}`;
}

function provenanceLabel(provenance: string) {
  if (provenance === "self_asserted_at_creation")
    return "self-asserted when created";
  if (provenance === "local_record_creation")
    return "recorded when cookbook was created";
  if (provenance === "adapter_source")
    return "adaptation with source preserved";
  if (provenance === "external_claim") return "unverified external claim";
  return `provenance: ${provenance}`;
}

export default function CreatorProfileView({
  profileID,
  actorID,
  onSessionExpired,
}: {
  profileID: string;
  actorID: string;
  onSessionExpired: () => void;
}) {
  const [profile, setProfile] = useState<CreatorProfile | null>(null);
  const [recipes, setRecipes] =
    useState<CollectionPage<CreatorRecipeItem>>(emptyPage);
  const [cookbooks, setCookbooks] =
    useState<CollectionPage<CreatorCookbookItem>>(emptyPage);
  const [courses, setCourses] =
    useState<CollectionPage<CreatorCourseItem>>(emptyPage);
  const [courseLoadState, setCourseLoadState] = useState<
    "not-started" | "pending" | "complete"
  >("not-started");
  const [state, setState] = useState<
    "loading" | "ready" | "unavailable" | "session-error"
  >("loading");
  const [retry, setRetry] = useState(0);
  const requestGeneration = useRef(0);
  const courseRequestGeneration = useRef(0);
  const { state: publicConfig } = usePublicAuthConfig();
  const coursesEnabled =
    publicConfig.status === "ready" && publicConfig.config.courses_enabled;

  useEffect(() => {
    let active = true;
    const generation = ++requestGeneration.current;
    setState("loading");
    setProfile(null);
    setRecipes(emptyPage());
    setCookbooks(emptyPage());
    setCourses(emptyPage());
    setCourseLoadState("not-started");
    if (!profileID) {
      setState("unavailable");
      return () => {
        active = false;
      };
    }
    void (async () => {
      try {
        const data = await getCreatorProfile(profileID);
        if (!active) return;
        setProfile(data.profile);
        const [recipeResult, cookbookResult] = await Promise.allSettled([
          listCreatorRecipes(profileID),
          listCreatorCookbooks(profileID),
        ]);
        if (!active || requestGeneration.current !== generation) return;
        const unavailable = [recipeResult, cookbookResult].some(
          (result) =>
            result.status === "rejected" &&
            result.reason instanceof CreatorProfileApiError &&
            result.reason.status === 404,
        );
        if (unavailable) {
          setState("unavailable");
          setProfile(null);
          return;
        }
        const unauthorized = [recipeResult, cookbookResult].some(
          (result) =>
            result.status === "rejected" &&
            result.reason instanceof CreatorProfileApiError &&
            result.reason.status === 401,
        );
        if (unauthorized) {
          setState("session-error");
          setProfile(null);
          return;
        }
        if (recipeResult.status === "fulfilled") {
          setRecipes(asPage(recipeResult.value));
        } else {
          setRecipes({
            ...emptyPage<CreatorRecipeItem>(),
            error: "Could not load attributed recipes. Retry this list.",
          });
        }
        if (cookbookResult.status === "fulfilled") {
          setCookbooks(asPage(cookbookResult.value));
        } else {
          setCookbooks({
            ...emptyPage<CreatorCookbookItem>(),
            error: "Could not load attributed cookbooks. Retry this list.",
          });
        }
        setState("ready");
      } catch (cause) {
        if (!active || requestGeneration.current !== generation) return;
        if (cause instanceof CreatorProfileApiError && cause.status === 401) {
          setState("session-error");
        } else {
          setState("unavailable");
        }
      }
    })();
    return () => {
      active = false;
    };
  }, [profileID, actorID, retry]);

  useEffect(() => {
    let active = true;
    const generation = ++courseRequestGeneration.current;
    setCourses(emptyPage());
    setCourseLoadState("not-started");
    if (!coursesEnabled || !profile?.id) {
      return () => {
        active = false;
        courseRequestGeneration.current += 1;
      };
    }
    setCourseLoadState("pending");
    void listCreatorCourses(profile.id)
      .then((result) => {
        if (!active || courseRequestGeneration.current !== generation) return;
        setCourses(asPage(result));
        setCourseLoadState("complete");
      })
      .catch(() => {
        if (!active || courseRequestGeneration.current !== generation) return;
        setCourses({
          ...emptyPage<CreatorCourseItem>(),
          error: "Could not load attributed courses. Retry this list.",
        });
        setCourseLoadState("complete");
      });
    return () => {
      active = false;
      courseRequestGeneration.current += 1;
    };
  }, [profile?.id, actorID, coursesEnabled, retry]);

  async function loadMore(kind: "recipes" | "cookbooks" | "courses") {
    const page =
      kind === "recipes" ? recipes : kind === "cookbooks" ? cookbooks : courses;
    if ((!page.hasMore && !page.error) || page.loadingMore) return;
    const offset = page.error && page.items.length === 0 ? 0 : page.offset;
    const generation =
      kind === "courses"
        ? courseRequestGeneration.current
        : requestGeneration.current;
    if (kind === "recipes") {
      setRecipes((current) => ({ ...current, loadingMore: true, error: "" }));
    } else if (kind === "cookbooks") {
      setCookbooks((current) => ({ ...current, loadingMore: true, error: "" }));
    } else {
      setCourses((current) => ({ ...current, loadingMore: true, error: "" }));
      if (offset === 0) setCourseLoadState("pending");
    }
    try {
      if (kind === "recipes") {
        const result = await listCreatorRecipes(profileID, offset);
        if (generation !== requestGeneration.current) return;
        if (result.offset !== offset) {
          throw new CreatorProfileApiError(
            "The creator list page did not match the requested offset.",
            502,
            "invalid_pagination_response",
          );
        }
        setRecipes((current) => ({
          ...current,
          items: [...current.items, ...result.items],
          offset: result.offset + result.items.length,
          hasMore: result.items.length === result.limit,
          loadingMore: false,
          error: "",
        }));
      } else if (kind === "cookbooks") {
        const result = await listCreatorCookbooks(profileID, offset);
        if (generation !== requestGeneration.current) return;
        if (result.offset !== offset) {
          throw new CreatorProfileApiError(
            "The creator list page did not match the requested offset.",
            502,
            "invalid_pagination_response",
          );
        }
        setCookbooks((current) => ({
          ...current,
          items: [...current.items, ...result.items],
          offset: result.offset + result.items.length,
          hasMore: result.items.length === result.limit,
          loadingMore: false,
          error: "",
        }));
      } else {
        const result = await listCreatorCourses(profileID, offset);
        if (generation !== courseRequestGeneration.current) return;
        if (result.offset !== offset) {
          throw new CreatorProfileApiError(
            "The creator list page did not match the requested offset.",
            502,
            "invalid_pagination_response",
          );
        }
        setCourses((current) => ({
          ...current,
          items: [...current.items, ...result.items],
          offset: result.offset + result.items.length,
          hasMore: result.items.length === result.limit,
          loadingMore: false,
          error: "",
        }));
        if (offset === 0) setCourseLoadState("complete");
      }
    } catch (cause) {
      if (
        generation !==
        (kind === "courses"
          ? courseRequestGeneration.current
          : requestGeneration.current)
      )
        return;
      if (
        kind !== "courses" &&
        cause instanceof CreatorProfileApiError &&
        cause.status === 401
      ) {
        setProfile(null);
        setState("session-error");
        return;
      }
      if (
        kind !== "courses" &&
        cause instanceof CreatorProfileApiError &&
        cause.status === 404
      ) {
        setProfile(null);
        setState("unavailable");
        return;
      }
      if (kind === "recipes") {
        setRecipes((current) => ({
          ...current,
          loadingMore: false,
          error: "Could not load more recipes. Retry this list.",
        }));
      } else if (kind === "cookbooks") {
        setCookbooks((current) => ({
          ...current,
          loadingMore: false,
          error: "Could not load more cookbooks. Retry this list.",
        }));
      } else {
        setCourses((current) => ({
          ...current,
          loadingMore: false,
          error: "Could not load more courses. Retry this list.",
        }));
        if (offset === 0) setCourseLoadState("complete");
      }
    }
  }

  if (state === "loading")
    return (
      <p className="cookbook-dashboard" role="status">
        Loading creator profile…
      </p>
    );
  if (state === "session-error")
    return (
      <section className="cookbook-dashboard">
        <h2>Session check needed</h2>
        <p>
          Your session could not read this profile. Retry the session check
          before continuing.
        </p>
        <button type="button" onClick={onSessionExpired}>
          Retry session
        </button>
      </section>
    );
  if (state === "unavailable" || !profile)
    return (
      <section
        className="cookbook-dashboard"
        aria-labelledby="creator-unavailable-title"
      >
        <h2 id="creator-unavailable-title">Creator profile unavailable</h2>
        <p>This profile could not be found or is not available to you.</p>
        <button type="button" onClick={() => setRetry((value) => value + 1)}>
          Retry profile
        </button>
      </section>
    );

  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="creator-profile-title"
    >
      <span className="eyebrow">
        {profile.visibility === "instance"
          ? "Same-instance profile · signed-in viewers"
          : "Private profile preview"}
      </span>
      <h2 id="creator-profile-title">
        {profile.public_name || "Unnamed creator"}
      </h2>
      {profile.bio && <p>{profile.bio}</p>}
      {profile.visibility === "private" ? (
        <p className="dialog-note">
          This is your private profile preview. Only you can view it.
        </p>
      ) : (
        <p className="dialog-note">
          Share this same-instance link with signed-in people on this Cookbook
          instance:{" "}
          <a
            href={`/creator?id=${encodeURIComponent(profile.id)}`}
          >{`/creator?id=${encodeURIComponent(profile.id)}`}</a>
          . Recipes and cookbooks keep their own access rules.
        </p>
      )}
      <p className="dialog-note">
        Profile visibility does not grant access to recipes, cookbooks, or
        courses. Each item follows its own access rules; course ownership is
        separate from profile attribution.
      </p>
      <section aria-labelledby="creator-recipes-title">
        <h3 id="creator-recipes-title">Recipes</h3>
        {recipes.error ? (
          <p role="alert" className="form-message">
            {recipes.error}
          </p>
        ) : recipes.items.length ? (
          <ul>
            {recipes.items.map(({ recipe, role, provenance }) => (
              <li key={`${recipe.id}:${role}`}>
                <a href={`/recipe?id=${encodeURIComponent(recipe.id)}`}>
                  {recipe.title}
                </a>
                <span>
                  {" "}
                  —{" "}
                  <span className="creator-attribution__role">
                    {roleLabel(role, "recipe")}
                  </span>
                  <span className="creator-attribution__provenance">
                    {" "}
                    · {provenanceLabel(provenance)}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p>No readable attributed recipes.</p>
        )}
        {(recipes.hasMore || recipes.error) && (
          <button
            type="button"
            disabled={recipes.loadingMore}
            onClick={() => void loadMore("recipes")}
          >
            {recipes.loadingMore
              ? "Loading more recipes…"
              : recipes.error
                ? "Retry loading recipes"
                : "Load more recipes"}
          </button>
        )}
      </section>
      <section aria-labelledby="creator-cookbooks-title">
        <h3 id="creator-cookbooks-title">Cookbooks</h3>
        {cookbooks.error ? (
          <p role="alert" className="form-message">
            {cookbooks.error}
          </p>
        ) : cookbooks.items.length ? (
          <ul>
            {cookbooks.items.map(({ cookbook, role, provenance }) => (
              <li key={`${cookbook.id}:${role}`}>
                <a href={`/cookbook?id=${encodeURIComponent(cookbook.id)}`}>
                  {cookbook.title}
                </a>
                <span>
                  {" "}
                  —{" "}
                  <span className="creator-attribution__role">
                    {roleLabel(role, "cookbook")}
                  </span>
                  <span className="creator-attribution__provenance">
                    {" "}
                    · {provenanceLabel(provenance)}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p>No readable attributed cookbooks.</p>
        )}
        {(cookbooks.hasMore || cookbooks.error) && (
          <button
            type="button"
            disabled={cookbooks.loadingMore}
            onClick={() => void loadMore("cookbooks")}
          >
            {cookbooks.loadingMore
              ? "Loading more cookbooks…"
              : cookbooks.error
                ? "Retry loading cookbooks"
                : "Load more cookbooks"}
          </button>
        )}
      </section>
      {coursesEnabled && (
        <section aria-labelledby="creator-courses-title">
          <h3 id="creator-courses-title">Courses</h3>
          <p className="dialog-note">
            Courses appear here only when they explicitly credit this profile
            and remain readable to you. Attribution does not show course
            ownership or grant editing access.
          </p>
          {courseLoadState !== "complete" && courses.items.length === 0 ? (
            <p role="status">Loading attributed courses…</p>
          ) : courses.error ? (
            <p role="alert" className="form-message">
              {courses.error}
            </p>
          ) : courses.items.length ? (
            <ul>
              {courses.items.map(({ course, role, provenance }) => (
                <li key={`${course.id}:${role}`}>
                  <a href={`/course?id=${encodeURIComponent(course.id)}`}>
                    {course.title}
                  </a>
                  <span>
                    {" "}
                    —{" "}
                    <span className="creator-attribution__role">
                      {roleLabel(role, "course")}
                    </span>
                    <span className="creator-attribution__provenance">
                      {" "}
                      · {provenanceLabel(provenance)}
                    </span>
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p>No readable attributed courses.</p>
          )}
          {(courses.hasMore || courses.error) && (
            <button
              type="button"
              disabled={courses.loadingMore}
              onClick={() => void loadMore("courses")}
            >
              {courses.loadingMore
                ? "Loading more courses…"
                : courses.error
                  ? "Retry loading courses"
                  : "Load more courses"}
            </button>
          )}
        </section>
      )}
      <span className="sr-only">
        Each list loads up to {CREATOR_PROFILE_PAGE_SIZE} items at a time.
      </span>
    </section>
  );
}
