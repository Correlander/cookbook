interface CredentialDescriptorJSON {
  type: "public-key";
  id: string;
  transports?: string[];
}

interface CreationOptionsJSON extends Omit<
  PublicKeyCredentialCreationOptions,
  "challenge" | "user" | "excludeCredentials"
> {
  challenge: string;
  user: Omit<PublicKeyCredentialUserEntity, "id"> & { id: string };
  excludeCredentials?: CredentialDescriptorJSON[];
}

interface RequestOptionsJSON extends Omit<
  PublicKeyCredentialRequestOptions,
  "challenge" | "allowCredentials"
> {
  challenge: string;
  allowCredentials?: CredentialDescriptorJSON[];
}

interface PasskeyBeginResponse {
  ceremony_id: string;
  options: { publicKey: CreationOptionsJSON | RequestOptionsJSON };
}

export function validTransports(
  values: string[] | undefined,
): AuthenticatorTransport[] | undefined {
  if (!values) return undefined;
  const supported: AuthenticatorTransport[] = [
    "ble",
    "hybrid",
    "internal",
    "nfc",
    "usb",
  ];
  return values.filter((value): value is AuthenticatorTransport =>
    supported.includes(value as AuthenticatorTransport),
  );
}

interface RegistrationResponseJSON {
  clientDataJSON: string;
  attestationObject: string;
  transports?: string[];
}

interface AssertionResponseJSON {
  clientDataJSON: string;
  authenticatorData: string;
  signature: string;
  userHandle: string | null;
}

interface SerializedCredentialBase {
  id: string;
  rawId: string;
  type: "public-key";
  authenticatorAttachment: string | null;
  clientExtensionResults: AuthenticationExtensionsClientOutputs;
}

type SerializedCredential = SerializedCredentialBase &
  (
    { response: RegistrationResponseJSON } | { response: AssertionResponseJSON }
  );

export function decodeBase64URL(value: string): ArrayBuffer {
  const padded = value
    .replace(/-/g, "+")
    .replace(/_/g, "/")
    .padEnd(Math.ceil(value.length / 4) * 4, "=");
  const binary = window.atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes.buffer;
}

export function encodeBase64URL(value: ArrayBuffer): string {
  const bytes = new Uint8Array(value);
  let binary = "";
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return window
    .btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/g, "");
}

export function encodeUserHandle(value: string): ArrayBuffer {
  return new TextEncoder().encode(value).buffer;
}

async function failureMessage(
  response: Response,
  fallback: string,
): Promise<string> {
  const body = (await response
    .clone()
    .json()
    .catch(() => null)) as { error?: string } | null;
  if (body?.error === "reauthentication_required")
    return "Sign in again before changing passkeys.";
  return fallback;
}

function publicKeyCreationOptions(
  options: CreationOptionsJSON,
): PublicKeyCredentialCreationOptions {
  return {
    ...options,
    challenge: decodeBase64URL(options.challenge),
    user: { ...options.user, id: encodeUserHandle(options.user.id) },
    excludeCredentials: options.excludeCredentials?.map((credential) => ({
      ...credential,
      id: decodeBase64URL(credential.id),
      transports: validTransports(credential.transports),
    })),
  };
}

function publicKeyRequestOptions(
  options: RequestOptionsJSON,
): PublicKeyCredentialRequestOptions {
  return {
    ...options,
    challenge: decodeBase64URL(options.challenge),
    allowCredentials: options.allowCredentials?.map((credential) => ({
      ...credential,
      id: decodeBase64URL(credential.id),
      transports: validTransports(credential.transports),
    })),
  };
}

function isAttestationResponse(
  response: AuthenticatorResponse,
): response is AuthenticatorAttestationResponse {
  return "attestationObject" in response;
}

function isAssertionResponse(
  response: AuthenticatorResponse,
): response is AuthenticatorAssertionResponse {
  return (
    "authenticatorData" in response &&
    "signature" in response &&
    "userHandle" in response
  );
}

export function serializeCredential(
  credential: PublicKeyCredential,
  registration: true,
): SerializedCredentialBase & { response: RegistrationResponseJSON };
export function serializeCredential(
  credential: PublicKeyCredential,
  registration: false,
): SerializedCredentialBase & { response: AssertionResponseJSON };
export function serializeCredential(
  credential: PublicKeyCredential,
  registration: boolean,
): SerializedCredential {
  const common: SerializedCredentialBase = {
    id: credential.id,
    rawId: encodeBase64URL(credential.rawId),
    type: "public-key",
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
  };
  const clientDataJSON = encodeBase64URL(credential.response.clientDataJSON);

  if (registration) {
    if (!isAttestationResponse(credential.response)) {
      throw new Error("Invalid passkey registration response.");
    }
    const response: RegistrationResponseJSON = {
      clientDataJSON,
      attestationObject: encodeBase64URL(credential.response.attestationObject),
    };
    if (
      "getTransports" in credential.response &&
      typeof credential.response.getTransports === "function"
    ) {
      response.transports = credential.response.getTransports();
    }
    return { ...common, response };
  }

  if (!isAssertionResponse(credential.response)) {
    throw new Error("Invalid passkey sign-in response.");
  }
  const response: AssertionResponseJSON = {
    clientDataJSON,
    authenticatorData: encodeBase64URL(credential.response.authenticatorData),
    signature: encodeBase64URL(credential.response.signature),
    userHandle: credential.response.userHandle
      ? encodeBase64URL(credential.response.userHandle)
      : null,
  };
  return { ...common, response };
}

function isPublicKeyCredential(
  credential: Credential | null,
): credential is PublicKeyCredential {
  return (
    credential !== null &&
    credential.type === "public-key" &&
    "rawId" in credential &&
    "response" in credential &&
    "getClientExtensionResults" in credential
  );
}

async function runRegistrationCeremony(
  path: string,
  finishPath: string,
  name: string,
): Promise<unknown> {
  if (!window.PublicKeyCredential || !window.isSecureContext) {
    throw new Error(
      "Passkeys require a supported browser and a secure connection.",
    );
  }
  const startResponse = await fetch(path, { method: "POST" });
  if (!startResponse.ok)
    throw new Error(
      await failureMessage(
        startResponse,
        "Could not start the passkey ceremony.",
      ),
    );
  const { ceremony_id: ceremonyID, options } =
    (await startResponse.json()) as PasskeyBeginResponse;
  const publicKey = publicKeyCreationOptions(
    options.publicKey as CreationOptionsJSON,
  );
  const credential = await navigator.credentials.create({ publicKey });
  if (!isPublicKeyCredential(credential))
    throw new Error("The passkey ceremony was cancelled.");

  const finishResponse = await fetch(finishPath, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Cookbook-Ceremony": ceremonyID,
      "X-Cookbook-Passkey-Name": name || "Passkey",
    },
    body: JSON.stringify(serializeCredential(credential, true)),
  });
  if (!finishResponse.ok)
    throw new Error(
      await failureMessage(
        finishResponse,
        "The passkey could not be verified. Try again.",
      ),
    );
  return finishResponse.json();
}

async function runSignInCeremony(
  path: string,
  finishPath: string,
): Promise<unknown> {
  if (!window.PublicKeyCredential || !window.isSecureContext) {
    throw new Error(
      "Passkeys require a supported browser and a secure connection.",
    );
  }
  const startResponse = await fetch(path, { method: "POST" });
  if (!startResponse.ok)
    throw new Error(
      await failureMessage(
        startResponse,
        "Could not start the passkey ceremony.",
      ),
    );
  const { ceremony_id: ceremonyID, options } =
    (await startResponse.json()) as PasskeyBeginResponse;
  const publicKey = publicKeyRequestOptions(
    options.publicKey as RequestOptionsJSON,
  );
  const credential = await navigator.credentials.get({ publicKey });
  if (!isPublicKeyCredential(credential))
    throw new Error("The passkey ceremony was cancelled.");

  const finishResponse = await fetch(finishPath, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Cookbook-Ceremony": ceremonyID,
    },
    body: JSON.stringify(serializeCredential(credential, false)),
  });
  if (!finishResponse.ok)
    throw new Error(
      await failureMessage(
        finishResponse,
        "The passkey could not be verified. Try again.",
      ),
    );
  return finishResponse.json();
}

export function registerPasskey(name = "Passkey"): Promise<unknown> {
  return runRegistrationCeremony(
    "/api/auth/passkey/register/begin",
    "/api/auth/passkey/register/finish",
    name,
  );
}

export function signInWithPasskey(): Promise<unknown> {
  return runSignInCeremony(
    "/api/auth/passkey/login/begin",
    "/api/auth/passkey/login/finish",
  );
}
