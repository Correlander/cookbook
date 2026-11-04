import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import AccountNavigation from "./AccountNavigation.tsx";

afterEach(cleanup);

it("links creator profile and security from either account page", () => {
  const { rerender } = render(
    <AccountNavigation pathname="/account/security" />,
  );
  expect(
    screen.getByRole("link", { name: "Creator profile" }).getAttribute("href"),
  ).toBe("/account/creator-profile");
  expect(screen.getByRole("link", { name: "Overview" }).getAttribute("href")).toBe("/account");
  expect(screen.getByRole("link", { name: "Overview" }).getAttribute("aria-current")).toBeNull();
  expect(
    screen
      .getByRole("link", { name: "Account security" })
      .getAttribute("aria-current"),
  ).toBe("page");
  rerender(<AccountNavigation pathname="/account/creator-profile" />);
  expect(
    screen
      .getByRole("link", { name: "Creator profile" })
      .getAttribute("aria-current"),
  ).toBe("page");
  rerender(<AccountNavigation pathname="/account" />);
  expect(screen.getByRole("link", { name: "Overview" }).getAttribute("aria-current")).toBe("page");
});
