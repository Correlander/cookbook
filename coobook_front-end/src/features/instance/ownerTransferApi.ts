import type {
  OwnerTransfer,
  OwnerTransferCreatePayload,
} from "../../apiModels";

export type { OwnerTransfer } from "../../apiModels";

export class OwnerTransferApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(code);
    this.name = "OwnerTransferApiError";
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isTransfer(value: unknown): value is OwnerTransfer {
  if (!isRecord(value)) return false;
  return (
    typeof value.id === "string" &&
    (value.direction === "incoming" || value.direction === "outgoing") &&
    value.state === "pending" &&
    typeof value.created_at === "string" &&
    typeof value.expires_at === "string" &&
    typeof value.counterparty_display_name === "string"
  );
}

async function throwResponseError(response: Response): Promise<never> {
  const body: unknown = await response.json().catch(() => null);
  const code = isRecord(body) && typeof body.error === "string"
    ? body.error
    : "request_failed";
  throw new OwnerTransferApiError(response.status, code);
}

export async function readOwnerTransfers(): Promise<OwnerTransfer[]> {
  const response = await fetch("/api/instance/owner-transfers", {
    cache: "no-store",
  });
  if (!response.ok) return throwResponseError(response);
  const body: unknown = await response.json();
  if (!isRecord(body) || !Array.isArray(body.transfers) || !body.transfers.every(isTransfer)) {
    throw new Error("Could not load instance ownership requests.");
  }
  return body.transfers.map((transfer) => ({
    id: transfer.id,
    direction: transfer.direction,
    state: transfer.state,
    created_at: transfer.created_at,
    expires_at: transfer.expires_at,
    counterparty_display_name: transfer.counterparty_display_name,
  }));
}

export async function createOwnerTransfer(targetUserID: string): Promise<void> {
  const response = await fetch("/api/instance/owner-transfers", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target_user_id: targetUserID } satisfies OwnerTransferCreatePayload),
  });
  if (!response.ok) return throwResponseError(response);
}

export async function acceptOwnerTransfer(transferID: string): Promise<void> {
  const response = await fetch(
    `/api/instance/owner-transfers/${encodeURIComponent(transferID)}/accept`,
    { method: "POST" },
  );
  if (!response.ok) return throwResponseError(response);
}

export async function cancelOwnerTransfer(transferID: string): Promise<void> {
  const response = await fetch(
    `/api/instance/owner-transfers/${encodeURIComponent(transferID)}`,
    { method: "DELETE" },
  );
  if (!response.ok) return throwResponseError(response);
}
