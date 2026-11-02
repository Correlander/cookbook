import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import App from "./App.tsx";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status });
}

it("closes auth-bound dialogs on sign-out so public sign-in can open", async () => {
  const recipe = {
    id: "recipe-1",
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

  render(<App />);
  fireEvent.click(await screen.findByRole("button", { name: "Open Apple pie" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit recipe" }));
  expect(await screen.findByRole("dialog", { name: "Edit recipe" })).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: "Edit recipe" })).toBeNull(),
  );

  fireEvent.click(screen.getByRole("button", { name: /^Sign in$/ }));
  expect(
    await screen.findByRole("dialog", { name: "Sign in with email" }),
  ).toBeTruthy();
});
