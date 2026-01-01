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

  it('throws on invalid input', () => {
    expect(() => bufferFromBase64Url(123 as unknown as string)).toThrow(
      'Invalid base64url input: expected string or buffer.'
    );
  });
});
