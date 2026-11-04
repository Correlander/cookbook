import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { CreatorAttribution } from "../../apiModels";
import CreatorCredits from "./CreatorCredits.tsx";

const localCreator: CreatorAttribution = {
  subject_kind: "local",
  profile: { id: "profile-1", public_name: "Avery Cook", visibility: "instance" },
  role: "creator",
  provenance: "self_asserted_at_creation",
};

afterEach(cleanup);

it("omits absent readable relations and renders a blank private self profile without its ID", () => {
  const { rerender } = render(
    <CreatorCredits attributions={undefined} resourceKind="recipe" />,
  );
  expect(screen.queryByRole("region", { name: "Recipe creator credits" })).toBeNull();

  rerender(
    <CreatorCredits
      attributions={[{
        subject_kind: "local",
        profile: { id: "private-profile-secret", public_name: "", visibility: "private" },
        role: "creator",
        provenance: "self_asserted_at_creation",
      }]}
      resourceKind="recipe"
      currentUserID="owner-1"
    />,
  );
  expect(screen.getByText("Your creator profile (name not set)")).toBeTruthy();
  expect(screen.queryByRole("link")).toBeNull();
  expect(document.body.textContent).not.toContain("private-profile-secret");
});

it("does not call a blank instance profile self or turn an unknown role into adapter", () => {
  const unknownRole = {
    subject_kind: "external_claim",
    role: "reviewer",
    provenance: "portable_claim",
  } as unknown as CreatorAttribution;

  render(
    <CreatorCredits
      attributions={[
        {
          subject_kind: "local",
          profile: { id: "instance-profile", public_name: "", visibility: "instance" },
          role: "creator",
          provenance: "self_asserted_at_creation",
        },
        unknownRole,
      ]}
      resourceKind="recipe"
      currentUserID="reader-1"
    />,
  );

  const region = screen.getByRole("region", { name: "Recipe creator credits" });
  expect(within(region).getByText("Creator profile (name not set)")).toBeTruthy();
  expect(within(region).queryByText("Your creator profile (name not set)")).toBeNull();
  expect(within(region).getByText("Unverified relationship · Imported claim")).toBeTruthy();
  expect(within(region).queryByText("Adapter · Imported claim")).toBeNull();
  expect(within(region).queryByRole("link")).toBeNull();
});

it("keeps external claims inert and unverified when their name matches a local profile", () => {
  render(
    <CreatorCredits
      attributions={[
        localCreator,
        {
          subject_kind: "external_claim",
          name_claim: "Avery Cook",
          role: "contributor",
          provenance: "source-instance-controlled-text",
        },
        {
          subject_kind: "external_claim",
          role: "adapter",
          provenance: "portable_claim",
        },
      ]}
      resourceKind="recipe"
      currentUserID="reader-1"
    />,
  );

  const region = screen.getByRole("region", { name: "Recipe creator credits" });
  expect(within(region).getByRole("link", { name: "Avery Cook" }).getAttribute("href"))
    .toBe("/creator?id=profile-1");
  expect(within(region).getByText("Unverified creator claim: Avery Cook")).toBeTruthy();
  expect(within(region).getByText("Unverified creator claim", { exact: true })).toBeTruthy();
  expect(within(region).getAllByText("Contributor · Unverified provenance")).toHaveLength(1);
  expect(within(region).getByText("Adapter · Imported claim")).toBeTruthy();
  expect(within(region).getAllByRole("link")).toHaveLength(1);
  expect(within(region).queryByRole("img")).toBeNull();
});

it("preserves multiple local roles and labels a cookbook creator as collection creator", () => {
  const { rerender } = render(
    <CreatorCredits
      attributions={[
        localCreator,
        { ...localCreator, role: "contributor" },
        { ...localCreator, role: "adapter", provenance: "adapter_source" },
      ]}
      resourceKind="recipe"
      currentUserID="reader-1"
    />,
  );
  const recipeCredits = screen.getByRole("region", { name: "Recipe creator credits" });
  expect(within(recipeCredits).getAllByRole("link", { name: "Avery Cook" })).toHaveLength(3);
  expect(within(recipeCredits).getByText("Contributor · Self-asserted at creation")).toBeTruthy();
  expect(within(recipeCredits).getByText("Adapter · Recorded as a source adapter")).toBeTruthy();

  rerender(
    <CreatorCredits
      attributions={[localCreator]}
      resourceKind="cookbook"
      currentUserID="reader-1"
    />,
  );
  expect(screen.getByText("Collection creator · Self-asserted at creation")).toBeTruthy();
});

it("uses plain text in compact card context even when a signed-in viewer can open the profile", () => {
  render(
    <button type="button">
      <CreatorCredits
        attributions={[localCreator]}
        resourceKind="recipe"
        variant="compact"
        currentUserID="reader-1"
      />
    </button>,
  );
  expect(screen.getByRole("button").querySelector("a")).toBeNull();
  expect(screen.getByText("Avery Cook")).toBeTruthy();
});
