import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import App from "./App.tsx";
import { AppSessionProvider } from "./app/AppSession.tsx";

function renderApp(
  route: "home" | "account-security" | "admin-settings" = "home",
  pathname = route === "home" ? "/" : `/${route}`,
) {
  return render(
    <AppSessionProvider>
      <App route={route} pathname={pathname} />
    </AppSessionProvider>,
  );
}

afterEach(() => {
  cleanup();
  window.history.replaceState({}, "", "/");
  vi.unstubAllGlobals();
});

function publicConfig(
  emailLink: boolean,
  passkey: boolean,
  registrationPolicy: "invite_only" | "open" | "disabled" = "invite_only",
) {
  return {
    auth_methods: { email_link: emailLink, passkey },
    registration_policy: registrationPolicy,
    public_instance: false,
    courses_enabled: false,
  };
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

it("serves the account route directly and requires a signed-in session", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResponse({}, 401))));

  renderApp("account-security", "/account/security");

  expect(
    await screen.findByRole("heading", { name: "Sign in to continue" }),
  ).toBeTruthy();
  expect(
    screen
      .getAllByRole("link", { name: "Sign in" })
      .every((link) => link.getAttribute("href") === "/?signin=1"),
  ).toBe(true);
  expect(screen.getByRole("main").getAttribute("id")).toBe("home");
});

it("keeps instance settings behind the administrator check", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      if (String(input) === "/api/auth/me")
        return Promise.resolve(
          jsonResponse({
            id: "user-1",
            email: "cook@example.test",
            display_name: "Cook",
            is_admin: false,
            created_at: "2026-01-01T00:00:00Z",
          }),
        );
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp("admin-settings", "/admin/settings");

  expect(
    await screen.findByRole("heading", {
      name: "Administrator access required",
    }),
  ).toBeTruthy();
  expect(fetch).not.toHaveBeenCalledWith("/api/admin/settings");
});

it("hides instance settings when Go rejects an administrator session", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      if (String(input) === "/api/auth/me")
        return Promise.resolve(
          jsonResponse({
            id: "user-1",
            email: "admin@example.test",
            display_name: "Admin",
            is_admin: true,
            created_at: "2026-01-01T00:00:00Z",
          }),
        );
      if (String(input) === "/api/admin/settings")
        return Promise.resolve(jsonResponse({}, 403));
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp("admin-settings", "/admin/settings");

  expect(
    await screen.findByRole("heading", { name: "Instance administration" }),
  ).toBeTruthy();
  await waitFor(() =>
    expect(screen.queryByLabelText("Enable public browsing")).toBeNull(),
  );
});

it("preserves hidden cookbook references when an editor cannot manage sharing", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "another-user",
    cookbook_ids: ["hidden-private-book"],
    creator_name: "Alex",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [{ name: "apples", quantity: "4", notes: "" }],
    steps: [{ text: "Bake it." }],
    servings: 6,
    prep_time_minutes: 10,
    cook_time_minutes: 40,
    tags: [],
    visibility: "private",
    can_edit: true,
    can_delete: false,
    can_manage_sharing: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  let updateBody: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "editor@example.test",
          display_name: "Editor",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes/recipe-1" && init?.method === "PUT") {
      updateBody = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(jsonResponse(recipe));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit recipe" }));
  await screen.findByRole("dialog", { name: "Edit recipe" });
  expect(screen.queryByLabelText("Add to cookbooks")).toBeNull();
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Apple tart" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

  await waitFor(() => expect(updateBody).toBeDefined());
  expect(updateBody).not.toHaveProperty("cookbook_ids");
  expect(fetchMock.mock.calls.map(([input]) => String(input))).not.toContain(
    "/api/cookbooks",
  );
});

it("shows cookbook sharing only when the API grants sharing management", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "user-1",
    creator_name: "Alex",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [{ name: "apples", quantity: "4", notes: "" }],
    steps: [{ text: "Bake it." }],
    servings: 6,
    prep_time_minutes: 10,
    cook_time_minutes: 40,
    tags: [],
    visibility: "private",
    can_edit: true,
    can_delete: true,
    can_manage_sharing: true,
    version: 1,
    cookbook_ids: ["owned-book"],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/auth/me")
        return Promise.resolve(
          jsonResponse({
            id: "user-1",
            email: "owner@example.test",
            display_name: "Owner",
            is_admin: false,
            created_at: "2026-01-01T00:00:00Z",
          }),
        );
      if (url === "/api/recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([recipe]));
      if (url === "/api/public/remote-recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/cookbooks")
        return Promise.resolve(
          jsonResponse([
            {
              id: "owned-book",
              owner_id: "user-1",
              title: "Family favorites",
            },
          ]),
        );
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit recipe" }));
  expect(await screen.findByLabelText("Add to cookbooks")).toBeTruthy();
  expect(
    await screen.findByRole("option", { name: "Family favorites" }),
  ).toBeTruthy();
});

it("loads publication settings only after the canonical owner opens the panel", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "user-1",
    creator_name: "Owner",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [{ name: "apples", quantity: "4", notes: "" }],
    steps: [{ text: "Bake it." }],
    servings: 6,
    prep_time_minutes: 10,
    cook_time_minutes: 40,
    tags: [],
    visibility: "public",
    can_edit: false,
    can_delete: false,
    can_manage_sharing: true,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes/recipe-1/publication")
      return Promise.resolve(jsonResponse({ error: "forbidden" }, 403));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  expect(screen.getByRole("button", { name: "Manage publication" })).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(
      ([input]) => String(input) === "/api/recipes/recipe-1/publication",
    ),
  ).toHaveLength(0);

  fireEvent.click(screen.getByRole("button", { name: "Manage publication" }));
  await screen.findByText(/could not load publication settings/i);
  expect(
    fetchMock.mock.calls.filter(
      ([input]) => String(input) === "/api/recipes/recipe-1/publication",
    ),
  ).toHaveLength(1);
});

it("hides publication controls from an editor who is not the recipe owner", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "another-user",
    creator_name: "Owner",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [],
    steps: [],
    servings: 0,
    prep_time_minutes: 0,
    cook_time_minutes: 0,
    tags: [],
    visibility: "public",
    can_edit: true,
    can_delete: false,
    can_manage_sharing: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "editor@example.test",
          display_name: "Editor",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  expect(screen.queryByRole("button", { name: "Manage publication" })).toBeNull();
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).endsWith("/publication"),
    ),
  ).toBe(false);
});

it("hides publication controls from guests even when a projection has an owner id", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "user-1",
    creator_name: "Owner",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [],
    steps: [],
    servings: 0,
    prep_time_minutes: 0,
    cook_time_minutes: 0,
    tags: [],
    visibility: "public",
    can_edit: false,
    can_delete: false,
    can_manage_sharing: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    if (url === "/api/public/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  expect(screen.queryByRole("button", { name: "Manage publication" })).toBeNull();
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).endsWith("/publication"),
    ),
  ).toBe(false);
});

it("does not show or fetch publication settings for a remote recipe", async () => {
  const remote = {
    object_id: "https://remote.example/recipes/1",
    creator_name: "Remote cook",
    source_origin: "https://remote.example",
    sequence: 1,
    recipe: {
      title: "Remote soup",
      description: "A remote recipe.",
      ingredients: [],
      steps: [],
      servings: 0,
      prep_time_minutes: 0,
      cook_time_minutes: 0,
      tags: [],
    },
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([remote]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Remote soup" }));
  expect(screen.queryByRole("button", { name: "Manage publication" })).toBeNull();
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).endsWith("/publication"),
    ),
  ).toBe(false);
});

it("sends explicit safe initial publication fields and public access confirmation", async () => {
  let createBody: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/cookbooks") return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes" && init?.method === "POST") {
      createBody = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(jsonResponse({ id: "new-recipe" }));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click((await screen.findAllByRole("button", { name: /Add a recipe/i }))[0]);
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Summer salad" },
  });
  fireEvent.change(screen.getByLabelText("Ingredients (name | quantity | notes per line)"), {
    target: { value: "Tomatoes" },
  });
  fireEvent.change(screen.getByLabelText("Step 1"), {
    target: { value: "Mix and serve." },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  await waitFor(() => expect(createBody).toBeDefined());
  expect(createBody).toMatchObject({
    initial_publication: {
      publication_state: "unpublished",
      federation_mode: "off",
      index_eligible: false,
    },
  });
  expect(createBody).not.toHaveProperty("confirm_public_access");
  expect(createBody).not.toHaveProperty("version");
});

it("requires public-access confirmation and retains form values after a denied create", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/cookbooks") return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes" && init?.method === "POST")
      return Promise.resolve(
        jsonResponse({ error: "publication_unavailable" }, 409),
      );
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click((await screen.findAllByRole("button", { name: /Add a recipe/i }))[0]);
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Summer salad" },
  });
  fireEvent.change(screen.getByLabelText("Ingredients (name | quantity | notes per line)"), {
    target: { value: "Tomatoes" },
  });
  fireEvent.change(screen.getByLabelText("Step 1"), {
    target: { value: "Mix and serve." },
  });
  fireEvent.change(screen.getByLabelText("Visibility"), {
    target: { value: "public" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  expect(
    await screen.findByText(/confirm that anyone can read this recipe/i),
  ).toBeTruthy();
  expect(fetchMock).not.toHaveBeenCalledWith("/api/recipes", expect.anything());

  fireEvent.click(screen.getByRole("checkbox", { name: /anyone can read/i }));
  fireEvent.click(screen.getByRole("checkbox", { name: /not appear in anonymous/i }));
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  expect(
    await screen.findByText(/publishing requires public visibility/i),
  ).toBeTruthy();
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Summer salad");
  expect((screen.getByLabelText("Visibility") as HTMLSelectElement).value).toBe("public");
});

it("sends only local-access confirmation on a visibility-expanding update", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "user-1",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [{ name: "apples", quantity: "4", notes: "" }],
    steps: [{ text: "Bake it." }],
    servings: 6,
    prep_time_minutes: 10,
    cook_time_minutes: 40,
    tags: [],
    visibility: "private",
    can_edit: true,
    can_delete: true,
    can_manage_sharing: true,
    version: 3,
    cookbook_ids: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  let updateBody: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/cookbooks") return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes/recipe-1" && init?.method === "PUT") {
      updateBody = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(jsonResponse({ ...recipe, visibility: "public" }));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit recipe" }));
  fireEvent.change(screen.getByLabelText("Visibility"), {
    target: { value: "public" },
  });
  fireEvent.click(screen.getByRole("checkbox", { name: /anyone can read/i }));
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

  await waitFor(() => expect(updateBody).toBeDefined());
  expect(updateBody).toHaveProperty("confirm_public_access", true);
  expect(updateBody).not.toHaveProperty("initial_publication");
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status });
}

it.each([
  ["open", "Anyone can create an account with email sign-in."],
  [
    "invite_only",
    "New accounts need an invitation; existing accounts can still sign in.",
  ],
  [
    "disabled",
    "New email registrations are disabled; existing accounts can still sign in.",
  ],
] as const)("explains %s email registration policy", async (policy, copy) => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/public/config")
        return Promise.resolve(jsonResponse(publicConfig(true, false, policy)));
      if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
      if (url === "/api/public/remote-recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/public/recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));

  expect((await screen.findByText(copy)).textContent).toBe(copy);
  expect(screen.getByLabelText("Email address")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
});

it("shows only the enabled passkey action when email sign-in is disabled", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/public/config")
        return Promise.resolve(jsonResponse(publicConfig(false, true, "disabled")));
      if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
      if (url === "/api/public/remote-recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/public/recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));

  expect(await screen.findByRole("button", { name: "Sign in with a passkey" })).toBeTruthy();
  expect(screen.queryByLabelText("Email address")).toBeNull();
  expect(screen.getByText("Email link sign-in is unavailable on this instance.")).toBeTruthy();
});

it("reports when neither sign-in method is available", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/public/config")
        return Promise.resolve(jsonResponse(publicConfig(false, false)));
      if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
      if (url === "/api/public/remote-recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/public/recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));

  expect(
    await screen.findByText("Sign-in is currently unavailable on this instance."),
  ).toBeTruthy();
  expect(screen.queryByLabelText("Email address")).toBeNull();
  expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
});

it("shows a retryable configuration error instead of guessing available methods", async () => {
  const configRetry = deferred<Response>();
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config")
      return fetchMock.mock.calls.filter(([call]) => String(call) === url).length === 1
        ? Promise.resolve(jsonResponse({}, 503))
        : configRetry.promise;
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));
  expect(
    await screen.findByText("Could not load sign-in options. Try again."),
  ).toBeTruthy();
  expect(screen.queryByLabelText("Email address")).toBeNull();
  expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await act(async () => {
    configRetry.resolve(jsonResponse(publicConfig(true, false)));
    await configRetry.promise;
  });
  expect(await screen.findByLabelText("Email address")).toBeTruthy();
});

it("preserves the explicit email-unavailable message", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/public/config")
        return Promise.resolve(jsonResponse(publicConfig(true, false)));
      if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
      if (url === "/api/public/remote-recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/public/recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/auth/email")
        return Promise.resolve(jsonResponse({ error: "email_unavailable" }, 503));
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));
  fireEvent.change(await screen.findByLabelText("Email address"), {
    target: { value: "cook@example.test" },
  });
  fireEvent.click(screen.getByRole("button", { name: /Send sign-in link/ }));

  expect(await screen.findByText("Email sign-in is not configured on this instance.")).toBeTruthy();
});

it("defers optional passkey setup until config loads without blocking confirmed sign-in", async () => {
  const config = deferred<Response>();
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config") return config.promise;
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(
        sessionChecks === 1
          ? jsonResponse({}, 401)
          : jsonResponse({
              id: "user-1",
              email: "cook@example.test",
              display_name: "Cook",
              is_admin: false,
            }),
      );
    }
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/auth/consume") return Promise.resolve(jsonResponse({}));
    if (url === "/api/auth/passkeys") return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=one-time-token");

  renderApp();
  fireEvent.click(
    await screen.findByRole("button", { name: "Continue to Cookbook" }),
  );

  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Confirm your sign-in" })).toBeNull(),
  );
  expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/api/auth/consume")).toHaveLength(1);
  expect(screen.queryByRole("dialog", { name: "Add a passkey?" })).toBeNull();

  await act(async () => {
    config.resolve(jsonResponse(publicConfig(true, true)));
    await config.promise;
  });
  expect(await screen.findByRole("dialog", { name: "Add a passkey?" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Not now" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Add a passkey?" })).toBeNull(),
  );
});

it("does not offer first-passkey setup when passkeys are disabled", async () => {
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config")
      return Promise.resolve(jsonResponse(publicConfig(true, false)));
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(
        sessionChecks === 1
          ? jsonResponse({}, 401)
          : jsonResponse({ id: "user-1", email: "cook@example.test", display_name: "Cook", is_admin: false }),
      );
    }
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/auth/consume") return Promise.resolve(jsonResponse({}));
    if (url === "/api/auth/passkeys") return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=one-time-token");

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Continue to Cookbook" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Confirm your sign-in" })).toBeNull(),
  );
  expect(screen.queryByRole("dialog", { name: "Add a passkey?" })).toBeNull();
  expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/api/auth/consume")).toHaveLength(1);
});

it("closes auth-bound dialogs on sign-out so public sign-in can open", async () => {
  const recipe = {
    id: "recipe-1",
    owner_id: "another-user",
    cookbook_ids: ["hidden-private-book"],
    creator_name: "Alex",
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [],
    steps: [{ text: "Bake it." }],
    servings: 6,
    prep_time_minutes: 10,
    cook_time_minutes: 40,
    tags: [],
    visibility: "private",
    can_edit: true,
    can_delete: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/auth/me")
        return Promise.resolve(
          jsonResponse({
            id: "user-1",
            email: "alex@example.com",
            display_name: "Alex",
            is_admin: false,
            created_at: "2026-01-01T00:00:00Z",
          }),
        );
      if (url === "/api/recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([recipe]));
      if (url === "/api/public/remote-recipes?limit=24&offset=0")
        return Promise.resolve(jsonResponse([]));
      if (url === "/api/auth/logout") return Promise.resolve(jsonResponse({}));
      return Promise.resolve(jsonResponse({}, 404));
    }),
  );

  renderApp();
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit recipe" }));
  expect(await screen.findByRole("dialog", { name: "Edit recipe" })).toBeTruthy();
  expect(screen.queryByLabelText("Add to cookbooks")).toBeNull();
  expect(
    (screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  expect(
    screen.getByText(/does not confirm safe cookbook sharing permissions/i),
  ).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Edit recipe" })).toBeNull(),
  );

  fireEvent.click(screen.getByRole("button", { name: /^Sign in$/ }));
  expect(
    await screen.findByRole("dialog", { name: "Sign in" }),
  ).toBeTruthy();
});
