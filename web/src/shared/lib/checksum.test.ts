import { describe, expect, it } from "vitest";

import { formatBytes, sha256Base64 } from "./checksum";

describe("sha256Base64", () => {
  it("matches the digest the object store will compute", async () => {
    // The empty string's SHA-256 is a published constant, so this pins the
    // encoding as well as the digest - base64 of the raw bytes, not hex.
    const file = new File([], "empty.pdf", { type: "application/pdf" });
    expect(await sha256Base64(file)).toBe("47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=");
  });

  it("digests content rather than the name", async () => {
    const a = new File(["receipt"], "a.pdf", { type: "application/pdf" });
    const b = new File(["receipt"], "b.pdf", { type: "application/pdf" });
    const c = new File(["different"], "a.pdf", { type: "application/pdf" });

    expect(await sha256Base64(a)).toBe(await sha256Base64(b));
    expect(await sha256Base64(a)).not.toBe(await sha256Base64(c));
  });

  it("produces a 32-byte digest", async () => {
    const digest = await sha256Base64(new File(["x"], "x.pdf"));
    expect(atob(digest)).toHaveLength(32);
  });
});

describe("formatBytes", () => {
  it("uses binary units, because the limit is expressed in them", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024)).toBe("1.0 KiB");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(25 * 1024 * 1024)).toBe("25 MiB");
  });

  it("drops the decimal once the number is large enough not to need it", () => {
    expect(formatBytes(10 * 1024)).toBe("10 KiB");
    expect(formatBytes(9 * 1024)).toBe("9.0 KiB");
  });
});
