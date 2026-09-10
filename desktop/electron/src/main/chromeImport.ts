import { DatabaseSync } from "node:sqlite";
import { execFile } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { createDecipheriv, createHash, pbkdf2Sync } from "node:crypto";
import type { ChromeImportFailure } from "../shared/ipc.js";

export interface ChromeProfile {
  name: string;
  cookiesPath: string;
  modifiedAt: number;
}

export class ChromeImportError extends Error {
  constructor(readonly code: ChromeImportFailure, message?: string) {
    super(message ?? code);
    this.name = "ChromeImportError";
  }
}

export interface ChromeImportSummary {
  profile: string;
  cookies: number;
  skipped: number;
}

export interface ChromeCookie {
  url: string;
  name: string;
  value: string;
  domain: string;
  path: string;
  secure: boolean;
  httpOnly: boolean;
  sameSite: "unspecified" | "no_restriction" | "lax" | "strict";
  expirationDate?: number;
}

export interface CookieSink {
  set(cookie: ChromeCookie): Promise<void>;
}

export interface ChromeImportDeps {
  platform: NodeJS.Platform;
  home: string;
  env: NodeJS.ProcessEnv;
  cookies: CookieSink;
  run(command: string, args: string[]): Promise<string>;
  log?: { info(message: string): void; warn(message: string): void };
}

const CHROME_EPOCH_OFFSET_SECONDS = 11_644_473_600;
const CIPHER_ITERATIONS = { darwin: 1003, linux: 1 } as const;
const SAME_SITE = ["no_restriction", "lax", "strict"] as const;

export function chromeProfileRoot(platform: NodeJS.Platform, home: string, env: NodeJS.ProcessEnv): string {
  if (platform === "darwin") return join(home, "Library", "Application Support", "Google", "Chrome");
  if (platform === "win32") {
    const local = env.LOCALAPPDATA && env.LOCALAPPDATA !== "" ? env.LOCALAPPDATA : join(home, "AppData", "Local");
    return join(local, "Google", "Chrome", "User Data");
  }
  return join(home, ".config", "google-chrome");
}

// Chrome writes one Cookies SQLite database per profile; the newest database is
// the profile the user last browsed with, which is the one worth importing.
export function findChromeProfiles(root: string, list: (path: string) => string[]): ChromeProfile[] {
  if (!existsSync(root)) return [];
  const profiles: ChromeProfile[] = [];
  for (const name of list(root)) {
    if (name !== "Default" && !/^Profile \d+$/.test(name)) continue;
    const cookiesPath = join(root, name, "Cookies");
    if (!existsSync(cookiesPath)) continue;
    try {
      profiles.push({ name, cookiesPath, modifiedAt: statSync(cookiesPath).mtimeMs });
    } catch {
      continue;
    }
  }
  return profiles.sort((left, right) => right.modifiedAt - left.modifiedAt);
}

export function chromeExpiryToUnixSeconds(expiresUTC: string | number | bigint): number | undefined {
  const micros = toBigInt(expiresUTC);
  if (micros === null || micros <= 0n) return undefined;
  return Number(micros / 1_000_000n) - CHROME_EPOCH_OFFSET_SECONDS;
}

// Chrome's microsecond timestamps exceed Number.MAX_SAFE_INTEGER, so every path
// keeps them as text or BigInt until the division is already exact.
function toBigInt(value: string | number | bigint): bigint | null {
  try {
    if (typeof value === "bigint") return value;
    if (typeof value === "number") return Number.isFinite(value) ? BigInt(Math.trunc(value)) : null;
    return value.trim() === "" ? null : BigInt(value);
  } catch {
    return null;
  }
}

// Chrome stores sameSite as -1/0/1/2; Electron takes the same names on the wire,
// so this is a rename rather than a translation.
export function chromeSameSite(value: number): ChromeCookie["sameSite"] {
  return SAME_SITE[value] ?? "unspecified";
}

export function cookieURL(hostKey: string, path: string, secure: boolean): string {
  const host = hostKey.startsWith(".") ? hostKey.slice(1) : hostKey;
  return `${secure ? "https" : "http"}://${host}${path.startsWith("/") ? path : `/${path}`}`;
}

// macOS and Linux wrap cookie values with AES-128-CBC under a PBKDF2 key: the
// login-keychain secret on macOS, the well-known "peanuts" password elsewhere.
export function decryptCBC(payload: Buffer, password: string, iterations: number): Buffer {
  const prefix = payload.subarray(0, 3).toString("latin1");
  const body = prefix === "v10" || prefix === "v11" ? payload.subarray(3) : payload;
  const key = pbkdf2Sync(password, "saltysalt", iterations, 16, "sha1");
  const decipher = createDecipheriv("aes-128-cbc", key, Buffer.alloc(16, 0x20));
  return Buffer.concat([decipher.update(body), decipher.final()]);
}

// Windows keeps a DPAPI-protected master key in Local State and seals each value
// with AES-256-GCM; v20 values are App-Bound and cannot be read here at all.
export function decryptGCM(payload: Buffer, key: Buffer): Buffer {
  const decipher = createDecipheriv("aes-256-gcm", key, payload.subarray(3, 15));
  decipher.setAuthTag(payload.subarray(payload.length - 16));
  return Buffer.concat([decipher.update(payload.subarray(15, payload.length - 16)), decipher.final()]);
}

// Chrome 96+ binds every value to its host: the plaintext starts with the
// 32-byte SHA-256 of host_key before the value itself.
export function stripDomainHash(plaintext: Buffer, hostKey: string): Buffer {
  if (plaintext.length <= 32) return plaintext;
  const digest = createHash("sha256").update(hostKey).digest();
  return plaintext.subarray(0, 32).equals(digest) ? plaintext.subarray(32) : plaintext;
}

export function windowsMasterKeyBlob(localState: unknown): Buffer {
  const record = typeof localState === "object" && localState !== null ? (localState as Record<string, unknown>) : {};
  const osCrypt = typeof record.os_crypt === "object" && record.os_crypt !== null ? (record.os_crypt as Record<string, unknown>) : {};
  const encoded = osCrypt.encrypted_key;
  if (typeof encoded !== "string" || encoded === "") throw new ChromeImportError("safe-storage-unavailable", "chrome_master_key_missing");
  const raw = Buffer.from(encoded, "base64");
  return raw.subarray(0, 5).toString("latin1") === "DPAPI" ? raw.subarray(5) : raw;
}

export async function readChromeSafeStoragePassword(deps: Pick<ChromeImportDeps, "platform" | "run">): Promise<string> {
  if (deps.platform === "linux") return "peanuts";
  if (deps.platform !== "darwin") throw new ChromeImportError("unsupported-platform", "windows cookies use a DPAPI master key");
  try {
    const password = (await deps.run("security", ["find-generic-password", "-w", "-s", "Chrome Safe Storage"])).trim();
    if (password === "") throw new ChromeImportError("safe-storage-unavailable", "empty keychain secret");
    return password;
  } catch (error) {
    if (error instanceof ChromeImportError) throw error;
    throw new ChromeImportError("safe-storage-denied", error instanceof Error ? error.message : String(error));
  }
}

async function readWindowsMasterKey(deps: Pick<ChromeImportDeps, "run">, root: string): Promise<Buffer> {
  const localStatePath = join(root, "Local State");
  if (!existsSync(localStatePath)) throw new ChromeImportError("safe-storage-unavailable", "Local State missing");
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(localStatePath, "utf8"));
  } catch (error) {
    throw new ChromeImportError("safe-storage-unavailable", error instanceof Error ? error.message : String(error));
  }
  const blob = windowsMasterKeyBlob(parsed).toString("base64");
  const script = [
    "$ErrorActionPreference='Stop'",
    `$data=[Convert]::FromBase64String('${blob}')`,
    "$plain=[Security.Cryptography.ProtectedData]::Unprotect($data,$null,'CurrentUser')",
    "[Convert]::ToBase64String($plain)",
  ].join(";");
  const output = await deps.run("powershell", ["-NoProfile", "-NonInteractive", "-Command", script]).catch((error: unknown) => {
    throw new ChromeImportError("safe-storage-denied", error instanceof Error ? error.message : String(error));
  });
  return Buffer.from(output.trim(), "base64");
}

interface CookieRow {
  host_key: string;
  name: string;
  encrypted_value: Uint8Array;
  path: string;
  expires_utc: string;
  is_secure: number;
  is_httponly: number;
  samesite: number;
}

export function readCookieRows(cookiesPath: string): CookieRow[] {
  const database = new DatabaseSync(cookiesPath, { readOnly: true });
  try {
    return database
      .prepare(
        "SELECT host_key, name, encrypted_value, path, CAST(expires_utc AS TEXT) AS expires_utc, is_secure, is_httponly, samesite FROM cookies",
      )
      .all() as unknown as CookieRow[];
  } finally {
    database.close();
  }
}

export async function importChromeCookies(deps: ChromeImportDeps, root: string, profiles: ChromeProfile[]): Promise<ChromeImportSummary> {
  const profile = profiles[0];
  if (!profile) throw new ChromeImportError("profile-not-found");
  let rows: CookieRow[];
  try {
    rows = readCookieRows(profile.cookiesPath);
  } catch (error) {
    throw new ChromeImportError("cookies-unreadable", error instanceof Error ? error.message : String(error));
  }
  const password = deps.platform === "win32" ? "" : await readChromeSafeStoragePassword(deps);
  const masterKey = deps.platform === "win32" ? await readWindowsMasterKey(deps, root) : null;
  const iterations = deps.platform === "linux" ? CIPHER_ITERATIONS.linux : CIPHER_ITERATIONS.darwin;
  const now = Math.floor(Date.now() / 1000);
  let cookies = 0;
  let skipped = 0;
  for (const row of rows) {
    const expiry = chromeExpiryToUnixSeconds(row.expires_utc);
    if (expiry !== undefined && expiry <= now) {
      skipped++;
      continue;
    }
    let value: string;
    try {
      const payload = Buffer.from(row.encrypted_value);
      const prefix = payload.subarray(0, 3).toString("latin1");
      if (prefix === "v20") {
        skipped++;
        continue;
      }
      const plaintext = masterKey && prefix === "v10" ? decryptGCM(payload, masterKey) : decryptCBC(payload, password, iterations);
      value = stripDomainHash(plaintext, row.host_key).toString("utf8");
    } catch (error) {
      deps.log?.warn(`chrome cookie ${row.name} could not be decrypted: ${error instanceof Error ? error.message : String(error)}`);
      skipped++;
      continue;
    }
    const secure = row.is_secure !== 0;
    const cookie: ChromeCookie = {
      url: cookieURL(row.host_key, row.path, secure),
      name: row.name,
      value,
      domain: row.host_key,
      path: row.path,
      secure,
      httpOnly: row.is_httponly !== 0,
      sameSite: chromeSameSite(row.samesite),
      ...(expiry === undefined ? {} : { expirationDate: expiry }),
    };
    try {
      await deps.cookies.set(cookie);
      cookies++;
    } catch (error) {
      deps.log?.warn(`chrome cookie ${row.name} was rejected: ${error instanceof Error ? error.message : String(error)}`);
      skipped++;
    }
  }
  deps.log?.info(`chrome import: profile=${profile.name} cookies=${cookies} skipped=${skipped}`);
  return { profile: profile.name, cookies, skipped };
}

export function defaultRunCommand(command: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(command, args, { maxBuffer: 8 * 1024 * 1024 }, (error, stdout) => {
      if (error) reject(error);
      else resolve(stdout);
    });
  });
}
