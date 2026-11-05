import { useCallback, useEffect, useRef, useState } from "react";
import type {
  AnonymousHomePreset,
  InstancePalette,
  PublicAuthConfig,
  PublicBranding,
  RegistrationPolicy,
} from "../../apiModels";

export type PublicAuthConfigState =
  | { status: "loading" }
  | { status: "ready"; config: PublicAuthConfig }
  | { status: "error"; message: string };

function trimUnicodeWhitespace(value: string): string {
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
}

function normalizedBranding(value: unknown): PublicBranding {
  const branding = isRecord(value) && isRecord(value.branding) ? value.branding : {};
  const candidate = typeof branding.site_name === "string"
    ? trimUnicodeWhitespace(branding.site_name)
    : "";
  const valid = candidate.length > 0 && Array.from(candidate).length <= 48 && !/\p{Cc}/u.test(candidate);
  const palette: InstancePalette = isRecord(branding) &&
    (branding.palette === "garden" || branding.palette === "plum")
    ? branding.palette
    : "garden";
  return {
    site_name: valid ? candidate : "Cookbook",
    palette,
  };
}

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

function publicAuthConfig(value: unknown): PublicAuthConfig {
  const config = value as PublicAuthConfig;
  const preset: AnonymousHomePreset =
    isRecord(value) && config.anonymous_home_preset === "discovery_first"
      ? "discovery_first"
      : "library_first";
  return {
    ...config,
    anonymous_home_preset: preset,
    // Capability negotiation is deliberately fail-closed and optional for older APIs.
    course_creator_attribution: isRecord(value) && value.course_creator_attribution === true,
    // This is protocol support, not an instance policy toggle. Only literal true enables menu UI.
    menu_api: isRecord(value) && value.menu_api === true,
    branding: normalizedBranding(value),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export default function usePublicAuthConfig({ enabled = true }: { enabled?: boolean } = {}): {
  state: PublicAuthConfigState;
  retry: () => void;
} {
  const requestID = useRef(0);
  const [state, setState] = useState<PublicAuthConfigState>({
    status: "loading",
  });

  const load = useCallback(async () => {
    if (!enabled) return;
    const currentRequest = ++requestID.current;
    setState({ status: "loading" });
    try {
      const response = await fetch("/api/public/config", { cache: "no-store" });
      if (!response.ok) throw new Error("Could not load sign-in options.");
      const value: unknown = await response.json();
      if (!isPublicAuthConfig(value))
        throw new Error("Could not load sign-in options.");
      if (requestID.current === currentRequest)
        setState({ status: "ready", config: publicAuthConfig(value) });
    } catch {
      if (requestID.current === currentRequest)
        setState({
          status: "error",
          message: "Could not load sign-in options. Try again.",
        });
    }
  }, [enabled]);

  useEffect(() => {
    if (!enabled) {
      requestID.current += 1;
      return undefined;
    }
    void load();
    return () => {
      requestID.current += 1;
    };
  }, [enabled, load]);

  return { state, retry: () => void load() };
}
