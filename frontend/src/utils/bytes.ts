/**
 * Decodes base64url (or passes bytes through) as a Uint8Array backed by a plain
 * ArrayBuffer.
 *
 * The buffer type is pinned because WebAuthn's BufferSource rejects a
 * Uint8Array<ArrayBufferLike>, which is what you get from a view whose buffer
 * might be a SharedArrayBuffer.
 */
export function bufferFromBase64Url(
  value: string | ArrayBuffer | ArrayLike<number>
): Uint8Array<ArrayBuffer> {
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    // Copy only the view's own window. Reading value.buffer directly returned the
    // entire backing buffer, so a subarray silently decoded its neighbours too.
    const view = new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    const copy = new Uint8Array(view.byteLength);
    copy.set(view);
    return copy;
  }
  if (Array.isArray(value)) {
    return new Uint8Array(value);
  }
  if (typeof value !== 'string') {
    throw new Error('Invalid base64url input: expected string or buffer.');
  }
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), '=');
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

export function toBase64Url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = '';
  bytes.forEach((b) => (binary += String.fromCharCode(b)));
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
