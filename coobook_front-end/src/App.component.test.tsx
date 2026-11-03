import {
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
  vi.unstubAllGlobals();
});

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

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status });
}

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
    await screen.findByRole("dialog", { name: "Sign in with email" }),
  ).toBeTruthy();
});
