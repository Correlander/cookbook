import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import PasskeyEnrollment from "./PasskeyEnrollment.tsx";
import { enrollPasskeyWithGrant } from "../../passkeys.ts";

vi.mock("../../passkeys.ts", () => ({
  enrollPasskeyWithGrant: vi.fn(),
}));

const enrollment = vi.mocked(enrollPasskeyWithGrant);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe("PasskeyEnrollment", () => {
  beforeEach(() => {
    enrollment.mockReset();
  });

  it("submits with Enter, clears the code after success, and never persists it", async () => {
    let finish!: () => void;
    enrollment.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
    );
    const localWrite = vi.spyOn(window.localStorage, "setItem");
    const sessionWrite = vi.spyOn(window.sessionStorage, "setItem");
    const originalURL = window.location.href;
    const code = "0123456789abcdef0123456789abcdef." + "a".repeat(64);

    render(<PasskeyEnrollment />);
    const input = screen.getByLabelText(
      "One-time enrollment code",
    ) as HTMLInputElement;
    expect(input.type).toBe("password");
    fireEvent.change(input, { target: { value: code } });
    expect(document.body.textContent).not.toContain(code);

    const form = input.closest("form");
    expect(form).not.toBeNull();
    await act(async () => {
      form?.requestSubmit();
    });

    expect(enrollment).toHaveBeenCalledWith(code, expect.any(AbortSignal));
    expect(screen.getByRole("status").textContent).toMatch(/waiting for your device/i);
    expect((screen.getByRole("button", { name: "Waiting…" }) as HTMLButtonElement).disabled).toBe(true);
    expect(window.location.href).toBe(originalURL);
    expect(localWrite).not.toHaveBeenCalled();
    expect(sessionWrite).not.toHaveBeenCalled();

    await act(async () => finish());
    expect(
      await screen.findByRole("heading", { name: "Your passkey is ready" }),
    ).toBeTruthy();
    const successHeading = screen.getByRole("heading", {
      name: "Your passkey is ready",
    });
    expect(successHeading.tabIndex).toBe(-1);
    expect(document.activeElement).toBe(successHeading);
    expect(screen.getByRole("status").textContent).toMatch(
      /setup is complete.*sign in with this passkey/i,
    );
    expect(screen.getByText(/does not sign you in automatically/i)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Continue to sign in" }).getAttribute("href")).toBe("/");
    expect(document.body.textContent).not.toContain(code);
    expect(localWrite).not.toHaveBeenCalled();
    expect(sessionWrite).not.toHaveBeenCalled();
  });

  it("clears the code and aborts the browser ceremony when cancelled", async () => {
    let finish!: () => void;
    enrollment.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );

    render(<PasskeyEnrollment />);
    const input = screen.getByLabelText("One-time enrollment code");
    fireEvent.change(input, { target: { value: "one-time-code" } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);
    const signal = enrollment.mock.calls[0]?.[1];
    expect(signal?.aborted).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Cancel enrollment" }));
    expect(signal?.aborted).toBe(true);
    expect((screen.getByLabelText("One-time enrollment code") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("status").textContent).toMatch(/setup was cancelled/i);

    await act(async () => finish());
    expect(screen.queryByRole("heading", { name: "Your passkey is ready" })).toBeNull();
  });

  it("aborts an active ceremony when the page island unmounts", () => {
    enrollment.mockImplementationOnce(() => new Promise<void>(() => {}));
    const view = render(<PasskeyEnrollment />);
    fireEvent.change(screen.getByLabelText("One-time enrollment code"), {
      target: { value: "one-time-code" },
    });
    fireEvent.submit(
      screen.getByLabelText("One-time enrollment code").closest("form") as HTMLFormElement,
    );
    const signal = enrollment.mock.calls[0]?.[1];

    view.unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("shows safe recovery guidance without echoing backend details or the code", async () => {
    enrollment.mockRejectedValueOnce(
      new Error("grant operator-grant-secret expired at internal timestamp"),
    );
    const code = "operator-grant-secret";
    render(<PasskeyEnrollment />);
    fireEvent.change(screen.getByLabelText("One-time enrollment code"), {
      target: { value: code },
    });
    fireEvent.submit(
      screen.getByLabelText("One-time enrollment code").closest("form") as HTMLFormElement,
    );

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/could not be used/i);
    expect(document.body.textContent).not.toContain(code);
    expect(document.body.textContent).not.toContain("internal timestamp");
    expect((screen.getByLabelText("One-time enrollment code") as HTMLInputElement).value).toBe("");
  });

  it("treats a dismissed authenticator prompt as cancellation", async () => {
    enrollment.mockRejectedValueOnce(
      new DOMException("The user closed the prompt.", "NotAllowedError"),
    );
    render(<PasskeyEnrollment />);
    fireEvent.change(screen.getByLabelText("One-time enrollment code"), {
      target: { value: "one-time-code" },
    });
    fireEvent.submit(
      screen.getByLabelText("One-time enrollment code").closest("form") as HTMLFormElement,
    );

    const status = await screen.findByRole("status");
    expect(status.textContent).toMatch(/setup was cancelled/i);
    expect(status.textContent).not.toMatch(/invalid, expired, or already used/i);
    expect((screen.getByLabelText("One-time enrollment code") as HTMLInputElement).value).toBe("");
  });
});
