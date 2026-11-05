import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import RecipeForm from "./RecipeForm.tsx";
import type { Recipe } from "../../apiModels";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const user = {
  id: "u1",
  email: "cook@example.test",
  display_name: "Cook",
  is_admin: false,
};
const profile = {
  id: "p1",
  public_name: "",
  bio: "",
  visibility: "private",
  created_at: "",
  updated_at: "",
};
const recipe: Recipe = {
  id: "r1",
  owner_id: "u1",
  creator_name: "Legacy author",
  title: "Soup",
  description: "",
  ingredients: [{ name: "Tomato", quantity: "1", notes: "" }],
  steps: [{ text: "Cook." }],
  servings: 1,
  prep_time_minutes: 1,
  cook_time_minutes: 1,
  tags: [],
  visibility: "private",
  can_edit: true,
  can_delete: false,
  version: 1,
  cookbook_ids: [],
  created_at: "",
  updated_at: "",
};
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });

function mount(
  fetchMock: ReturnType<typeof vi.fn>,
  existing: Recipe | null = null,
  actor = user,
) {
  vi.stubGlobal("fetch", fetchMock);
  return render(
    <RecipeForm
      user={actor}
      recipe={existing}
      onClose={vi.fn()}
      onCreated={vi.fn()}
      onUpdated={vi.fn()}
      onSignedOut={vi.fn()}
    />,
  );
}

async function completeRequiredFields() {
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Soup" },
  });
  fireEvent.change(
    screen.getByLabelText("Ingredients (name | quantity | notes per line)"),
    { target: { value: "Tomato | 1" } },
  );
  fireEvent.change(await screen.findByLabelText("Step 1"), {
    target: { value: "Cook." },
  });
}

for (const [choice, expected] of [
  ["local_profile", { kind: "local_profile" }],
  ["name_claim", { kind: "name_claim", name: "Aunt May" }],
  ["unknown", { kind: "unknown" }],
] as const) {
  it(`sends the explicit ${choice} create attribution`, async () => {
    let payload: Record<string, unknown> | undefined;
    const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
      if (String(url) === "/api/account/creator-profile")
        return Promise.resolve(response({ profile: null }));
      if (String(url) === "/api/cookbooks")
        return Promise.resolve(response([]));
      if (String(url) === "/api/recipes" && init?.method === "POST") {
        payload = JSON.parse(String(init.body)) as Record<string, unknown>;
        return Promise.resolve(response(recipe, 201));
      }
      return Promise.resolve(response({}, 404));
    });
    mount(fetchMock);
    await screen.findByRole("combobox", { name: "Creator credit" });
    await completeRequiredFields();
    fireEvent.change(screen.getByLabelText("Creator credit"), {
      target: { value: choice },
    });
    if (choice === "name_claim")
      fireEvent.change(screen.getByLabelText("Creator name (unverified)"), {
        target: { value: "Aunt May" },
      });
    fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
    await waitFor(() => expect(payload).toBeDefined());
    expect(payload?.creator_attribution).toEqual(expected);
  });
}

it("keeps the explicit creator choice and form values after a create failure", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/account/creator-profile")
      return Promise.resolve(response({ profile }));
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes" && init?.method === "POST")
      return Promise.resolve(response({ error: "invalid_recipe" }, 400));
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock);
  await screen.findByRole("combobox", { name: "Creator credit" });
  await completeRequiredFields();
  fireEvent.change(screen.getByLabelText("Creator credit"), {
    target: { value: "local_profile" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(
    (screen.getByLabelText("Creator credit") as HTMLSelectElement).value,
  ).toBe("local_profile");
  expect(
    fetchMock.mock.calls.filter(([url]) => String(url) === "/api/recipes"),
  ).toHaveLength(1);
});

it("allows a public but unpublished recipe draft without a creator profile", async () => {
  let payload: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/account/creator-profile")
      return Promise.resolve(response({ profile: null }));
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes" && init?.method === "POST") {
      payload = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(response(recipe, 201));
    }
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock);
  await screen.findByRole("combobox", { name: "Creator credit" });
  await completeRequiredFields();
  fireEvent.change(screen.getByLabelText("Creator credit"), {
    target: { value: "unknown" },
  });
  fireEvent.change(screen.getByLabelText("Visibility"), {
    target: { value: "public" },
  });
  fireEvent.click(screen.getByLabelText("Anyone can read this recipe on a public instance."));
  fireEvent.click(screen.getByLabelText("This recipe will not appear in anonymous browsing until I publish it."));
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));

  await waitFor(() => expect(payload).toBeDefined());
  expect(payload?.visibility).toBe("public");
  expect(payload?.initial_publication).toMatchObject({
    publication_state: "unpublished",
    federation_mode: "off",
    index_eligible: false,
  });
  expect(
    fetchMock.mock.calls.filter(
      ([url, init]) => String(url) === "/api/recipes" && init?.method === "POST",
    ),
  ).toHaveLength(1);
  expect(screen.queryByRole("link", { name: /creator profile/i })).toBeNull();
});

it("retains a public published create after the exact publisher requirement until explicit resubmit", async () => {
  const payloads: Record<string, unknown>[] = [];
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/account/creator-profile")
      return Promise.resolve(response({ profile: null }));
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes" && init?.method === "POST") {
      payloads.push(JSON.parse(String(init.body)) as Record<string, unknown>);
      return Promise.resolve(
        payloads.length === 1
          ? response({ error: "publisher_profile_required" }, 409)
          : response(recipe, 201),
      );
    }
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock);
  await screen.findByRole("combobox", { name: "Creator credit" });
  await completeRequiredFields();
  fireEvent.change(screen.getByLabelText("Creator credit"), {
    target: { value: "unknown" },
  });
  fireEvent.change(screen.getByLabelText("Visibility"), {
    target: { value: "public" },
  });
  fireEvent.change(screen.getByLabelText("Initial publication status"), {
    target: { value: "published" },
  });
  fireEvent.click(screen.getByLabelText("Anyone can read this recipe on a public instance."));
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));

  expect(await screen.findByRole("link", { name: /opens in a new tab/i })).toBeTruthy();
  expect(payloads).toHaveLength(1);
  expect(payloads[0]).toMatchObject({
    title: "Soup",
    visibility: "public",
    creator_attribution: { kind: "unknown" },
    initial_publication: {
      publication_state: "published",
      federation_mode: "off",
      index_eligible: false,
    },
  });
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Soup");
  expect((screen.getByLabelText("Visibility") as HTMLSelectElement).value).toBe("public");
  expect((screen.getByLabelText("Initial publication status") as HTMLSelectElement).value).toBe("published");
  expect((screen.getByLabelText("Creator credit") as HTMLSelectElement).value).toBe("unknown");

  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  await waitFor(() => expect(payloads).toHaveLength(2));
  expect(payloads[1]).toEqual(payloads[0]);
});

it("keeps a public edit draft after the exact publisher requirement without retrying", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes/r1" && init?.method === "PUT")
      return Promise.resolve(response({ error: "publisher_profile_required" }, 409));
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock, recipe);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Revised soup" } });
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Keep this draft" } });
  fireEvent.change(screen.getByLabelText("Visibility"), { target: { value: "public" } });
  fireEvent.click(screen.getByLabelText(/anyone can read/i));
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

  expect(await screen.findByRole("link", { name: /opens in a new tab/i })).toBeTruthy();
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Revised soup");
  expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("Keep this draft");
  expect((screen.getByLabelText("Visibility") as HTMLSelectElement).value).toBe("public");
  expect(fetchMock.mock.calls.filter(([url, init]) => String(url) === "/api/recipes/r1" && init?.method === "PUT")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(fetchMock.mock.calls.filter(([url, init]) => String(url) === "/api/recipes/r1" && init?.method === "PUT")).toHaveLength(2));
});

it("does not show setup guidance for a different 409 recipe conflict", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes/r1" && init?.method === "PUT")
      return Promise.resolve(response({ error: "version_conflict" }, 409));
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock, recipe);
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.queryByRole("link", { name: /creator profile/i })).toBeNull();
});

it("retries transient support checks without losing recipe input", async () => {
  let supportChecks = 0;
  let payload: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/account/creator-profile") {
      supportChecks += 1;
      return Promise.resolve(
        supportChecks === 1 ? response({}, 503) : response({ profile: null }),
      );
    }
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes" && init?.method === "POST") {
      payload = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(response(recipe, 201));
    }
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock);
  await screen.findByRole("button", { name: "Retry support check" });
  await completeRequiredFields();
  fireEvent.click(screen.getByRole("button", { name: "Retry support check" }));
  const creatorChoice = await screen.findByRole("combobox", {
    name: "Creator credit",
  });
  expect((creatorChoice as HTMLSelectElement).value).toBe("");
  fireEvent.change(creatorChoice, { target: { value: "unknown" } });
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  await waitFor(() => expect(payload).toBeDefined());
  expect(payload?.title).toBe("Soup");
  expect(payload?.creator_attribution).toEqual({ kind: "unknown" });
  expect(supportChecks).toBe(2);
});

it("offers a deliberate legacy save on old servers without sending an attribution field", async () => {
  let payload: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/account/creator-profile")
      return Promise.resolve(response({ error: "not_found" }, 404));
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes" && init?.method === "POST") {
      payload = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(response(recipe, 201));
    }
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock);
  expect(
    await screen.findByText(
      /older servers may use your account display name as an unverified recipe byline; that byline follows this recipe’s visibility/i,
    ),
  ).toBeTruthy();
  await completeRequiredFields();
  fireEvent.click(
    screen.getByLabelText(
      /I understand the legacy byline disclosure and want to continue/,
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Save recipe" }));
  await waitFor(() => expect(payload).toBeDefined());
  expect(payload).not.toHaveProperty("creator_attribution");
  expect(
    fetchMock.mock.calls.filter(
      ([url]) => String(url) === "/api/account/creator-profile",
    ),
  ).toHaveLength(1);
});

it("clears creator choice, name claim, and recipe input when the actor changes", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    if (String(url) === "/api/account/creator-profile")
      return Promise.resolve(response({ profile }));
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  });
  const { rerender } = mount(fetchMock);
  await screen.findByRole("combobox", { name: "Creator credit" });
  await completeRequiredFields();
  fireEvent.change(screen.getByLabelText("Creator credit"), {
    target: { value: "name_claim" },
  });
  fireEvent.change(screen.getByLabelText("Creator name (unverified)"), {
    target: { value: "A's private claim" },
  });

  rerender(
    <RecipeForm
      user={{ ...user, id: "u2", display_name: "Other cook" }}
      recipe={null}
      onClose={vi.fn()}
      onCreated={vi.fn()}
      onUpdated={vi.fn()}
      onSignedOut={vi.fn()}
    />,
  );

  const creatorChoice = await screen.findByRole("combobox", {
    name: "Creator credit",
  });
  expect((creatorChoice as HTMLSelectElement).value).toBe("");
  expect(screen.queryByLabelText("Creator name (unverified)")).toBeNull();
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("");
});

it("clears legacy byline consent when the actor changes", async () => {
  const fetchMock = vi.fn((url: RequestInfo | URL) =>
    String(url) === "/api/account/creator-profile"
      ? Promise.resolve(response({ error: "not_found" }, 404))
      : String(url) === "/api/cookbooks"
        ? Promise.resolve(response([]))
        : Promise.resolve(response({}, 404)),
  );
  const { rerender } = mount(fetchMock);
  const consent = await screen.findByLabelText(
    /I understand the legacy byline disclosure and want to continue/,
  );
  await completeRequiredFields();
  fireEvent.click(consent);
  expect((consent as HTMLInputElement).checked).toBe(true);

  rerender(
    <RecipeForm
      user={{ ...user, id: "u2", display_name: "Other cook" }}
      recipe={null}
      onClose={vi.fn()}
      onCreated={vi.fn()}
      onUpdated={vi.fn()}
      onSignedOut={vi.fn()}
    />,
  );

  const newConsent = await screen.findByLabelText(
    /I understand the legacy byline disclosure and want to continue/,
  );
  expect((newConsent as HTMLInputElement).checked).toBe(false);
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("");
});

it("ignores creator support from a previous actor", async () => {
  let resolveActorA!: (value: Response) => void;
  const actorAResponse = new Promise<Response>((resolve) => {
    resolveActorA = resolve;
  });
  let supportCalls = 0;
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    if (String(url) === "/api/account/creator-profile") {
      supportCalls += 1;
      return supportCalls === 1
        ? actorAResponse
        : Promise.resolve(response({ error: "temporarily_unavailable" }, 503));
    }
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  });
  const { rerender } = mount(fetchMock);
  await waitFor(() => expect(supportCalls).toBe(1));

  rerender(
    <RecipeForm
      user={{ ...user, id: "u2", display_name: "Other cook" }}
      recipe={null}
      onClose={vi.fn()}
      onCreated={vi.fn()}
      onUpdated={vi.fn()}
      onSignedOut={vi.fn()}
    />,
  );
  expect(await screen.findByRole("button", { name: "Retry support check" })).toBeTruthy();
  await act(async () => {
    resolveActorA(response({ profile: null }));
    await actorAResponse;
  });
  expect(screen.queryByRole("combobox", { name: "Creator credit" })).toBeNull();
  expect(screen.getByRole("button", { name: "Retry support check" })).toBeTruthy();
});

it("omits create-only creator controls and payload on edit", async () => {
  let payload: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes/r1" && init?.method === "PUT") {
      payload = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(response(recipe));
    }
    return Promise.resolve(response({}, 404));
  });
  mount(fetchMock, recipe);
  expect(screen.queryByLabelText("Creator credit")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(payload).toBeDefined());
  expect(payload).not.toHaveProperty("creator_attribution");
  expect(
    fetchMock.mock.calls.some(
      ([url]) => String(url) === "/api/account/creator-profile",
    ),
  ).toBe(false);
});

it("delivers a successful edit update to the same actor", async () => {
  const updatedRecipe = { ...recipe, title: "Updated soup" };
  let payload: Record<string, unknown> | undefined;
  const onUpdated = vi.fn();
  const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === "/api/cookbooks") return Promise.resolve(response([]));
    if (String(url) === "/api/recipes/r1" && init?.method === "PUT") {
      payload = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(response(updatedRecipe));
    }
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(
    <RecipeForm
      user={user}
      recipe={recipe}
      onClose={vi.fn()}
      onCreated={vi.fn()}
      onUpdated={onUpdated}
      onSignedOut={vi.fn()}
    />,
  );
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Updated soup" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

  await waitFor(() => expect(onUpdated).toHaveBeenCalledWith(updatedRecipe));
  expect(payload).not.toHaveProperty("creator_attribution");
});
