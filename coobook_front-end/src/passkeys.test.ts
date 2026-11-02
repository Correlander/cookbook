import { describe, expect, it } from "vitest";
import {
  decodeBase64URL,
  encodeBase64URL,
  encodeUserHandle,
  serializeCredential,
  validTransports,
} from "./passkeys.ts";

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
