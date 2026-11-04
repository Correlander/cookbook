import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CourseForm from "./CourseForm.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("keeps course creation in the extracted feature form", async () => {
  const saved = vi.fn(async () => {});
  const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 201 }));
  vi.stubGlobal("fetch", fetchMock);
  render(<CourseForm course={null} onClose={vi.fn()} onSaved={saved} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Kitchen basics" } });
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  expect(fetchMock).toHaveBeenCalledWith("/api/courses", expect.objectContaining({ method: "POST" }));
});
