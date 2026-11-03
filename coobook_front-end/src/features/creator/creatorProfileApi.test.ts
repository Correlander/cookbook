import { afterEach, expect, it, vi } from "vitest";
import {
  getMyCreatorProfile,
  isCreatorProfileEnvelope,
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
