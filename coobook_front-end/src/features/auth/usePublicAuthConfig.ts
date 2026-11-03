import { useCallback, useEffect, useRef, useState } from "react";
import type { PublicAuthConfig, RegistrationPolicy } from "../../apiModels";

export type PublicAuthConfigState =
  | { status: "loading" }
  | { status: "ready"; config: PublicAuthConfig }
  | { status: "error"; message: string };

function isPublicAuthConfig(value: unknown): value is PublicAuthConfig {
  if (!value || typeof value !== "object") return false;
  const config = value as Partial<PublicAuthConfig>;
  const methods = config.auth_methods;
  const policies: RegistrationPolicy[] = ["invite_only", "open", "disabled"];
  return Boolean(
    methods &&
      typeof methods.email_link === "boolean" &&
      typeof methods.passkey === "boolean" &&
      policies.includes(config.registration_policy as RegistrationPolicy) &&
      typeof config.public_instance === "boolean" &&
      typeof config.courses_enabled === "boolean",
  );
}

export default function usePublicAuthConfig(): {
  state: PublicAuthConfigState;
  retry: () => void;
} {
  const requestID = useRef(0);
  const [state, setState] = useState<PublicAuthConfigState>({
    status: "loading",
  });

  const load = useCallback(async () => {
    const currentRequest = ++requestID.current;
    setState({ status: "loading" });
    try {
      const response = await fetch("/api/public/config");
      if (!response.ok) throw new Error("Could not load sign-in options.");
      const value: unknown = await response.json();
      if (!isPublicAuthConfig(value))
        throw new Error("Could not load sign-in options.");
      if (requestID.current === currentRequest)
        setState({ status: "ready", config: value });
    } catch {
      if (requestID.current === currentRequest)
        setState({
          status: "error",
          message: "Could not load sign-in options. Try again.",
        });
    }
  }, []);

  useEffect(() => {
    void load();
    return () => {
      requestID.current += 1;
    };
  }, [load]);

  return { state, retry: () => void load() };
}
