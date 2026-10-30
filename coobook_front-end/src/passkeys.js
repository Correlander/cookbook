function decodeBase64URL(value) {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/')
    .padEnd(Math.ceil(value.length / 4) * 4, '=')
  const binary = window.atob(padded)
  return Uint8Array.from(binary, character => character.charCodeAt(0))
}

function encodeBase64URL(value) {
  const bytes = new Uint8Array(value)
  let binary = ''
  bytes.forEach(byte => { binary += String.fromCharCode(byte) })
  return window.btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

function publicKeyOptions(options, registration) {
  const publicKey = options.publicKey
  publicKey.challenge = decodeBase64URL(publicKey.challenge)
  if (registration) {
    publicKey.user.id = new TextEncoder().encode(publicKey.user.id)
    publicKey.excludeCredentials = (publicKey.excludeCredentials || []).map(credential => ({
      ...credential,
      id: decodeBase64URL(credential.id),
    }))
  } else {
    publicKey.allowCredentials = (publicKey.allowCredentials || []).map(credential => ({
      ...credential,
      id: decodeBase64URL(credential.id),
    }))
  }
  return publicKey
}

function serializeCredential(credential, registration) {
  const response = {
    clientDataJSON: encodeBase64URL(credential.response.clientDataJSON),
  }
  if (registration) {
    response.attestationObject = encodeBase64URL(credential.response.attestationObject)
    if (credential.response.getTransports) response.transports = credential.response.getTransports()
  } else {
    response.authenticatorData = encodeBase64URL(credential.response.authenticatorData)
    response.signature = encodeBase64URL(credential.response.signature)
    response.userHandle = credential.response.userHandle
      ? encodeBase64URL(credential.response.userHandle)
      : null
  }
  return {
    id: credential.id,
    rawId: encodeBase64URL(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    response,
    clientExtensionResults: credential.getClientExtensionResults(),
  }
}

async function runCeremony(path, finishPath, registration) {
  if (!window.PublicKeyCredential || !window.isSecureContext) {
    throw new Error('Passkeys require a supported browser and a secure connection.')
  }
  const startResponse = await fetch(path, { method: 'POST' })
  if (!startResponse.ok) throw new Error('Could not start the passkey ceremony.')
  const { ceremony_id: ceremonyID, options } = await startResponse.json()
  const publicKey = publicKeyOptions(options, registration)
  const credential = registration
    ? await navigator.credentials.create({ publicKey })
    : await navigator.credentials.get({ publicKey })
  if (!credential) throw new Error('The passkey ceremony was cancelled.')
  const finishResponse = await fetch(finishPath, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Cookbook-Ceremony': ceremonyID,
    },
    body: JSON.stringify(serializeCredential(credential, registration)),
  })
  if (!finishResponse.ok) throw new Error('The passkey could not be verified. Try again.')
  return finishResponse.json()
}

export function registerPasskey() {
  return runCeremony('/api/auth/passkey/register/begin', '/api/auth/passkey/register/finish', true)
}

export function signInWithPasskey() {
  return runCeremony('/api/auth/passkey/login/begin', '/api/auth/passkey/login/finish', false)
}
