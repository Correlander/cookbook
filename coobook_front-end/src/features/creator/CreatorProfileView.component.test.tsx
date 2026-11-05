import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CreatorProfileView from "./CreatorProfileView.tsx";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });
const profile = {
  id: "p1",
  public_name: "Baker",
  bio: "Local profile",
  visibility: "instance",
  created_at: "",
  updated_at: "",
};
const courseItem = {
  course: { id: "c/1", title: "Bread basics" },
  role: "creator",
  provenance: "self_asserted_at_creation",
};
const publicConfig = (courses_enabled: boolean) => ({
  auth_methods: { email_link: false, passkey: true },
  registration_policy: "disabled",
  public_instance: true,
  courses_enabled,
});

it("shows only typed readable resource projections and keeps role/provenance distinct", async () => {
  const recipe = { id: "r1", title: "Soup" };
  const cookbook = {
    id: "b1",
    title: "Family book",
    description: "",
    visibility: "private",
    created_at: "",
    updated_at: "",
  };
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path.endsWith("/recipes?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile,
          items: [
            {
              recipe,
              role: "creator",
              provenance: "self_asserted_at_creation",
            },
          ],
          limit: 24,
          offset: 0,
        }),
      );
    if (path.endsWith("/cookbooks?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile,
          items: [
            { cookbook, role: "creator", provenance: "local_record_creation" },
          ],
          limit: 24,
          offset: 0,
        }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(await screen.findByRole("heading", { name: "Baker" })).toBeTruthy();
  expect(
    screen.getByText("Creator", { selector: ".creator-attribution__role" }),
  ).toBeTruthy();
  expect(
    document.querySelector(".creator-attribution__provenance")?.textContent,
  ).toContain("self-asserted when created");
  expect(
    screen.getByText("Cookbook creator", {
      selector: ".creator-attribution__role",
    }),
  ).toBeTruthy();
  expect(
    Array.from(document.querySelectorAll(".creator-attribution__provenance"))
      .map((node) => node.textContent)
      .join(" "),
  ).toContain("recorded when cookbook was created");
  expect(screen.getByRole("link", { name: "/creator?id=p1" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Soup" }).getAttribute("href")).toBe(
    "/recipe?id=r1",
  );
  expect(
    screen.getByRole("link", { name: "Family book" }).getAttribute("href"),
  ).toBe("/cookbook?id=b1");
  expect(
    fetchMock.mock.calls.some(
      ([url]) =>
        (String(url).includes("/api/public/") &&
          String(url) !== "/api/public/config") ||
        String(url) === "/api/recipes?limit=24&offset=0",
    ),
  ).toBe(false);
});

it("asks for a session retry after 401 without falling back to public reads", async () => {
  const onSessionExpired = vi.fn();
  const fetchMock = vi
    .fn()
    .mockResolvedValue(response({ error: "unauthorized" }, 401));
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={onSessionExpired}
    />,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Retry session" }));
  expect(onSessionExpired).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(
    fetchMock.mock.calls.some(
      ([url]) =>
        (String(url).includes("/api/public/") &&
          String(url) !== "/api/public/config") ||
        String(url) === "/api/recipes?limit=24&offset=0",
    ),
  ).toBe(false);
});

it("keeps a private owner preview free of a share link", async () => {
  const privateProfile = { ...profile, visibility: "private" };
  vi.stubGlobal(
    "fetch",
    vi.fn((url: RequestInfo | URL) => {
      const path = String(url);
      if (path.endsWith("/recipes?limit=24&offset=0"))
        return Promise.resolve(
          response({
            profile: privateProfile,
            items: [],
            limit: 24,
            offset: 0,
          }),
        );
      if (path.endsWith("/cookbooks?limit=24&offset=0"))
        return Promise.resolve(
          response({
            profile: privateProfile,
            items: [],
            limit: 24,
            offset: 0,
          }),
        );
      return Promise.resolve(response({ profile: privateProfile }));
    }),
  );
  render(
    <CreatorProfileView
      actorID="owner"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(await screen.findByText("Private profile preview")).toBeTruthy();
  expect(
    screen.getByText(
      "This is your private profile preview. Only you can view it.",
    ),
  ).toBeTruthy();
  expect(screen.queryByRole("link", { name: /creator\?id=/ })).toBeNull();
});

it("renders the same unavailable state for inaccessible and unknown profiles without fallback", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(response({ error: "not_found" }, 404));
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="unknown"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(
    await screen.findByRole("heading", { name: "Creator profile unavailable" }),
  ).toBeTruthy();
  expect(
    screen.getByText(
      "This profile could not be found or is not available to you.",
    ),
  ).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(
    fetchMock.mock.calls.some(
      ([url]) =>
        (String(url).includes("/api/public/") &&
          String(url) !== "/api/public/config") ||
        String(url).includes("/api/recipes"),
    ),
  ).toBe(false);
});

it("does not request a profile when the route has no id", async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView actorID="u1" profileID="" onSessionExpired={vi.fn()} />,
  );
  expect(
    await screen.findByRole("heading", { name: "Creator profile unavailable" }),
  ).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledWith("/api/public/config", { cache: "no-store" });
  expect(
    fetchMock.mock.calls.some(([url]) => String(url).includes("/courses?")),
  ).toBe(false);
});

it("ignores a slower response for a previous profile after route changes", async () => {
  let resolveSlow!: (value: Response) => void;
  const slow = new Promise<Response>((resolve) => {
    resolveSlow = resolve;
  });
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path.endsWith("/slow")) return slow;
    if (path.endsWith("/fast"))
      return Promise.resolve(
        response({ profile: { ...profile, id: "fast", public_name: "Fast" } }),
      );
    if (path.includes("/fast/recipes"))
      return Promise.resolve(
        response({
          profile: { ...profile, id: "fast", public_name: "Fast" },
          items: [],
          limit: 24,
          offset: 0,
        }),
      );
    if (path.includes("/fast/cookbooks"))
      return Promise.resolve(
        response({
          profile: { ...profile, id: "fast", public_name: "Fast" },
          items: [],
          limit: 24,
          offset: 0,
        }),
      );
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { rerender } = render(
    <CreatorProfileView
      actorID="u1"
      profileID="slow"
      onSessionExpired={vi.fn()}
    />,
  );
  rerender(
    <CreatorProfileView
      actorID="u1"
      profileID="fast"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(await screen.findByRole("heading", { name: "Fast" })).toBeTruthy();
  resolveSlow(
    response({ profile: { ...profile, id: "slow", public_name: "Slow" } }),
  );
  await Promise.resolve();
  expect(screen.queryByRole("heading", { name: "Slow" })).toBeNull();
});

it("continues recipes and cookbooks independently from their 24-item boundaries", async () => {
  const recipeItems = Array.from({ length: 24 }, (_, index) => ({
    recipe: { id: `r${index}`, title: `Soup ${index}` },
    role: "creator",
    provenance: "self_asserted_at_creation",
  }));
  const cookbookItems = Array.from({ length: 24 }, (_, index) => ({
    cookbook: {
      id: `b${index}`,
      title: `Book ${index}`,
      description: "",
      visibility: "private",
      created_at: "",
      updated_at: "",
    },
    role: "creator",
    provenance: "local_record_creation",
  }));
  const recipePage = (items: typeof recipeItems, offset: number) =>
    response({ profile, items, limit: 24, offset });
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path.endsWith("/recipes?limit=24&offset=0"))
      return Promise.resolve(recipePage(recipeItems, 0));
    if (path.endsWith("/cookbooks?limit=24&offset=0"))
      return Promise.resolve(
        response({ profile, items: cookbookItems, limit: 24, offset: 0 }),
      );
    if (path.endsWith("/recipes?limit=24&offset=24"))
      return Promise.resolve(
        recipePage(
          Array.from({ length: 24 }, (_, index) => ({
            recipe: { id: `r${index + 24}`, title: `Soup ${index + 24}` },
            role: "creator",
            provenance: "self_asserted_at_creation",
          })),
          24,
        ),
      );
    if (path.endsWith("/cookbooks?limit=24&offset=24"))
      return Promise.resolve(
        response({
          profile,
          items: [
            {
              cookbook: {
                id: "b24",
                title: "Book 24",
                description: "",
                visibility: "private",
                created_at: "",
                updated_at: "",
              },
              role: "creator",
              provenance: "local_record_creation",
            },
          ],
          limit: 24,
          offset: 24,
        }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );

  const loadRecipes = await screen.findByRole("button", {
    name: "Load more recipes",
  });
  expect(
    screen.getByRole("button", { name: "Load more cookbooks" }),
  ).toBeTruthy();
  fireEvent.click(loadRecipes);
  expect(await screen.findByRole("link", { name: "Soup 24" })).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/recipes?limit=24&offset=24"),
    ),
  ).toHaveLength(1);
  expect(
    screen.getByRole("button", { name: "Load more recipes" }),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Load more cookbooks" }));
  expect(await screen.findByRole("link", { name: "Book 24" })).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/cookbooks?limit=24&offset=24"),
    ),
  ).toHaveLength(1);
  expect(
    screen.getByRole("button", { name: "Load more recipes" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Load more cookbooks" }),
  ).toBeNull();
});

it("disables a pending list request and retries its failed page at the same offset", async () => {
  let rejectNext!: (value: Response) => void;
  let resolveRetry!: (value: Response) => void;
  const failedPage = new Promise<Response>((resolve) => {
    rejectNext = resolve;
  });
  const retriedPage = new Promise<Response>((resolve) => {
    resolveRetry = resolve;
  });
  let nextPageCalls = 0;
  const firstPage = Array.from({ length: 24 }, (_, index) => ({
    recipe: { id: `r${index}`, title: `Soup ${index}` },
    role: "creator",
    provenance: "self_asserted_at_creation",
  }));
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path.endsWith("/recipes?limit=24&offset=0"))
      return Promise.resolve(
        response({ profile, items: firstPage, limit: 24, offset: 0 }),
      );
    if (path.endsWith("/cookbooks?limit=24&offset=0"))
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    if (path.endsWith("/recipes?limit=24&offset=24")) {
      nextPageCalls += 1;
      return nextPageCalls === 1 ? failedPage : retriedPage;
    }
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );

  fireEvent.click(
    await screen.findByRole("button", { name: "Load more recipes" }),
  );
  const pendingButton = await screen.findByRole("button", {
    name: "Loading more recipes…",
  });
  expect(pendingButton.hasAttribute("disabled")).toBe(true);
  await act(async () => {
    rejectNext(response({ error: "temporarily unavailable" }, 503));
    await failedPage;
  });
  expect(await screen.findByRole("alert")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Retry loading recipes" }),
  );
  await waitFor(() => expect(nextPageCalls).toBe(2));
  await act(async () => {
    resolveRetry(
      response({
        profile,
        items: [
          {
            recipe: { id: "r24", title: "Soup 24" },
            role: "creator",
            provenance: "self_asserted_at_creation",
          },
        ],
        limit: 24,
        offset: 24,
      }),
    );
    await retriedPage;
  });
  expect(await screen.findByRole("link", { name: "Soup 24" })).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/recipes?limit=24&offset=24"),
    ),
  ).toHaveLength(2);
});

it("discards a prior actor response when the same profile route changes actor", async () => {
  let resolveOld!: (value: Response) => void;
  const oldResponse = new Promise<Response>((resolve) => {
    resolveOld = resolve;
  });
  let profileRequests = 0;
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path.endsWith("/p1")) {
      profileRequests += 1;
      return profileRequests === 1
        ? oldResponse
        : Promise.resolve(
            response({ profile: { ...profile, public_name: "Actor two" } }),
          );
    }
    return Promise.resolve(
      response({ profile, items: [], limit: 24, offset: 0 }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  const { rerender } = render(
    <CreatorProfileView
      actorID="actor-1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  rerender(
    <CreatorProfileView
      actorID="actor-2"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(
    await screen.findByRole("heading", { name: "Actor two" }),
  ).toBeTruthy();
  resolveOld(response({ profile: { ...profile, public_name: "Stale actor" } }));
  await Promise.resolve();
  expect(screen.queryByRole("heading", { name: "Stale actor" })).toBeNull();
});

it("shows explicitly attributed readable courses only when the public feature is enabled", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/courses?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile,
          items: [courseItem],
          total: 1,
          limit: 24,
          offset: 0,
        }),
      );
    if (
      path.endsWith("/recipes?limit=24&offset=0") ||
      path.endsWith("/cookbooks?limit=24&offset=0")
    )
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(
    await screen.findByRole("link", { name: "Bread basics" }),
  ).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Courses" })).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "Bread basics" }).getAttribute("href"),
  ).toBe("/course?id=c%2F1");
  expect(
    screen.getByText("Course creator", {
      selector: ".creator-attribution__role",
    }),
  ).toBeTruthy();
  expect(
    document.querySelector(".creator-attribution__provenance")?.textContent,
  ).toContain("self-asserted when created");
  expect(
    screen.getByText(/Attribution does not show course ownership/),
  ).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(([url]) => String(url).includes("/courses?")),
  ).toHaveLength(1);
});

it("shows an empty course collection without treating it as an endpoint error", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/courses?limit=24&offset=0"))
      return Promise.resolve(
        response({ profile, items: [], total: 0, limit: 24, offset: 0 }),
      );
    if (
      path.endsWith("/recipes?limit=24&offset=0") ||
      path.endsWith("/cookbooks?limit=24&offset=0")
    )
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(
    await screen.findByText("No readable attributed courses."),
  ).toBeTruthy();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("shows a pending course request before its empty result arrives", async () => {
  let resolveCourses!: (value: Response) => void;
  const pendingCourses = new Promise<Response>((resolve) => {
    resolveCourses = resolve;
  });
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/courses?limit=24&offset=0")) return pendingCourses;
    if (
      path.endsWith("/recipes?limit=24&offset=0") ||
      path.endsWith("/cookbooks?limit=24&offset=0")
    )
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );

  expect(await screen.findByRole("heading", { name: "Baker" })).toBeTruthy();
  const loadingStatus = await screen.findByText("Loading attributed courses…");
  expect(loadingStatus.getAttribute("role")).toBe("status");
  expect(screen.queryByText("No readable attributed courses.")).toBeNull();
  await act(async () => {
    resolveCourses(
      response({ profile, items: [], total: 0, limit: 24, offset: 0 }),
    );
    await pendingCourses;
  });
  expect(
    await screen.findByText("No readable attributed courses."),
  ).toBeTruthy();
});

it("shows loading while retrying a failed initial course page", async () => {
  let resolveRetry!: (value: Response) => void;
  const pendingRetry = new Promise<Response>((resolve) => {
    resolveRetry = resolve;
  });
  let courseCalls = 0;
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/courses?limit=24&offset=0")) {
      courseCalls += 1;
      return courseCalls === 1
        ? Promise.resolve(response({ error: "temporarily_unavailable" }, 503))
        : pendingRetry;
    }
    if (
      path.endsWith("/recipes?limit=24&offset=0") ||
      path.endsWith("/cookbooks?limit=24&offset=0")
    )
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );

  fireEvent.click(
    await screen.findByRole("button", { name: "Retry loading courses" }),
  );
  const loadingStatus = await screen.findByText("Loading attributed courses…");
  expect(loadingStatus.getAttribute("role")).toBe("status");
  expect(screen.queryByText("No readable attributed courses.")).toBeNull();
  await act(async () => {
    resolveRetry(
      response({ profile, items: [], total: 0, limit: 24, offset: 0 }),
    );
    await pendingRetry;
  });
  expect(
    await screen.findByText("No readable attributed courses."),
  ).toBeTruthy();
  expect(courseCalls).toBe(2);
});

it("fails the course section closed when a response contains uncontracted identity fields", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/courses?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile,
          items: [
            {
              course: { id: "c1", title: "Bread", owner_id: "private-id" },
              role: "creator",
              provenance: "self_asserted_at_creation",
            },
          ],
          total: 1,
          limit: 24,
          offset: 0,
        }),
      );
    if (
      path.endsWith("/recipes?limit=24&offset=0") ||
      path.endsWith("/cookbooks?limit=24&offset=0")
    )
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(
    await screen.findByText(
      "Could not load attributed courses. Retry this list.",
    ),
  ).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Bread" })).toBeNull();
  expect(screen.getByRole("heading", { name: "Baker" })).toBeTruthy();
});

it.each([
  ["disabled", response(publicConfig(false))],
  ["unknown", response({ courses_enabled: "yes" })],
])(
  "does not query or render courses when public feature config is %s",
  async (_label, configResponse) => {
    const fetchMock = vi.fn((url: RequestInfo | URL) => {
      const path = String(url);
      if (path === "/api/public/config") return Promise.resolve(configResponse);
      if (
        path.endsWith("/recipes?limit=24&offset=0") ||
        path.endsWith("/cookbooks?limit=24&offset=0")
      )
        return Promise.resolve(
          response({ profile, items: [], limit: 24, offset: 0 }),
        );
      return Promise.resolve(response({ profile }));
    });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <CreatorProfileView
        actorID="u1"
        profileID="p1"
        onSessionExpired={vi.fn()}
      />,
    );
    expect(await screen.findByRole("heading", { name: "Baker" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Courses" })).toBeNull();
    expect(
      fetchMock.mock.calls.some(([url]) => String(url).includes("/courses?")),
    ).toBe(false);
    expect(screen.getByRole("heading", { name: "Recipes" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Cookbooks" })).toBeTruthy();
  },
);

it("keeps the profile and its other lists available when an older course endpoint returns 404", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.includes("/courses?"))
      return Promise.resolve(response({ error: "not_found" }, 404));
    if (path.endsWith("/recipes?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile,
          items: [
            {
              recipe: { id: "r1", title: "Soup" },
              role: "creator",
              provenance: "self_asserted_at_creation",
            },
          ],
          limit: 24,
          offset: 0,
        }),
      );
    if (path.endsWith("/cookbooks?limit=24&offset=0"))
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(await screen.findByRole("link", { name: "Soup" })).toBeTruthy();
  expect(
    await screen.findByText(
      "Could not load attributed courses. Retry this list.",
    ),
  ).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Baker" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Recipes" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Cookbooks" })).toBeTruthy();
});

it("pages course results with the existing profile page size and offset", async () => {
  const firstPage = Array.from({ length: 24 }, (_, index) => ({
    course: { id: `c${index}`, title: `Course ${index}` },
    role: "creator",
    provenance: "self_asserted_at_creation",
  }));
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/courses?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile,
          items: firstPage,
          total: 25,
          limit: 24,
          offset: 0,
        }),
      );
    if (path.endsWith("/courses?limit=24&offset=24"))
      return Promise.resolve(
        response({
          profile,
          items: [courseItem],
          total: 25,
          limit: 24,
          offset: 24,
        }),
      );
    if (
      path.endsWith("/recipes?limit=24&offset=0") ||
      path.endsWith("/cookbooks?limit=24&offset=0")
    )
      return Promise.resolve(
        response({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(response({ profile }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <CreatorProfileView
      actorID="u1"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Load more courses" }),
  );
  expect(
    await screen.findByRole("link", { name: "Bread basics" }),
  ).toBeTruthy();
  expect(
    fetchMock.mock.calls.some(([url]) =>
      String(url).endsWith("/courses?limit=24&offset=24"),
    ),
  ).toBe(true);
});

it("ignores a late course response after the viewed profile changes", async () => {
  let resolveOld!: (value: Response) => void;
  const oldCourseResult = new Promise<Response>((resolve) => {
    resolveOld = resolve;
  });
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.endsWith("/slow/courses?limit=24&offset=0"))
      return oldCourseResult;
    if (path.endsWith("/fast/courses?limit=24&offset=0"))
      return Promise.resolve(
        response({
          profile: { ...profile, id: "fast" },
          items: [
            {
              ...courseItem,
              course: { id: "fast-course", title: "Fast course" },
            },
          ],
          total: 1,
          limit: 24,
          offset: 0,
        }),
      );
    if (path.endsWith("/slow") || path.endsWith("/fast"))
      return Promise.resolve(
        response({
          profile: { ...profile, id: path.endsWith("/fast") ? "fast" : "slow" },
        }),
      );
    return Promise.resolve(
      response({ profile, items: [], limit: 24, offset: 0 }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  const { rerender } = render(
    <CreatorProfileView
      actorID="u1"
      profileID="slow"
      onSessionExpired={vi.fn()}
    />,
  );
  rerender(
    <CreatorProfileView
      actorID="u1"
      profileID="fast"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(await screen.findByRole("link", { name: "Fast course" })).toBeTruthy();
  resolveOld(
    response({
      profile,
      items: [
        { ...courseItem, course: { id: "slow-course", title: "Stale course" } },
      ],
      total: 1,
      limit: 24,
      offset: 0,
    }),
  );
  await Promise.resolve();
  expect(screen.queryByRole("link", { name: "Stale course" })).toBeNull();
});

it("ignores a late course response after the signed-in actor changes", async () => {
  let resolveOld!: (value: Response) => void;
  const oldCourseResult = new Promise<Response>((resolve) => {
    resolveOld = resolve;
  });
  let profileCalls = 0;
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    const path = String(url);
    if (path === "/api/public/config")
      return Promise.resolve(response(publicConfig(true)));
    if (path.includes("/courses?"))
      return profileCalls === 1
        ? oldCourseResult
        : Promise.resolve(
            response({
              profile,
              items: [
                {
                  ...courseItem,
                  course: { id: "new-actor-course", title: "New actor course" },
                },
              ],
              total: 1,
              limit: 24,
              offset: 0,
            }),
          );
    if (path.endsWith("/p1")) {
      profileCalls += 1;
      return Promise.resolve(response({ profile }));
    }
    return Promise.resolve(
      response({ profile, items: [], limit: 24, offset: 0 }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  const { rerender } = render(
    <CreatorProfileView
      actorID="old-actor"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  rerender(
    <CreatorProfileView
      actorID="new-actor"
      profileID="p1"
      onSessionExpired={vi.fn()}
    />,
  );
  expect(
    await screen.findByRole("link", { name: "New actor course" }),
  ).toBeTruthy();
  resolveOld(
    response({
      profile,
      items: [
        {
          ...courseItem,
          course: { id: "old-actor-course", title: "Old actor course" },
        },
      ],
      total: 1,
      limit: 24,
      offset: 0,
    }),
  );
  await Promise.resolve();
  expect(screen.queryByRole("link", { name: "Old actor course" })).toBeNull();
});
