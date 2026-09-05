/**
 * The base64 SHA-256 of a file, computed in the browser.
 *
 * The object store verifies the upload against this digest, so it has to be
 * the digest of the bytes actually sent - which means computing it here rather
 * than trusting anything the server could guess. A mismatch is refused by the
 * store with XAmzContentChecksumMismatch, which is a stronger guarantee than
 * the API could make about a file it never sees.
 *
 * The whole file is read into memory once. That is unavoidable - a digest
 * needs every byte - and it is bounded by the 25 MiB the server accepts.
 *
 * crypto.subtle is only available in a secure context, which means HTTPS or
 * localhost. The failure is explicit rather than a silent fallback to a weaker
 * digest: an unverified upload should not look like a verified one.
 */
export async function sha256Base64(file: File): Promise<string> {
  if (!globalThis.crypto?.subtle) {
    throw new Error(
      "Uploading a receipt needs a secure connection: the file's checksum is computed in the browser, and the Web Crypto API is unavailable over plain HTTP.",
    );
  }

  // Copied into a Uint8Array rather than handed the ArrayBuffer directly.
  // digest accepts either, but an ArrayBuffer created in another realm - which
  // is what a File gives under jsdom, and what a cross-origin worker would
  // give in a browser - is rejected by the identity check inside it.
  const bytes = new Uint8Array(await file.arrayBuffer());
  const digest = await crypto.subtle.digest("SHA-256", bytes);

  // btoa wants a binary string, and String.fromCharCode(...bytes) blows the
  // argument limit on anything sizeable - a digest is only 32 bytes, but the
  // loop is what keeps this correct if it is ever reused for something larger.
  let binary = "";
  for (const byte of new Uint8Array(digest)) binary += String.fromCharCode(byte);
  return btoa(binary);
}

/** File sizes for humans. Binary units, because that is what the limit is in. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`;
}
