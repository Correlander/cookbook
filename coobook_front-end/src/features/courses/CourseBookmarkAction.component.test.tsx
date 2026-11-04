import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CourseBookmarkAction from "./CourseBookmarkAction.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }); }

it("saves and removes with the exact course reference endpoints", async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(json({ saved: false }))
    .mockResolvedValueOnce(json({ bookmark_id: "bookmark-1", course_id: "course-1", created_at: "now" }, 201))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  render(<CourseBookmarkAction courseID="course-1" title="Basics" currentUserID="actor-1" />);
  const save = await screen.findByRole("button", { name: "Save Basics to Library" });
  fireEvent.click(save);
  await screen.findByRole("button", { name: "Remove Basics from Library" });
  fireEvent.click(screen.getByRole("button", { name: "Remove Basics from Library" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
  expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
    "/api/library/course-bookmarks/status?local_course_id=course-1", "/api/library/course-bookmarks", "/api/library/course-bookmarks/bookmark-1",
  ]);
});

it("does not infer unsaved state after a failed exact-target check", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({}, 404)));
  render(<CourseBookmarkAction courseID="private-course" title="Private" currentUserID="actor-1" />);
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry saved status" })).toBeTruthy();
  expect((screen.getByRole("button", { name: "Saved status unavailable" }) as HTMLButtonElement).disabled).toBe(true);
});

it("ignores status responses from a prior actor", async () => {
  let resolveOld!: (response: Response) => void;
  const fetchMock = vi.fn().mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveOld = resolve; })).mockResolvedValueOnce(json({ saved: false }));
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<CourseBookmarkAction courseID="course-1" title="Basics" currentUserID="actor-1" />);
  view.rerender(<CourseBookmarkAction courseID="course-1" title="Basics" currentUserID="actor-2" />);
  await screen.findByRole("button", { name: "Save Basics to Library" });
  await act(async () => resolveOld(json({ saved: true, bookmark_id: "stale" })));
  expect(screen.getByRole("button", { name: "Save Basics to Library" })).toBeTruthy();
});
