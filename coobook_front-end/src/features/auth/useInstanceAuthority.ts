import { useCallback, useEffect, useRef, useState } from "react";
import type { InstanceAuthority } from "../../apiModels";

export type InstanceAuthorityState =
  | { status: "idle" }
  | { status: "loading"; userID: string }
  | { status: "ready"; userID: string; authority: InstanceAuthority }
  | { status: "error"; userID: string; message: string };

const INSTANCE_AUTHORITY_ERROR = "Could not load instance permissions. Try again.";

function isInstanceAuthority(value: unknown): value is InstanceAuthority {
  if (!value || typeof value !== "object") return false;
  const authority = value as Partial<InstanceAuthority>;
  return (
    typeof authority.is_instance_owner === "boolean" &&
    typeof authority.is_admin === "boolean" &&
    typeof authority.owner_claimed === "boolean" &&
    typeof authority.can_manage_instance_roles === "boolean" &&
    (authority.can_manage_instance_settings === undefined ||
      typeof authority.can_manage_instance_settings === "boolean")
  );
}

export default function useInstanceAuthority(
  userID: string | null,
  authenticated: boolean,
): { state: InstanceAuthorityState; retry: () => void } {
  const requestSequence = useRef(0);
  const [storedState, setStoredState] = useState<InstanceAuthorityState>({
    status: "idle",
  });
  const enabled = authenticated && Boolean(userID);

  const load = useCallback(async () => {
    if (!enabled || !userID) return;
    const request = ++requestSequence.current;
    const isCurrent = () => requestSequence.current === request;
    setStoredState({ status: "loading", userID });
    try {
      const response = await fetch("/api/instance/authority", {
        cache: "no-store",
      });
      if (!response.ok) throw new Error(INSTANCE_AUTHORITY_ERROR);
      const value: unknown = await response.json();
      if (!isInstanceAuthority(value))
        throw new Error(INSTANCE_AUTHORITY_ERROR);
      if (isCurrent())
        setStoredState({ status: "ready", userID, authority: value });
    } catch {
      if (isCurrent())
        setStoredState({
          status: "error",
          userID,
          message: INSTANCE_AUTHORITY_ERROR,
        });
    }
  }, [enabled, userID]);

  useEffect(() => {
    if (!enabled || !userID) {
      requestSequence.current += 1;
      setStoredState({ status: "idle" });
      return;
    }
    void load();
    return () => {
      requestSequence.current += 1;
    };
  }, [enabled, load, userID]);

  const state =
    !enabled || !userID
      ? ({ status: "idle" } as const)
      : storedState.status !== "idle" && storedState.userID === userID
        ? storedState
        : ({ status: "loading", userID } as const);

  return { state, retry: () => void load() };
}
