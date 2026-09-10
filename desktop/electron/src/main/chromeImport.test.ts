import assert from "node:assert/strict";
import { createCipheriv, createHash, pbkdf2Sync, randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { test } from "node:test";
import {
  ChromeImportError,
  chromeExpiryToUnixSeconds,
  chromeProfileRoot,
  chromeSameSite,
  cookieURL,
  decryptCBC,
  decryptGCM,
  findChromeProfiles,
  importChromeCookies,
  readChromeSafeStoragePassword,
  stripDomainHash,
  windowsMasterKeyBlob,
  type ChromeCookie,
  type ChromeImportDeps,
} from "./chromeImport.js";

const CHROME_EPOCH_OFFSET_SECONDS = 11_644_473_600;
const PASSWORD = "test-safe-storage";

// Chrome 96+ writes SHA-256(host_key) ahead of the value, so fixtures use the
// same layout the real profile does.
function encryptCBC(value: string, hostKey = "", prefix = "v10"): Buffer {
  const key = pbkdf2Sync(PASSWORD, "saltysalt", 1003, 16, "sha1");
  const cipher = createCipheriv("aes-128-cbc", key, Buffer.alloc(16, 0x20));
  const head = hostKey === "" ? Buffer.alloc(0) : createHash("sha256").update(hostKey).digest();
  return Buffer.concat([Buffer.from(prefix), cipher.update(Buffer.concat([head, Buffer.from(value, "utf8")])), cipher.final()]);
}

function encryptGCM(value: string, key: Buffer): Buffer {
  const nonce = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, nonce);
  const body = Buffer.concat([cipher.update(value, "utf8"), cipher.final()]);
  return Buffer.concat([Buffer.from("v10"), nonce, body, cipher.getAuthTag()]);
}

function writeCookiesDatabase(root: string, profile: string, rows: Record<string, unknown>[]): string {
  mkdirSync(join(root, profile), { recursive: true });
  const path = join(root, profile, "Cookies");
  const database = new DatabaseSync(path);
  database.exec(`CREATE TABLE cookies (
    host_key TEXT, name TEXT, encrypted_value BLOB, path TEXT,
    expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER)`);
  const insert = database.prepare("INSERT INTO cookies VALUES (?, ?, ?, ?, ?, ?, ?, ?)");
  for (const row of rows) {
    insert.run(
      row.host_key as string,
      row.name as string,
      row.encrypted_value as Uint8Array,
      row.path as string,
      row.expires_utc as number,
      row.is_secure as number,
      row.is_httponly as number,
      row.samesite as number,
    );
  }
  database.close();
  return path;
}

function deps(seen: ChromeCookie[], overrides: Partial<ChromeImportDeps> = {}): ChromeImportDeps {
  return {
    platform: "darwin",
    home: "/Users/example",
    env: {},
    cookies: { set: async (cookie) => void seen.push(cookie) },
    run: async () => PASSWORD,
    ...overrides,
  };
}

const FUTURE = ((BigInt(Math.floor(Date.now() / 1000)) + 86_400n + BigInt(CHROME_EPOCH_OFFSET_SECONDS)) * 1_000_000n).toString();
const PAST = ((BigInt(Math.floor(Date.now() / 1000)) - 86_400n + BigInt(CHROME_EPOCH_OFFSET_SECONDS)) * 1_000_000n).toString();

test("profile roots follow the platform conventions", () => {
  assert.equal(chromeProfileRoot("darwin", "/Users/example", {}), "/Users/example/Library/Application Support/Google/Chrome");
  assert.equal(chromeProfileRoot("win32", "C:\\Users\\example", { LOCALAPPDATA: "C:\\Users\\example\\AppData\\Local" }), join("C:\\Users\\example\\AppData\\Local", "Google", "Chrome", "User Data"));
  assert.equal(chromeProfileRoot("linux", "/home/example", {}), "/home/example/.config/google-chrome");
});

test("expiry converts from Chrome's 1601 epoch and treats zero as a session cookie", () => {
  assert.equal(chromeExpiryToUnixSeconds(0), undefined);
  assert.equal(chromeExpiryToUnixSeconds("0"), undefined);
  assert.equal(chromeExpiryToUnixSeconds(""), undefined);
  // Chrome's microsecond stamps exceed Number.MAX_SAFE_INTEGER, so the exact
  // string form must survive the conversion.
  assert.equal(chromeExpiryToUnixSeconds("11644473600000000"), 0);
  assert.equal(chromeExpiryToUnixSeconds("11644473660000000"), 60);
  assert.equal(chromeExpiryToUnixSeconds(11_644_473_660_000_000n), 60);
});

test("cookie fields map to Electron's vocabulary", () => {
  assert.equal(chromeSameSite(-1), "unspecified");
  assert.equal(chromeSameSite(0), "no_restriction");
  assert.equal(chromeSameSite(1), "lax");
  assert.equal(chromeSameSite(2), "strict");
  assert.equal(chromeSameSite(9), "unspecified");
  assert.equal(cookieURL(".example.test", "/app", true), "https://example.test/app");
  assert.equal(cookieURL("example.test", "app", false), "http://example.test/app");
});

test("CBC and GCM payloads round-trip", () => {
  assert.equal(decryptCBC(encryptCBC("session-token"), PASSWORD, 1003).toString("utf8"), "session-token");
  const key = randomBytes(32);
  assert.equal(decryptGCM(encryptGCM("session-token", key), key).toString("utf8"), "session-token");
});

test("the domain hash Chrome binds to each value is stripped", () => {
  const digest = createHash("sha256").update(".example.test").digest();
  const bound = Buffer.concat([digest, Buffer.from("token", "utf8")]);
  assert.equal(stripDomainHash(bound, ".example.test").toString("utf8"), "token");
  // A value bound to another host, a short value and an unbound value all
  // survive untouched.
  assert.deepEqual(stripDomainHash(bound, ".other.test"), bound);
  assert.equal(stripDomainHash(Buffer.from("short"), ".example.test").toString("utf8"), "short");
  assert.equal(stripDomainHash(decryptCBC(encryptCBC("unbound"), PASSWORD, 1003), ".example.test").toString("utf8"), "unbound");
});

test("windows master key strips the DPAPI prefix", () => {
  const blob = Buffer.concat([Buffer.from("DPAPI"), Buffer.from("secret")]);
  assert.equal(windowsMasterKeyBlob({ os_crypt: { encrypted_key: blob.toString("base64") } }).toString(), "secret");
  assert.throws(() => windowsMasterKeyBlob({ os_crypt: {} }), (error: unknown) => error instanceof ChromeImportError && error.code === "safe-storage-unavailable");
});

test("the newest profile with cookies wins", () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-chrome-"));
  writeCookiesDatabase(root, "Default", []);
  writeCookiesDatabase(root, "Profile 1", []);
  mkdirSync(join(root, "Crashpad"), { recursive: true });
  const profiles = findChromeProfiles(root, (path) => ["Default", "Profile 1", "Crashpad"].filter((name) => name !== "" && (name === "Crashpad" || path === root)));
  assert.deepEqual(profiles.map((profile) => profile.name).sort(), ["Default", "Profile 1"]);
  assert.deepEqual(findChromeProfiles(join(root, "missing"), () => []), []);
});

test("imports decryptable cookies and skips the ones it cannot use", async () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-chrome-"));
  writeCookiesDatabase(root, "Default", [
    { host_key: ".example.test", name: "sid", encrypted_value: encryptCBC("token-1", ".example.test"), path: "/", expires_utc: FUTURE, is_secure: 1, is_httponly: 1, samesite: 1 },
    { host_key: "expired.test", name: "old", encrypted_value: encryptCBC("token-2", "expired.test"), path: "/", expires_utc: PAST, is_secure: 0, is_httponly: 0, samesite: -1 },
    { host_key: "bound.test", name: "appbound", encrypted_value: encryptCBC("token-3", "bound.test", "v20"), path: "/", expires_utc: FUTURE, is_secure: 1, is_httponly: 0, samesite: 0 },
    { host_key: "broken.test", name: "garbled", encrypted_value: Buffer.from("v10notreallyencrypted"), path: "/", expires_utc: FUTURE, is_secure: 0, is_httponly: 0, samesite: -1 },
  ]);
  const seen: ChromeCookie[] = [];
  const profiles = findChromeProfiles(root, () => ["Default"]);
  const summary = await importChromeCookies(deps(seen), root, profiles);
  assert.deepEqual(summary, { profile: "Default", cookies: 1, skipped: 3 });
  assert.deepEqual(seen, [
    {
      url: "https://example.test/",
      name: "sid",
      value: "token-1",
      domain: ".example.test",
      path: "/",
      secure: true,
      httpOnly: true,
      sameSite: "lax",
      expirationDate: Number(BigInt(FUTURE) / 1_000_000n) - CHROME_EPOCH_OFFSET_SECONDS,
    },
  ]);
});

test("a rejected cookie is skipped instead of failing the import", async () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-chrome-"));
  writeCookiesDatabase(root, "Default", [
    { host_key: "example.test", name: "sid", encrypted_value: encryptCBC("token", "example.test"), path: "/", expires_utc: FUTURE, is_secure: 1, is_httponly: 0, samesite: -1 },
  ]);
  const summary = await importChromeCookies(
    deps([], { cookies: { set: async () => Promise.reject(new Error("invalid cookie")) } }),
    root,
    findChromeProfiles(root, () => ["Default"]),
  );
  assert.deepEqual(summary, { profile: "Default", cookies: 0, skipped: 1 });
});

test("import failures surface as typed codes", async () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-chrome-"));
  await assert.rejects(
    () => importChromeCookies(deps([]), root, []),
    (error: unknown) => error instanceof ChromeImportError && error.code === "profile-not-found",
  );
  writeCookiesDatabase(root, "Default", []);
  writeFileSync(join(root, "Default", "Cookies"), "not a database");
  await assert.rejects(
    () => importChromeCookies(deps([]), root, findChromeProfiles(root, () => ["Default"])),
    (error: unknown) => error instanceof ChromeImportError && error.code === "cookies-unreadable",
  );
});

test("a denied keychain prompt is distinguishable from a missing key", async () => {
  await assert.rejects(
    () => readChromeSafeStoragePassword({ platform: "darwin", run: async () => Promise.reject(new Error("User interaction is not allowed.")) }),
    (error: unknown) => error instanceof ChromeImportError && error.code === "safe-storage-denied",
  );
  await assert.rejects(
    () => readChromeSafeStoragePassword({ platform: "darwin", run: async () => "" }),
    (error: unknown) => error instanceof ChromeImportError && error.code === "safe-storage-unavailable",
  );
  assert.equal(await readChromeSafeStoragePassword({ platform: "linux", run: async () => "" }), "peanuts");
  await assert.rejects(
    () => readChromeSafeStoragePassword({ platform: "win32", run: async () => "" }),
    (error: unknown) => error instanceof ChromeImportError && error.code === "unsupported-platform",
  );
});
