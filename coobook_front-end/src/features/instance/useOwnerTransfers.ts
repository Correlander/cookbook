import { useCallback, useEffect, useRef, useState } from "react";
import { readOwnerTransfers } from "./ownerTransferApi.ts";
import type { OwnerTransfer } from "./ownerTransferApi.ts";

export type OwnerTransfersState =
  | { status: "idle" }
  | { status: "loading"; userID: string }
  | { status: "ready"; userID: string; transfers: OwnerTransfer[] }
  | { status: "error"; userID: string; message: string };

export default function useOwnerTransfers(
  userID: string | null,
  authenticated: boolean,
  refreshOnFocus = false,
): {
  state: OwnerTransfersState;
  refresh: () => Promise<OwnerTransfer[] | null>;
} {
  const requestSequence = useRef(0);
  const [storedState, setStoredState] = useState<OwnerTransfersState>({
    status: "idle",
  });
  const enabled = authenticated && Boolean(userID);

  const refresh = useCallback(async () => {
    if (!enabled || !userID) return null;
    const requestID = ++requestSequence.current;
    const isCurrent = () => requestSequence.current === requestID;
    setStoredState({ status: "loading", userID });
    try {
      const transfers = await readOwnerTransfers();
      if (isCurrent()) setStoredState({ status: "ready", userID, transfers });
      return isCurrent() ? transfers : null;
    } catch {
      if (isCurrent()) {
        setStoredState({
          status: "error",
          userID,
          message: "Could not check instance ownership requests. Try again.",
        });
      }
      return null;
    }
  }, [enabled, userID]);

  useEffect(() => {
    if (!enabled || !userID) {
      requestSequence.current += 1;
      setStoredState({ status: "idle" });
      return;
    }
    void refresh();
    if (!refreshOnFocus) return;
    const handleFocus = () => void refresh();
    window.addEventListener("focus", handleFocus);
    return () => {
      window.removeEventListener("focus", handleFocus);
      requestSequence.current += 1;
    };
  }, [enabled, refresh, refreshOnFocus, userID]);

  const state = !enabled || !userID
    ? ({ status: "idle" } as const)
    : storedState.status !== "idle" && storedState.userID === userID
      ? storedState
      : ({ status: "loading", userID } as const);

  return { state, refresh };
}
