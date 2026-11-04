import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import App from "./App.tsx";
import { AppSessionProvider } from "./app/AppSession.tsx";

function renderApp(pathname = "/") {
  return render(
    <AppSessionProvider>
      <App pathname={pathname} />
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

it("shows authenticated typed credits in Home cards as plain text and links them only in details", async () => {
  const reader = {
    id: "reader-1",
    email: "reader@example.test",
    display_name: "Reader",
    is_admin: false,
  };
  const recipe = {
    id: "recipe-1",
    owner_id: "owner-1",
    creator_name: "Legacy Cook",
    creator_attributions: [
      {
        subject_kind: "local",
        profile: { id: "profile-1", public_name: "Avery Cook", visibility: "instance" },
        role: "creator",
        provenance: "self_asserted_at_creation",
      },
      {
        subject_kind: "external_claim",
        name_claim: "Avery Cook",
        role: "contributor",
        provenance: "portable_claim",
      },
    ],
    title: "Tomato soup",
    description: "A warm soup.",
    ingredients: [{ name: "Tomato" }],
    steps: [{ text: "Cook." }],
    servings: 2,
    prep_time_minutes: 5,
    cook_time_minutes: 10,
    tags: [],
    visibility: "private",
    can_edit: false,
    can_delete: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config") return Promise.resolve(jsonResponse(publicConfig(true, true)));
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(reader));
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/instance/authority")
      return Promise.resolve(jsonResponse({
        is_instance_owner: false,
        is_admin: false,
        owner_claimed: true,
        can_manage_instance_roles: false,
      }));
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(jsonResponse({ transfers: [] }));
    if (url === "/api/cookbooks?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();

  const card = await screen.findByRole("button", { name: "Open Tomato soup" });
  expect(card.textContent).toContain("By Legacy Cook");
  expect(card.textContent).toContain("Creator · Self-asserted at creation");
  expect(card.textContent).toContain("Unverified creator claim: Avery Cook");
  expect(card.querySelector("a")).toBeNull();

  fireEvent.click(card);
  const dialog = await screen.findByRole("dialog", { name: "Tomato soup" });
  const profileLink = within(dialog).getByRole("link", { name: "Avery Cook" });
  expect(profileLink.getAttribute("href")).toBe("/creator?id=profile-1");
  expect(within(dialog).getByText("Unverified creator claim: Avery Cook")).toBeTruthy();
});

it("keeps public Home cards on the legacy byline even if a response has a typed field", async () => {
  const recipe = {
    id: "public-recipe",
    creator_name: "Legacy Cook",
    creator_attributions: [{
      subject_kind: "local",
      profile: { id: "private-profile", public_name: "Private Name", visibility: "private" },
      role: "creator",
      provenance: "self_asserted_at_creation",
    }],
    title: "Tomato soup",
    description: "A warm soup.",
    ingredients: [],
    steps: [{ text: "Cook." }],
    servings: 2,
    prep_time_minutes: 5,
    cook_time_minutes: 10,
    tags: [],
    visibility: "public",
    can_edit: false,
    can_delete: false,
    version: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config") return Promise.resolve(jsonResponse(publicConfig(true, true)));
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    if (url === "/api/public/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();

  const card = await screen.findByRole("button", { name: "Open Tomato soup" });
  expect(card.textContent).toContain("By Legacy Cook");
  expect(card.textContent).not.toContain("Private Name");
  expect(card.querySelector("a")).toBeNull();
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
    if (url === "/api/account/creator-profile")
      return Promise.resolve(jsonResponse({ profile: null }));
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
  fireEvent.change(await screen.findByLabelText("Creator credit"), {
    target: { value: "unknown" },
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
    if (url === "/api/account/creator-profile")
      return Promise.resolve(jsonResponse({ profile: null }));
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
  fireEvent.change(await screen.findByLabelText("Creator credit"), {
    target: { value: "unknown" },
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

it("ignores an actor A create completion after session refresh switches the form to actor B", async () => {
  let sessionChecks = 0;
  let resolveCreate!: (value: Response) => void;
  const pendingCreate = new Promise<Response>((resolve) => {
    resolveCreate = resolve;
  });
  const actorA = {
    id: "actor-a",
    email: "a@example.test",
    display_name: "Cook A",
    is_admin: false,
  };
  const actorB = {
    id: "actor-b",
    email: "b@example.test",
    display_name: "Cook B",
    is_admin: false,
  };
  let profileChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(jsonResponse(sessionChecks === 1 ? actorA : actorB));
    }
    if (url === "/api/public/config")
      return Promise.resolve(jsonResponse(publicConfig(true, false)));
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/account/creator-profile") {
      profileChecks += 1;
      return Promise.resolve(jsonResponse({ profile: null }));
    }
    if (url === "/api/cookbooks") return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes" && init?.method === "POST")
      return pendingCreate;
    if (url === "/api/auth/consume")
      return Promise.resolve(jsonResponse({}));
    if (url === "/api/auth/passkeys")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=actor-switch");

  renderApp();
  await screen.findByRole("heading", { name: "Welcome back, Cook A." });
  fireEvent.click((await screen.findAllByRole("button", { name: "Add a recipe" }))[0]);
  await screen.findByRole("combobox", { name: "Creator credit" });
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Actor A draft" },
  });
  fireEvent.change(
    screen.getByLabelText("Ingredients (name | quantity | notes per line)"),
    { target: { value: "Tomato" } },
  );
  fireEvent.change(screen.getByLabelText("Step 1"), {
    target: { value: "Cook." },
  });
  fireEvent.change(screen.getByLabelText("Creator credit"), {
    target: { value: "name_claim" },
  });
  fireEvent.change(screen.getByLabelText("Creator name (unverified)"), {
    target: { value: "A private claim" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(
        ([input, init]) =>
          String(input) === "/api/recipes" && init?.method === "POST",
      ),
    ).toBe(true),
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Continue to Cookbook" }),
  );
  expect(
    await screen.findByRole("heading", { name: "Welcome back, Cook B." }),
  ).toBeTruthy();
  const actorBChoice = await screen.findByRole("combobox", {
    name: "Creator credit",
  });
  await waitFor(() => expect(profileChecks).toBe(2));
  expect((actorBChoice as HTMLSelectElement).value).toBe("");
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("");

  await act(async () => {
    resolveCreate(
      jsonResponse({
        id: "late-a-recipe",
        owner_id: actorA.id,
        creator_name: "A private claim",
        title: "Actor A draft",
        ingredients: [],
        steps: [],
        visibility: "private",
      }),
    );
    await pendingCreate;
  });
  expect(
    screen.getByRole("combobox", { name: "Creator credit" }),
  ).toBeTruthy();
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("");
  expect(
    screen.queryByRole("button", { name: "Open Actor A draft" }),
  ).toBeNull();
});

it("discards a same-recipe actor A image upload completion after Home switches to actor B", async () => {
  let sessionChecks = 0;
  let resolveImageUpload!: (value: Response) => void;
  const pendingImageUpload = new Promise<Response>((resolve) => {
    resolveImageUpload = resolve;
  });
  const actorA = {
    id: "actor-a",
    email: "a@example.test",
    display_name: "Cook A",
    is_admin: false,
  };
  const actorB = {
    id: "actor-b",
    email: "b@example.test",
    display_name: "Cook B",
    is_admin: false,
  };
  const recipe = {
    id: "recipe-1",
    owner_id: actorA.id,
    title: "Apple pie",
    description: "A family recipe.",
    ingredients: [{ name: "Apples", quantity: "4", notes: "" }],
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
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(jsonResponse(sessionChecks === 1 ? actorA : actorB));
    }
    if (url === "/api/public/config")
      return Promise.resolve(jsonResponse(publicConfig(true, false)));
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([recipe]));
    if (url === "/api/public/remote-recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/cookbooks") return Promise.resolve(jsonResponse([]));
    if (url === "/api/recipes/recipe-1" && init?.method === "PUT")
      return Promise.resolve(jsonResponse({ ...recipe, title: "Actor A edited pie" }));
    if (url === "/api/recipes/recipe-1/image" && init?.method === "PUT")
      return pendingImageUpload;
    if (url === "/api/auth/consume")
      return Promise.resolve(jsonResponse({}));
    if (url === "/api/auth/passkeys")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=actor-edit-switch");

  renderApp();
  await screen.findByRole("heading", { name: "Welcome back, Cook A." });
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit recipe" }));
  expect(await screen.findByRole("dialog", { name: "Edit recipe" })).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Actor A edited pie" },
  });
  fireEvent.change(screen.getByLabelText(/Recipe photo/), {
    target: { files: [new File(["image"], "pie.png", { type: "image/png" })] },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(
        ([input, init]) =>
          String(input) === "/api/recipes/recipe-1/image" &&
          init?.method === "PUT",
      ),
    ).toBe(true),
  );

  fireEvent.click(
    screen.getByRole("button", { name: "Continue to Cookbook" }),
  );
  expect(
    await screen.findByRole("heading", { name: "Welcome back, Cook B." }),
  ).toBeTruthy();
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Edit recipe" })).toBeNull(),
  );
  expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  expect(screen.getByRole("button", { name: "Open Apple pie" })).toBeTruthy();

  await act(async () => {
    resolveImageUpload(jsonResponse({ image: "/api/recipes/recipe-1/image", version: recipe.version + 1 }));
    await pendingImageUpload;
  });
  expect(screen.getByRole("button", { name: "Open Apple pie" })).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Open Actor A edited pie" }),
  ).toBeNull();
  expect(screen.queryByRole("dialog", { name: "Edit recipe" })).toBeNull();
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

it("keeps instance account management off Home", async () => {
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "member-1",
          email: "member@example.test",
          display_name: "Member",
          is_admin: false,
        }),
      );
    if (url === "/api/instance/authority")
      return Promise.resolve(
        jsonResponse({
          is_instance_owner: false,
          is_admin: false,
          owner_claimed: true,
          can_manage_instance_roles: false,
        }),
      );
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(jsonResponse({ transfers: [] }));
    return Promise.resolve(jsonResponse([], 200));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();

  expect(await screen.findByText("Welcome back, Member.")).toBeTruthy();
  await waitFor(() => expect(calls).toContain("/api/instance/owner-transfers"));
  expect(calls.some((url) => url.startsWith("/api/admin/users?"))).toBe(false);
  expect(calls.includes("/api/admin/invitations")).toBe(false);
  expect(calls.includes("/api/admin/audit?limit=24&offset=0")).toBe(false);
  expect(calls.some((url) => url.startsWith("/api/admin/federation/"))).toBe(false);
  expect(screen.queryByRole("heading", { name: "Accounts" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Instance" })).toBeNull();
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

it("shows cookbook edit and member controls only to the owner", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        jsonResponse({
          id: "owner-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        }),
      );
    if (url === "/api/cookbooks?limit=24&offset=0")
      return Promise.resolve(
        jsonResponse([
          {
            id: "owned-book",
            owner_id: "owner-1",
            title: "My recipes",
            description: "",
            visibility: "private",
          },
          {
            id: "member-book",
            owner_id: "someone-else",
            title: "Shared recipes",
            description: "",
            visibility: "public",
          },
        ]),
      );
    if (url === "/api/cookbooks/owned-book/members")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/cookbooks/owned-book/members/invitations")
      return Promise.resolve(jsonResponse([]));
    if (
      url ===
      "/api/recipes?cookbook_id=owned-book&limit=24&offset=0&sort=recent"
    )
      return Promise.resolve(
        jsonResponse([
          {
            id: "soup-1",
            owner_id: "owner-1",
            creator_name: "Owner",
            title: "Garden soup",
            description: "A simple soup.",
            ingredients: [{ name: "carrots" }],
            steps: [{ text: "Cook gently." }],
            servings: 2,
            prep_time_minutes: 5,
            cook_time_minutes: 20,
            tags: ["vegetarian"],
            visibility: "private",
            can_edit: true,
            can_delete: true,
            can_manage_sharing: true,
            version: 1,
            cookbook_ids: ["owned-book"],
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ]),
      );
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();

  const ownerCard = (await screen.findByText("My recipes")).closest("article");
  if (!ownerCard) throw new Error("Expected an owned cookbook card.");
  expect(ownerCard.textContent).toContain("Edit cookbook");
  expect(ownerCard.textContent).toContain("Share and manage members");
  expect(
    screen.getByRole("button", { name: "Share and manage members" }),
  ).toBeTruthy();
  const memberCard = screen.getByText("Shared recipes").closest("article");
  if (!memberCard) throw new Error("Expected a shared cookbook card.");
  expect(memberCard.textContent).toContain("Open cookbook");
  expect(memberCard.textContent).not.toContain("Edit cookbook");
  expect(memberCard.textContent).not.toContain("Share and manage members");

  fireEvent.click(screen.getByRole("button", { name: "Share and manage members" }));
  expect(await screen.findByRole("dialog", { name: "Cookbook members" })).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith("/api/cookbooks/owned-book/members");
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/cookbooks/owned-book/members/invitations",
  );

  fireEvent.click(screen.getByRole("button", { name: "Close members" }));
  const cookbookLink = screen.getAllByRole("link", { name: "Open cookbook" })[0];
  expect(cookbookLink?.getAttribute("href")).toBe("/cookbook?id=owned-book");
});

it("keeps both legacy cookbook anchors on the same useful section through session hydration", async () => {
  const session = deferred<Response>();
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return session.promise;
    if (url === "/api/auth/config") return Promise.resolve(jsonResponse(publicConfig(true, true)));
    if (url === "/api/cookbooks?limit=24&offset=0")
      return Promise.resolve(jsonResponse([
        {
          id: "home-book",
          owner_id: "owner-1",
          title: "My kitchen book",
          description: "",
          visibility: "private",
        },
      ]));
    if (url.startsWith("/api/recipes?")) return Promise.resolve(jsonResponse([]));
    if (url === "/api/instance/authority")
      return Promise.resolve(jsonResponse({
        is_instance_owner: false,
        is_admin: false,
        owner_claimed: false,
        can_manage_instance_roles: false,
      }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderApp();
  const cookbookSection = document.getElementById("cookbooks");
  const myCookbooksAnchor = document.getElementById("my-cookbooks");
  expect(cookbookSection).toBeTruthy();
  expect(myCookbooksAnchor).toBeTruthy();
  expect(cookbookSection?.textContent).toContain("Browse public cookbooks");
  expect(myCookbooksAnchor?.textContent).toContain("Browse public cookbooks");
  expect(cookbookSection?.querySelector('a[href="/cookbooks"]')?.textContent)
    .toContain("Open cookbooks");
  expect(calls.some((url) => url.startsWith("/api/public/cookbooks"))).toBe(false);

  await act(async () => {
    session.resolve(jsonResponse({
      id: "owner-1",
      email: "owner@example.test",
      display_name: "Owner",
      is_admin: false,
    }));
    await session.promise;
  });

  expect(await screen.findByRole("link", { name: "Open cookbook" })).toBeTruthy();
  expect(screen.getByText("My kitchen book")).toBeTruthy();
  expect(document.getElementById("cookbooks")).toBe(cookbookSection);
  expect(document.getElementById("my-cookbooks")).toBe(myCookbooksAnchor);
  expect(cookbookSection?.textContent).toContain("Cookbooks you can access");
  expect(myCookbooksAnchor?.textContent).toContain("Cookbooks you can access");
  expect(calls).toContain("/api/cookbooks?limit=24&offset=0");
  expect(calls.some((url) => url.startsWith("/api/public/cookbooks"))).toBe(false);
});
