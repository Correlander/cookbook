import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import ViewSwitcher from "./ViewSwitcher.tsx";

afterEach(cleanup);

it("exposes a labelled group with the selected view and keyboard-operable buttons", () => {
  const onChange = vi.fn();
  render(<ViewSwitcher label="Library type" value="recipes" options={[{ value: "recipes", label: "Recipes" }, { value: "cookbooks", label: "Cookbooks" }]} onChange={onChange} />);
  const recipes = screen.getByRole("button", { name: "Recipes" });
  expect(recipes.getAttribute("aria-pressed")).toBe("true");
  fireEvent.click(screen.getByRole("button", { name: "Cookbooks" }));
  expect(onChange).toHaveBeenCalledWith("cookbooks");
});
