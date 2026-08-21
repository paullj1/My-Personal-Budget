import { bufferFromBase64Url, toBase64Url } from './bytes';

describe('bytes utils', () => {
  it('round-trips base64url encoding', () => {
    const buffer = new Uint8Array([1, 2, 255]).buffer;
    const encoded = toBase64Url(buffer);
    const decoded = bufferFromBase64Url(encoded);
    expect(Array.from(decoded)).toEqual([1, 2, 255]);
  });

  it('handles Uint8Array inputs', () => {
    const decoded = bufferFromBase64Url(new Uint8Array([9, 8, 7]));
    expect(Array.from(decoded)).toEqual([9, 8, 7]);
  });

  it('copies only the view it was given, not the whole backing buffer', () => {
    // A subarray shares its buffer with the original. Reading .buffer directly
    // returned all eight bytes, so a WebAuthn credential id sliced out of a
    // larger buffer silently carried its neighbours along.
    const backing = new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8]);
    const view = backing.subarray(4, 6);

    const decoded = bufferFromBase64Url(view);
    expect(Array.from(decoded)).toEqual([5, 6]);
    expect(decoded.byteLength).toBe(2);
  });

  it('detaches from the source buffer', () => {
    // The result must not alias the input, or mutating one would corrupt the other.
    const source = new Uint8Array([1, 2, 3]);
    const decoded = bufferFromBase64Url(source);
    source[0] = 99;
    expect(Array.from(decoded)).toEqual([1, 2, 3]);
  });

  it('accepts an ArrayBuffer and a plain array', () => {
    expect(Array.from(bufferFromBase64Url(new Uint8Array([4, 5]).buffer))).toEqual([4, 5]);
    expect(Array.from(bufferFromBase64Url([6, 7]))).toEqual([6, 7]);
  });

  it('decodes base64url without padding and with url-safe characters', () => {
    // "-" and "_" stand in for "+" and "/", and the padding is stripped.
    const bytes = new Uint8Array([251, 255, 190]);
    const encoded = toBase64Url(bytes.buffer);
    expect(encoded).not.toContain('=');
    expect(encoded).not.toContain('+');
    expect(encoded).not.toContain('/');
    expect(Array.from(bufferFromBase64Url(encoded))).toEqual([251, 255, 190]);
  });

  it('produces a buffer WebAuthn will accept', () => {
    // BufferSource rejects a Uint8Array whose buffer might be shared, which is
    // what a view-derived result used to be.
    const decoded = bufferFromBase64Url('AQID');
    expect(decoded.buffer).toBeInstanceOf(ArrayBuffer);
    const asBufferSource: BufferSource = decoded;
    expect(asBufferSource).toBeDefined();
  });

  it('throws on invalid input', () => {
    expect(() => bufferFromBase64Url(123 as unknown as string)).toThrow(
      'Invalid base64url input: expected string or buffer.'
    );
  });
});
