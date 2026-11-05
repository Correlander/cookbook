import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import MenuRecipePicker from "./MenuRecipePicker.tsx";

function recipe(idNumber: number, title: string) {
  return {
    id: idNumber.toString(16).padStart(32, "0"),
    title,
    description: "Not part of the picker projection",
    ingredients: [{ name: "secret ingredient" }],
    steps: [{ text: "secret step" }],
  };
}

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise; });
  return { promise, resolve, reject };
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("searches only authenticated local recipes and passes only id/title to selection", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse([recipe(1, "Soup")]));
  vi.stubGlobal("fetch", fetchMock);
  const onSelect = vi.fn();
  render(<MenuRecipePicker actorID="actor-a" onSelect={onSelect} />);
  await screen.findByRole("button", { name: "Add Soup" });
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/recipes?limit=24&offset=0");
  expect(screen.queryByText("secret ingredient")).toBeNull();

  const option = screen.getByRole("button", { name: "Add Soup" });
  option.focus();
  expect(document.activeElement).toBe(option);
  fireEvent.click(option);
  expect(onSelect).toHaveBeenCalledWith({ id: recipe(1, "Soup").id, title: "Soup" });
  expect(onSelect.mock.calls[0]?.[0]).not.toHaveProperty("ingredients");
  fireEvent.change(screen.getByLabelText("Search local recipes"), { target: { value: "  tomato soup " } });
  await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/recipes?limit=24&offset=0&q=tomato+soup"));
  expect(fetchMock.mock.calls.every(([url]) => String(url).startsWith("/api/recipes?"))).toBe(true);
});

it("ignores a stale response when the search changes", async () => {
  const soup = deferred<Response>();
  const salad = deferred<Response>();
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).includes("q=salad") ? salad.promise : soup.promise);
  vi.stubGlobal("fetch", fetchMock);
  render(<MenuRecipePicker actorID="actor-a" onSelect={vi.fn()} />);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  fireEvent.change(screen.getByLabelText("Search local recipes"), { target: { value: "salad" } });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  salad.resolve(jsonResponse([recipe(2, "Fresh salad")]));
  await screen.findByRole("button", { name: "Add Fresh salad" });
  soup.resolve(jsonResponse([recipe(1, "Stale soup")]));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Add Stale soup" })).toBeNull());
  expect(screen.getByRole("button", { name: "Add Fresh salad" })).toBeTruthy();
});

it("ignores a stale reply after the authenticated actor changes", async () => {
  const actorA = deferred<Response>();
  const actorB = deferred<Response>();
  const fetchMock = vi.fn()
    .mockImplementationOnce(() => actorA.promise)
    .mockImplementationOnce(() => actorB.promise);
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<MenuRecipePicker actorID="actor-a" onSelect={vi.fn()} />);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  view.rerender(<MenuRecipePicker actorID="actor-b" onSelect={vi.fn()} />);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  actorB.resolve(jsonResponse([recipe(2, "Actor B recipe")]));
  await screen.findByRole("button", { name: "Add Actor B recipe" });
  actorA.resolve(jsonResponse([recipe(1, "Actor A recipe")]));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Add Actor A recipe" })).toBeNull());
  expect(screen.getByRole("button", { name: "Add Actor B recipe" })).toBeTruthy();
});

it("pages in 24-item increments and keeps keyboard-accessible native buttons", async () => {
  const firstPage = Array.from({ length: 24 }, (_, index) => recipe(index + 1, `Recipe ${index + 1}`));
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse(firstPage))
    .mockResolvedValueOnce(jsonResponse([recipe(25, "Recipe 25")]));
  vi.stubGlobal("fetch", fetchMock);
  render(<MenuRecipePicker actorID="actor-a" onSelect={vi.fn()} />);
  await screen.findByRole("button", { name: "Add Recipe 24" });
  const nextPage = screen.getByRole("button", { name: "Load more local recipes" });
  expect(nextPage.tagName).toBe("BUTTON");
  fireEvent.click(nextPage);
  await screen.findByRole("button", { name: "Add Recipe 25" });
  expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/recipes?limit=24&offset=24");
});

it("keeps the first page on load-more failure and lets the user retry that page", async () => {
  const firstPage = Array.from({ length: 24 }, (_, index) => recipe(index + 1, `Recipe ${index + 1}`));
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse(firstPage))
    .mockResolvedValueOnce(new Response("{}", { status: 500 }))
    .mockResolvedValueOnce(jsonResponse([recipe(25, "Recipe 25")]));
  vi.stubGlobal("fetch", fetchMock);
  render(<MenuRecipePicker actorID="actor-a" onSelect={vi.fn()} />);
  await screen.findByRole("button", { name: "Add Recipe 24" });
  fireEvent.click(screen.getByRole("button", { name: "Load more local recipes" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Add Recipe 1" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Load more local recipes" }));
  await screen.findByRole("button", { name: "Add Recipe 25" });
  expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/recipes?limit=24&offset=24");
});

it("distinguishes no actor, empty results, loading, and request errors", async () => {
  const initialPage = deferred<Response>();
  const fetchMock = vi.fn()
    .mockImplementationOnce(() => initialPage.promise)
    .mockResolvedValueOnce(new Response("{}", { status: 500 }))
    .mockResolvedValueOnce(jsonResponse([recipe(1, "Available after retry")]));
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<MenuRecipePicker actorID="" onSelect={vi.fn()} />);
  expect(screen.getByRole("status").textContent).toContain("Sign in to choose");
  expect(fetchMock).not.toHaveBeenCalled();

  view.rerender(<MenuRecipePicker actorID="actor-a" onSelect={vi.fn()} />);
  expect(await screen.findByText("Loading local recipes…")).toBeTruthy();
  initialPage.resolve(jsonResponse([]));
  await waitFor(() => expect(screen.getByText("No local recipes found.")).toBeTruthy());
  fireEvent.change(screen.getByLabelText("Search local recipes"), { target: { value: "retry" } });
  expect(await screen.findByRole("alert")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Try recipe search again" }));
  await screen.findByRole("button", { name: "Add Available after retry" });
});
