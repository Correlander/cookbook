import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ReactNode } from "react";
import type { UserSummary } from "../apiModels";

interface AppSessionValue {
  currentUser: UserSummary | null;
  loading: boolean;
  refreshSession: () => Promise<UserSummary | null>;
  clearSession: () => void;
}

const AppSessionContext = createContext<AppSessionValue | null>(null);

export function AppSessionProvider({ children }: { children: ReactNode }) {
  const [currentUser, setCurrentUser] = useState<UserSummary | null>(null);
  const [loading, setLoading] = useState(true);
  const requestSequence = useRef(0);
  const mounted = useRef(false);

  const refreshSession = useCallback(async () => {
    const request = ++requestSequence.current;
    try {
      const response = await fetch("/api/auth/me");
      const user = response.ok ? ((await response.json()) as UserSummary) : null;
      if (mounted.current && requestSequence.current === request)
        setCurrentUser(user);
      return user;
    } catch {
      if (mounted.current && requestSequence.current === request)
        setCurrentUser(null);
      return null;
    } finally {
      if (mounted.current && requestSequence.current === request)
        setLoading(false);
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
    setCurrentUser(null);
    setLoading(false);
  }, []);

  return (
    <AppSessionContext.Provider
      value={{ currentUser, loading, refreshSession, clearSession }}
    >
      {children}
    </AppSessionContext.Provider>
  );
}

export function useAppSession(): AppSessionValue {
  const session = useContext(AppSessionContext);
  if (!session) throw new Error("AppSessionProvider is required.");
  return session;
}
