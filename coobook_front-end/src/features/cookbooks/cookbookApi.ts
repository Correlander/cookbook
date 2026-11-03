import type {
  Cookbook,
  CookbookInvitation,
  CookbookMember,
  PublicCookbook,
  PublicCookbookRecipe,
  Recipe,
} from "../../apiModels";

export const COOKBOOK_PAGE_SIZE = 24;

export interface CookbookRecipeQuery {
  offset: number;
  sort: "recent" | "oldest" | "title";
  query: string;
  tag: string;
}

export interface CookbookWriteInput {
  title: string;
  description: string;
  visibility: Cookbook["visibility"];
}

async function readJson<T>(
  url: string,
  failure: string | ((response: Response) => string),
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    throw new Error(typeof failure === "string" ? failure : failure(response));
  }
  return response.json() as Promise<T>;
}

async function send(
  url: string,
  failure: string,
  init: RequestInit,
): Promise<void> {
  const response = await fetch(url, init);
  if (!response.ok) throw new Error(failure);
}

export function listCookbooks(offset: number, failure: string) {
  return readJson<Cookbook[]>(
    `/api/cookbooks?limit=${COOKBOOK_PAGE_SIZE}&offset=${offset}`,
    failure,
  );
}

export function listPublicCookbooks(
  query: string,
  offset: number,
  failure: string,
) {
  const params = new URLSearchParams({
    limit: String(COOKBOOK_PAGE_SIZE),
    offset: String(offset),
  });
  if (query.trim()) params.set("q", query.trim());
  return readJson<PublicCookbook[]>(`/api/public/cookbooks?${params}`, failure);
}

export function listCookbookRecipes(
  cookbookID: string,
  filters: CookbookRecipeQuery,
  failure: string | ((response: Response) => string),
) {
  const params = new URLSearchParams({
    cookbook_id: cookbookID,
    limit: String(COOKBOOK_PAGE_SIZE),
    offset: String(filters.offset),
    sort: filters.sort,
  });
  if (filters.query.trim()) params.set("q", filters.query.trim());
  if (filters.tag.trim()) params.set("tag", filters.tag.trim());
  return readJson<Recipe[]>(`/api/recipes?${params}`, failure);
}

export function listPublicCookbookRecipes(
  cookbookID: string,
  filters: CookbookRecipeQuery,
  failure: string,
) {
  const params = new URLSearchParams({
    limit: String(COOKBOOK_PAGE_SIZE),
    offset: String(filters.offset),
    sort: filters.sort,
  });
  if (filters.query.trim()) params.set("q", filters.query.trim());
  if (filters.tag.trim()) params.set("tag", filters.tag.trim());
  return readJson<PublicCookbookRecipe[]>(
    `/api/public/cookbooks/${encodeURIComponent(cookbookID)}/recipes?${params}`,
    failure,
  );
}

export function saveCookbook(
  book: Cookbook | undefined,
  input: CookbookWriteInput,
) {
  return readJson<Cookbook>(
    book ? `/api/cookbooks/${book.id}` : "/api/cookbooks",
    (response) =>
      response.status === 403
        ? "This instance does not allow that visibility or you cannot edit this cookbook."
        : "Could not save this cookbook. Try again.",
    {
      method: book ? "PATCH" : "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: input.title,
        description: input.description,
        visibility: book ? input.visibility : "private",
      }),
    },
  );
}

export interface CookbookMembersData {
  members: CookbookMember[];
  invitations: CookbookInvitation[];
}

export async function getCookbookMembers(
  cookbookID: string,
): Promise<CookbookMembersData> {
  const [membersResponse, inviteResponse] = await Promise.all([
    fetch(`/api/cookbooks/${cookbookID}/members`),
    fetch(`/api/cookbooks/${cookbookID}/members/invitations`),
  ]);
  if (!membersResponse.ok || !inviteResponse.ok) {
    throw new Error("Could not load cookbook members.");
  }
  const [members, invitations] = await Promise.all([
    membersResponse.json() as Promise<CookbookMember[]>,
    inviteResponse.json() as Promise<CookbookInvitation[]>,
  ]);
  return { members, invitations };
}

export function createCookbookInvitation(
  cookbookID: string,
  email: string,
  role: CookbookInvitation["role"],
) {
  return readJson<CookbookInvitation & { token: string }>(
    `/api/cookbooks/${cookbookID}/members/invitations`,
    "Could not invite this person.",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, role }),
    },
  );
}

export async function updateCookbookMember(
  cookbookID: string,
  userID: string,
  role: CookbookInvitation["role"],
) {
  await send(
    `/api/cookbooks/${cookbookID}/members`,
    "Could not update this member.",
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user_id: userID, role }),
    },
  );
}

export async function removeCookbookMember(cookbookID: string, userID: string) {
  await send(
    `/api/cookbooks/${cookbookID}/members?user_id=${encodeURIComponent(userID)}`,
    "Could not remove this member.",
    { method: "DELETE" },
  );
}

export async function revokeCookbookInvitation(cookbookID: string, id: string) {
  await send(
    `/api/cookbooks/${cookbookID}/members/invitations?id=${encodeURIComponent(id)}`,
    "Could not revoke this invitation.",
    { method: "DELETE" },
  );
}
