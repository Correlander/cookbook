import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import Toast from "./Toast.tsx";

afterEach(() => { cleanup(); vi.useRealTimers(); });

it("announces success once and dismisses the current message after its timer", () => {
  vi.useFakeTimers();
  const onDismiss = vi.fn();
  const { rerender } = render(<Toast actorID="a" message="Saved." onDismiss={onDismiss} duration={1000} />);
  expect(screen.getByRole("status").textContent).toBe("Saved.");
  rerender(<Toast actorID="a" message="Removed." onDismiss={onDismiss} duration={1000} />);
  act(() => { vi.advanceTimersByTime(999); });
  expect(onDismiss).not.toHaveBeenCalled();
  act(() => { vi.advanceTimersByTime(1); });
  expect(onDismiss).toHaveBeenCalledTimes(1);
});

it("restarts the dismissal timer when the same success happens again", () => {
  vi.useFakeTimers();
  const onDismiss = vi.fn();
  const { rerender } = render(<Toast actorID="a" message="Bookmark updated." revision={1} onDismiss={onDismiss} duration={1000} />);
  act(() => { vi.advanceTimersByTime(800); });
  rerender(<Toast actorID="a" message="Bookmark updated." revision={2} onDismiss={onDismiss} duration={1000} />);
  act(() => { vi.advanceTimersByTime(999); });
  expect(onDismiss).not.toHaveBeenCalled();
  act(() => { vi.advanceTimersByTime(1); });
  expect(onDismiss).toHaveBeenCalledTimes(1);
});

it("cleans up on unmount and does not carry notices across actors", () => {
  vi.useFakeTimers();
  const onDismiss = vi.fn();
  const { rerender, unmount } = render(<Toast actorID="a" message="Saved." onDismiss={onDismiss} />);
  rerender(<Toast actorID="b" message="" onDismiss={onDismiss} />);
  expect(screen.queryByRole("status")).toBeNull();
  act(() => { vi.advanceTimersByTime(3000); });
  expect(onDismiss).not.toHaveBeenCalled();
  rerender(<Toast actorID="b" message="New actor notice." onDismiss={onDismiss} />);
  unmount();
  act(() => { vi.advanceTimersByTime(3000); });
  expect(onDismiss).not.toHaveBeenCalled();
});
