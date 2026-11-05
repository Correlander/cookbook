import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CreateRoot from "./CreateRoot.tsx";

function response(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function publicConfig(overrides: Record<string, unknown> = {}) {
  return {
    auth_methods: { email_link: true, passkey: false }, registration_policy: "invite_only",
    public_instance: false, courses_enabled: false, ...overrides,
  };
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/create"); });

it("opens the existing recipe form for a contextual Library query", async () => {
  window.history.replaceState(null, "", "/create?type=recipes");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response(publicConfig()));
    if (url === "/api/cookbooks") return Promise.resolve(response([]));
    return Promise.resolve(response({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot />);
  expect(await screen.findByRole("heading", { name: "Add a recipe" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Recipe" })).toBeNull();
});

it("requires a confirmed signed-in session before offering creation forms", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => Promise.resolve(response(null, String(input) === "/api/auth/me" ? 401 : 404)));
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot />);
  expect(await screen.findByRole("heading", { name: "Sign in to create" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Recipe" })).toBeNull();
  expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/api/auth/me")).toHaveLength(1);
  expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/api/public/config")).toHaveLength(1);
});

it("offers courses only when the server configuration enables them", async () => {
  let resolveConfig!: (value: Response) => void;
  const configResponse = new Promise<Response>((resolve) => { resolveConfig = resolve; });
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return configResponse;
    if (url === "/api/cookbooks") return Promise.resolve(response([]));
    if (url === "/api/instance/authority") return Promise.resolve(response({ is_admin: false, is_instance_owner: false, owner_claimed: true, can_manage_instance_roles: false }));
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    return Promise.resolve(response({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot />);
  expect(await screen.findByRole("button", { name: "Recipe" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Cookbook" })).toBeTruthy();
  expect(screen.getByText("Checking enabled features…")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Course" })).toBeNull();
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/public/config", { cache: "no-store" }));
  expect(screen.queryByRole("button", { name: "Course" })).toBeNull();
  resolveConfig(response(publicConfig()));
  await waitFor(() => expect(screen.queryByText("Checking enabled features…")).toBeNull());
  expect(screen.queryByRole("button", { name: "Course" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Recipe" }));
  expect(await screen.findByRole("heading", { name: "Add a recipe" })).toBeTruthy();
});

it("offers private menus only for the literal menu protocol capability and opens the existing creator form", async () => {
  window.history.replaceState(null, "", "/create?type=menus");
  const created = { id: "a".repeat(32), title: "Dinner", note: "Friday", version: 1, created_at: "2026-11-04T20:00:00Z", updated_at: "2026-11-04T20:00:00Z", entries: [] };
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response(publicConfig({ menu_api: true })));
    if (url === "/api/menus" && init?.method === "POST") return Promise.resolve(response(created, 201));
    return Promise.resolve(response({}));
  });
  const navigate = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot navigate={navigate} />);
  expect(await screen.findByRole("dialog")).toBeTruthy();
  expect(within(screen.getByRole("dialog")).getByRole("heading", { name: "Create a private menu" })).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Dinner" } });
  fireEvent.change(screen.getByRole("combobox", { name: "Creator credit" }), { target: { value: "local_profile" } });
  fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
  await waitFor(() => expect(navigate).toHaveBeenCalledWith(`/menu?id=${created.id}`));
  expect(calls).toContain("/api/menus");
  expect(calls.some((url) => url.startsWith("/api/library/menus"))).toBe(false);
});

it.each([undefined, false, "true", { enabled: true }])("keeps menu creation unavailable for capability %j", async (menuApi) => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    return Promise.resolve(response(publicConfig({ courses_enabled: true, ...(menuApi === undefined ? {} : { menu_api: menuApi }) })));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot />);
  expect(await screen.findByRole("button", { name: "Course" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Menu" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "Create a private menu" })).toBeNull();
});

it("returns a newly created private-instance course to the owner's Home surface", async () => {
  const navigate = vi.fn();
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response(publicConfig({ courses_enabled: true, public_instance: false })));
    if (url === "/api/instance/authority") return Promise.resolve(response({ is_admin: false, is_instance_owner: false, owner_claimed: true, can_manage_instance_roles: false }));
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    if (url === "/api/courses") return Promise.resolve(response({ id: "course-1" }, 201));
    return Promise.resolve(response({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot navigate={navigate} />);
  fireEvent.click(await screen.findByRole("button", { name: "Course" }));
  const courseDialog = await screen.findByRole("dialog");
  expect(within(courseDialog).getByRole("heading", { name: "Create a course" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Course details", level: 2 })).toBeTruthy();
  expect(screen.getAllByRole("heading", { name: "Create a course" })).toHaveLength(1);
  expect(screen.getByLabelText("Visibility")).toHaveProperty("value", "private");
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Private lessons" } });
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(navigate).toHaveBeenCalledWith("/"));
  expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith("/api/public/courses"))).toBe(false);
});
