import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CourseForm from "./CourseForm.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function configResponse(capability?: unknown, includeCapability = true): Response {
  const body: Record<string, unknown> = {
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: false,
    courses_enabled: true,
  };
  if (includeCapability) body.course_creator_attribution = capability;
  return new Response(JSON.stringify(body), { status: 200 });
}

function installFetch(capability: unknown, includeCapability = true) {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    return Promise.resolve(url === "/api/public/config"
      ? configResponse(capability, includeCapability)
      : new Response("{}", { status: 201 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

it("offers and sends an explicit creator choice only when the capability is literal true", async () => {
  const calls = installFetch(true);
  const saved = vi.fn(async () => {});
  render(<CourseForm course={null} onClose={vi.fn()} onSaved={saved} />);
  const choice = await screen.findByRole("combobox", { name: "Creator credit" });
  fireEvent.change(choice, { target: { value: "name_claim" } });
  fireEvent.change(screen.getByLabelText("Creator name (unverified)"), { target: { value: "  Alex Baker  " } });
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Kitchen basics" } });
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  const post = calls.find((call) => call.url === "/api/courses" && call.init?.method === "POST");
  expect(JSON.parse(String(post?.init?.body))).toMatchObject({ creator_attribution: { kind: "name_claim", name: "Alex Baker" } });
  expect(calls.some((call) => call.url === "/api/account/creator-profile")).toBe(false);
});

it.each([
  ["local_profile", { kind: "local_profile" }],
  ["unknown", { kind: "unknown" }],
])("sends the explicit %s choice without inventing a name claim", async (choice, expected) => {
  const calls = installFetch(true);
  const saved = vi.fn(async () => {});
  render(<CourseForm course={null} onClose={vi.fn()} onSaved={saved} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Kitchen basics" } });
  fireEvent.change(await screen.findByRole("combobox", { name: "Creator credit" }), { target: { value: choice } });
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  const post = calls.find((call) => call.url === "/api/courses" && call.init?.method === "POST");
  expect(JSON.parse(String(post?.init?.body))).toMatchObject({ creator_attribution: expected });
});

it.each([
  ["absent", undefined, false],
  ["false", false, true],
  ["malformed", "true", true],
])("keeps the legacy no-claim request when capability is %s", async (_label, capability, includeCapability) => {
  const calls = installFetch(capability, includeCapability);
  const saved = vi.fn(async () => {});
  render(<CourseForm course={null} onClose={vi.fn()} onSaved={saved} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Kitchen basics" } });
  await waitFor(() => expect(screen.getByRole("button", { name: "Save course" }).hasAttribute("disabled")).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  const post = calls.find((call) => call.url === "/api/courses" && call.init?.method === "POST");
  expect(JSON.parse(String(post?.init?.body))).not.toHaveProperty("creator_attribution");
  expect(screen.queryByRole("combobox", { name: "Creator credit" })).toBeNull();
});

it("does not show creator controls or rewrite credits while editing even when supported", async () => {
  const calls = installFetch(true);
  const saved = vi.fn(async () => {});
  render(<CourseForm course={{ id: "c1", owner_id: "owner", owner_name: "Custodian", title: "Kitchen", description: "", visibility: "private", lesson_count: 0, created_at: "then", updated_at: "then", creator_name: "Original claim" }} onClose={vi.fn()} onSaved={saved} />);
  await waitFor(() => expect(calls.some((call) => call.url === "/api/public/config")).toBe(true));
  expect(screen.queryByRole("combobox", { name: "Creator credit" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  const patch = calls.find((call) => call.url === "/api/courses/c1" && call.init?.method === "PATCH");
  expect(JSON.parse(String(patch?.init?.body))).not.toHaveProperty("creator_attribution");
});

it.each([
  ["create", null],
  ["metadata edit", { id: "course-1", owner_id: "owner", owner_name: "Owner", title: "Old title", description: "Old text", visibility: "private" as const, lesson_count: 2, created_at: "then", updated_at: "then" }],
])("retains public course %s draft after publisher requirement and waits for resubmit", async (_label, course) => {
  let writes = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/public/config") return Promise.resolve(configResponse(false));
    if ((url === "/api/courses" || url === "/api/courses/course-1") && init?.method) {
      writes += 1;
      return Promise.resolve(new Response(JSON.stringify({ error: "publisher_profile_required" }), { status: 409 }));
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CourseForm course={course} onClose={vi.fn()} onSaved={vi.fn(async () => {})} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Revised public course" } });
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Keep these details" } });
  fireEvent.change(screen.getByLabelText("Visibility"), { target: { value: "public" } });
  await waitFor(() => expect((screen.getByRole("button", { name: "Save course" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));

  expect(await screen.findByRole("link", { name: /opens in a new tab/i })).toBeTruthy();
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Revised public course");
  expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("Keep these details");
  expect((screen.getByLabelText("Visibility") as HTMLSelectElement).value).toBe("public");
  expect(writes).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "Save course" }));
  await waitFor(() => expect(writes).toBe(2));
});
