import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ReactNode } from "react";
import type { AuthenticatedUser } from "../apiModels";

export type AppSessionStatus =
  | "loading"
  | "authenticated"
  | "anonymous"
  | "error";

export type AppSessionState =
  | { status: "loading"; currentUser: AuthenticatedUser | null }
  | { status: "authenticated"; currentUser: AuthenticatedUser }
  | { status: "anonymous"; currentUser: null }
  | {
      status: "error";
      currentUser: AuthenticatedUser | null;
      message: string;
    };

interface AppSessionValue {
  state: AppSessionState;
  status: AppSessionStatus;
  currentUser: AuthenticatedUser | null;
  loading: boolean;
  errorMessage: string | null;
  refreshSession: () => Promise<AuthenticatedUser | null>;
  clearSession: () => void;
}

const SESSION_CHECK_ERROR = "Could not check your session. Try again.";
const AppSessionContext = createContext<AppSessionValue | null>(null);

export function AppSessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AppSessionState>({
    status: "loading",
    currentUser: null,
  });
  const requestSequence = useRef(0);
  const currentUserRef = useRef<AuthenticatedUser | null>(null);
  const mounted = useRef(false);

  const refreshSession = useCallback(async () => {
    const request = ++requestSequence.current;
    setState({ status: "loading", currentUser: currentUserRef.current });
    const isCurrent = () =>
      mounted.current && requestSequence.current === request;

    try {
      const response = await fetch("/api/auth/me", { cache: "no-store" });
      if (!isCurrent()) return null;
      if (response.status === 401) {
        currentUserRef.current = null;
        setState({ status: "anonymous", currentUser: null });
        return null;
      }
      if (!response.ok) {
        setState({
          status: "error",
          currentUser: currentUserRef.current,
          message: SESSION_CHECK_ERROR,
        });
        return null;
      }

      const user = (await response.json()) as AuthenticatedUser;
      if (!isCurrent()) return null;
      currentUserRef.current = user;
      setState({ status: "authenticated", currentUser: user });
      return user;
    } catch {
      if (isCurrent())
        setState({
          status: "error",
          currentUser: currentUserRef.current,
          message: SESSION_CHECK_ERROR,
        });
      return null;
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refreshSession();
    return () => {
      mounted.current = false;
      requestSequence.current += 1;
    };
  }, [refreshSession]);

  const clearSession = useCallback(() => {
    requestSequence.current += 1;
    currentUserRef.current = null;
    setState({ status: "anonymous", currentUser: null });
  }, []);

  const errorMessage = state.status === "error" ? state.message : null;
  return (
    <AppSessionContext.Provider
      value={{
        state,
        status: state.status,
        currentUser: state.currentUser,
        loading: state.status === "loading",
        errorMessage,
        refreshSession,
        clearSession,
      }}
    >
      {children}
    </AppSessionContext.Provider>
  );
}

export function SessionCheckError({
  message = SESSION_CHECK_ERROR,
  onRetry,
}: {
  message?: string;
  onRetry: () => void;
}) {
  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="session-check-error-title"
    >
      <h2 id="session-check-error-title">Could not check your session</h2>
      <p role="alert">{message}</p>
      <button
        className="button button--green"
        type="button"
        onClick={onRetry}
      >
        Retry session check
      </button>
    </section>
  );
}

export function useAppSession(): AppSessionValue {
  const session = useContext(AppSessionContext);
  if (!session) throw new Error("AppSessionProvider is required.");
  return session;
}
