function decodeB64(b64url) {
  const s = b64url.replace(/-/g, '+').replace(/_/g, '/');
  return Uint8Array.from(atob(s), c => c.charCodeAt(0));
}

function encodeB64(buf) {
  return btoa(String.fromCharCode(...new Uint8Array(buf)))
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

function credentialToJSON(cred) {
  return {
    id: cred.id,
    rawId: encodeB64(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: encodeB64(cred.response.authenticatorData),
      clientDataJSON:    encodeB64(cred.response.clientDataJSON),
      signature:         encodeB64(cred.response.signature),
      userHandle: cred.response.userHandle ? encodeB64(cred.response.userHandle) : null,
    }
  };
}

function attestationToJSON(cred) {
  return {
    id:    cred.id,
    rawId: encodeB64(cred.rawId),
    type:  cred.type,
    response: {
      attestationObject: encodeB64(cred.response.attestationObject),
      clientDataJSON:    encodeB64(cred.response.clientDataJSON),
    }
  };
}
