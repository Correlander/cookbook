import { afterEach, describe, expect, it, vi } from "vitest";
import {
  decodeBase64URL,
  enrollPasskeyWithGrant,
  encodeBase64URL,
  encodeUserHandle,
  serializeCredential,
  validTransports,
} from "./passkeys.ts";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("WebAuthn byte encoding", () => {
  it("decodes base64url challenges and credential IDs to exact bytes", () => {
    expect([...new Uint8Array(decodeBase64URL("AQID"))]).toEqual([1, 2, 3]);
    expect([...new Uint8Array(decodeBase64URL("Bwg"))]).toEqual([7, 8]);
    expect(encodeBase64URL(new Uint8Array([251, 255]).buffer)).toBe("-_8");
  });

  it("encodes the literal registration user ID as UTF-8", () => {
    expect([...new Uint8Array(encodeUserHandle("cookbook-user-42"))]).toEqual([
      ...new TextEncoder().encode("cookbook-user-42"),
    ]);
  });

  it("serializes an assertion finish payload with base64url fields and nullable userHandle", () => {
    const response = {
      clientDataJSON: new Uint8Array([123, 34]).buffer,
      authenticatorData: new Uint8Array([1, 2]).buffer,
      signature: new Uint8Array([3, 4]).buffer,
      userHandle: null,
    } as AuthenticatorAssertionResponse;
    const credential = {
      id: "credential-id",
      rawId: new Uint8Array([5, 6]).buffer,
      type: "public-key",
      authenticatorAttachment: "platform",
      response,
      getClientExtensionResults: () => ({}),
    } as unknown as PublicKeyCredential;

    expect(serializeCredential(credential, false)).toEqual({
      id: "credential-id",
      rawId: "BQY",
      type: "public-key",
      authenticatorAttachment: "platform",
      clientExtensionResults: {},
      response: {
        clientDataJSON: "eyI",
        authenticatorData: "AQI",
        signature: "AwQ",
        userHandle: null,
      },
    });
  });

  it("serializes registration payloads when optional getTransports is unavailable", () => {
    const response = {
      clientDataJSON: new Uint8Array([123, 34]).buffer,
      attestationObject: new Uint8Array([9, 10]).buffer,
    } as AuthenticatorAttestationResponse;
    const credential = {
      id: "registration-id",
      rawId: new Uint8Array([11, 12]).buffer,
      type: "public-key" as const,
      authenticatorAttachment: null,
      response,
      getClientExtensionResults: () => ({}),
      toJSON: () => ({
        id: "",
        rawId: "",
        type: "public-key",
        response: { clientDataJSON: "", attestationObject: "" },
        authenticatorAttachment: null,
        clientExtensionResults: {},
      }),
    } as unknown as PublicKeyCredential;

    expect(serializeCredential(credential, true)).toEqual({
      id: "registration-id",
      rawId: "Cww",
      type: "public-key",
      authenticatorAttachment: null,
      clientExtensionResults: {},
      response: { clientDataJSON: "eyI", attestationObject: "CQo" },
    });
  });
});

describe("passkey authenticator transports", () => {
  it("preserves supported WebAuthn transport names in order", () => {
    expect(validTransports(["internal", "usb", "ble"])).toEqual([
      "internal",
      "usb",
      "ble",
    ]);
  });

  it("drops unsupported values and keeps an empty list empty", () => {
    expect(validTransports(["usb", "invalid", "smart-card"])).toEqual(["usb"]);
    expect(validTransports([])).toEqual([]);
    expect(validTransports(undefined)).toBeUndefined();
  });
});

describe("offline passkey enrollment", () => {
  it("posts the grant only in request bodies and preserves server WebAuthn options", async () => {
    const secureContextDescriptor = Object.getOwnPropertyDescriptor(
      window,
      "isSecureContext",
    );
    const publicKeyDescriptor = Object.getOwnPropertyDescriptor(
      window,
      "PublicKeyCredential",
    );
    const credentialsDescriptor = Object.getOwnPropertyDescriptor(
      navigator,
      "credentials",
    );
    Object.defineProperty(window, "isSecureContext", {
      configurable: true,
      value: true,
    });
    Object.defineProperty(window, "PublicKeyCredential", {
      configurable: true,
      value: class PublicKeyCredential {},
    });

    const credential = {
      id: "credential-id",
      rawId: new Uint8Array([9, 8, 7]).buffer,
      type: "public-key",
      authenticatorAttachment: "platform",
      response: {
        clientDataJSON: new Uint8Array([1, 2]).buffer,
        attestationObject: new Uint8Array([3, 4]).buffer,
      },
      getClientExtensionResults: () => ({}),
    } as unknown as PublicKeyCredential;
    const create = vi.fn().mockResolvedValue(credential);
    Object.defineProperty(navigator, "credentials", {
      configurable: true,
      value: { create },
    });

    const begin = {
      ceremony_id: "0123456789abcdef0123456789abcdef",
      options: {
        publicKey: {
          challenge: "AQID",
          rp: { name: "Cookbook", id: "localhost" },
          user: {
            id: "account-id",
            name: "cookbook",
            displayName: "Cookbook",
          },
          pubKeyCredParams: [{ type: "public-key", alg: -7 }],
          authenticatorSelection: { userVerification: "required" },
        },
      },
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => begin,
      } as Response)
      .mockResolvedValueOnce({ ok: true } as Response);
    vi.stubGlobal("fetch", fetchMock);

    try {
      const controller = new AbortController();
      await enrollPasskeyWithGrant("operator-grant-secret", controller.signal);

      expect(fetchMock).toHaveBeenCalledTimes(2);
      const [beginPath, beginRequest] = fetchMock.mock.calls[0] as [
        string,
        RequestInit,
      ];
      expect(beginPath).toBe("/api/auth/passkey/enrollment/begin");
      expect(beginRequest).toMatchObject({
        method: "POST",
        credentials: "omit",
        cache: "no-store",
        body: JSON.stringify({ token: "operator-grant-secret" }),
        signal: controller.signal,
      });
      expect(beginRequest.headers).toEqual({ "Content-Type": "application/json" });
      expect(beginPath).not.toContain("operator-grant-secret");

      const [finishPath, finishRequest] = fetchMock.mock.calls[1] as [
        string,
        RequestInit,
      ];
      expect(finishPath).toBe("/api/auth/passkey/enrollment/finish");
      expect(finishRequest).toMatchObject({
        method: "POST",
        credentials: "omit",
        cache: "no-store",
        signal: controller.signal,
      });
      expect(finishRequest.headers).toEqual({ "Content-Type": "application/json" });
      expect(finishPath).not.toContain("operator-grant-secret");
      expect(JSON.parse(String(finishRequest.body))).toEqual({
        token: "operator-grant-secret",
        ceremony_id: begin.ceremony_id,
        id: "credential-id",
        rawId: "CQgH",
        type: "public-key",
        authenticatorAttachment: "platform",
        clientExtensionResults: {},
        response: { clientDataJSON: "AQI", attestationObject: "AwQ" },
      });

      const creation = create.mock.calls[0]?.[0] as CredentialCreationOptions & {
        publicKey: PublicKeyCredentialCreationOptions;
      };
      expect([
        ...new Uint8Array(creation.publicKey.challenge as ArrayBuffer),
      ]).toEqual([1, 2, 3]);
      expect(creation.publicKey.rp.id).toBe("localhost");
      expect(creation.publicKey.authenticatorSelection?.userVerification).toBe(
        "required",
      );
      expect(creation.signal).toBe(controller.signal);
    } finally {
      if (secureContextDescriptor) {
        Object.defineProperty(window, "isSecureContext", secureContextDescriptor);
      } else {
        Reflect.deleteProperty(window, "isSecureContext");
      }
      if (publicKeyDescriptor) {
        Object.defineProperty(window, "PublicKeyCredential", publicKeyDescriptor);
      } else {
        Reflect.deleteProperty(window, "PublicKeyCredential");
      }
      if (credentialsDescriptor) {
        Object.defineProperty(navigator, "credentials", credentialsDescriptor);
      } else {
        Reflect.deleteProperty(navigator, "credentials");
      }
    }
  });
});
