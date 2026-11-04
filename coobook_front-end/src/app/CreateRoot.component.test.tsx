import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CreateRoot from "./CreateRoot.tsx";

function response(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/create"); });

it("opens the existing recipe form for a contextual Library query", async () => {
  window.history.replaceState(null, "", "/create?type=recipes");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response({ courses_enabled: false }));
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
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it("offers courses only when the server configuration enables them", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response({ courses_enabled: false }));
    if (url === "/api/cookbooks") return Promise.resolve(response([]));
    if (url === "/api/instance/authority") return Promise.resolve(response({ is_admin: false, is_instance_owner: false, owner_claimed: true, can_manage_instance_roles: false }));
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    return Promise.resolve(response({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CreateRoot />);
  expect(await screen.findByRole("button", { name: "Recipe" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Cookbook" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Course" })).toBeNull();
  expect(fetchMock).toHaveBeenCalledWith("/api/public/config", { cache: "no-store" });
  fireEvent.click(screen.getByRole("button", { name: "Recipe" }));
  expect(await screen.findByRole("heading", { name: "Add a recipe" })).toBeTruthy();
});

it("returns a newly created private-instance course to the owner's Home surface", async () => {
  const navigate = vi.fn();
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response({ courses_enabled: true, public_instance: false }));
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
