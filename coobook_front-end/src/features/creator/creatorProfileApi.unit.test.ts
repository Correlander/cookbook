import { afterEach, expect, it, vi } from "vitest";
import {
  getMyCreatorProfile,
  isCreatorProfileEnvelope,
  listCreatorCourses,
} from "./creatorProfileApi.ts";

afterEach(() => vi.unstubAllGlobals());

it("recognizes only the frozen self-profile envelope and requests it privately", async () => {
  const profile = {
    id: "a".repeat(32),
    public_name: "Cook",
    bio: "",
    visibility: "private",
    created_at: "now",
    updated_at: "now",
  };
  expect(isCreatorProfileEnvelope({ profile })).toBe(true);
  expect(
    isCreatorProfileEnvelope({ profile: { ...profile, account_id: "leak" } }),
  ).toBe(false);
  expect(
    isCreatorProfileEnvelope({ profile: { ...profile, visibility: "public" } }),
  ).toBe(false);
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify({ profile }), { status: 200 }),
    );
  vi.stubGlobal("fetch", fetchMock);
  await expect(getMyCreatorProfile()).resolves.toEqual({ profile });
  expect(fetchMock).toHaveBeenCalledWith("/api/account/creator-profile", {
    cache: "no-store",
  });
});

it("preserves unsupported status for an explicit legacy-create choice", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response("{}", { status: 404 })),
  );
  await expect(getMyCreatorProfile()).rejects.toMatchObject({ status: 404 });
});

it("does not treat a malformed 200 body as typed-profile support", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({
            profile: {
              id: "p1",
              public_name: "Cook",
              bio: "",
              visibility: "private",
              created_at: "now",
              updated_at: "now",
              account_id: "must-not-be-present",
            },
          }),
          { status: 200 },
        ),
      ),
  );
  await expect(getMyCreatorProfile()).rejects.toMatchObject({
    status: 502,
    code: "invalid_creator_profile_response",
  });
});

it("requests and strictly parses the minimal paged course attribution DTO", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        profile: {
          id: "p1",
          public_name: "Cook",
          bio: "",
          visibility: "instance",
          created_at: "now",
          updated_at: "now",
        },
        items: [
          {
            course: { id: "c1", title: "Bread" },
            role: "creator",
            provenance: "self_asserted_at_creation",
          },
        ],
        total: 25,
        limit: 24,
        offset: 24,
      }),
      { status: 200 },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  await expect(listCreatorCourses("p/1", 24)).resolves.toMatchObject({
    total: 25,
    limit: 24,
    offset: 24,
    items: [{ course: { id: "c1", title: "Bread" } }],
  });
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/creator-profiles/p%2F1/courses?limit=24&offset=24",
    { cache: "no-store" },
  );
});

it("rejects extra or malformed course DTO fields and preserves an older API 404", async () => {
  const base = {
    profile: {
      id: "p1",
      public_name: "Cook",
      bio: "",
      visibility: "instance",
      created_at: "now",
      updated_at: "now",
    },
    items: [
      {
        course: { id: "c1", title: "Bread" },
        role: "creator",
        provenance: "self_asserted_at_creation",
      },
    ],
    total: 1,
    limit: 24,
    offset: 0,
  };
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ...base,
          items: [
            {
              ...base.items[0],
              course: { ...base.items[0].course, owner_id: "leak" },
            },
          ],
        }),
        { status: 200 },
      ),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ error: "not_found" }), { status: 404 }),
    );
  vi.stubGlobal("fetch", fetchMock);
  await expect(listCreatorCourses("p1")).rejects.toMatchObject({
    status: 502,
    code: "invalid_creator_profile_response",
  });
  await expect(listCreatorCourses("p1")).rejects.toMatchObject({
    status: 404,
    code: "not_found",
  });
});
