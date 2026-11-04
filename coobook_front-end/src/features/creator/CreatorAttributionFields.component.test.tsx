import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import CreatorAttributionFields from "./CreatorAttributionFields.tsx";

afterEach(cleanup);

it("provides labeled native creator choice controls with a clear unverified-name disclosure", () => {
  let choice: "local_profile" | "name_claim" | "unknown" | "" = "";
  const { rerender } = render(<fieldset><CreatorAttributionFields resourceKind="course" choice={choice} name="" onChoiceChange={(next) => { choice = next; }} onNameChange={() => {}} /></fieldset>);
  const select = screen.getByRole("combobox", { name: "Creator credit" });
  fireEvent.change(select, { target: { value: "name_claim" } });
  expect(choice).toBe("name_claim");
  rerender(<fieldset><CreatorAttributionFields resourceKind="course" choice="name_claim" name="" onChoiceChange={() => {}} onNameChange={() => {}} /></fieldset>);
  expect(screen.getByLabelText("Creator name (unverified)")).toBeTruthy();
  expect(screen.getByText(/This is unverified text, not a linked profile/)).toBeTruthy();
  fireEvent.change(screen.getByRole("combobox", { name: "Creator credit" }), { target: { value: "unknown" } });
});
