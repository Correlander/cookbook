import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import PublisherProfileSetupGuidance from "./PublisherProfileSetupGuidance.tsx";

afterEach(cleanup);

it("offers an explicit private-profile setup link that opens in a separate tab", () => {
  render(<PublisherProfileSetupGuidance />);
  const link = screen.getByRole("link", {
    name: "Set up your creator profile (opens in a new tab)",
  });
  expect(link.getAttribute("href")).toBe("/account/creator-profile");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener");
  expect(screen.getByText(/Your profile can stay private/)).toBeTruthy();
  expect(screen.getByText(/return here, and submit again/)).toBeTruthy();
});
