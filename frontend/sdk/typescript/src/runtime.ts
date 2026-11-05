/**
 * What the library takes from the runtime: the environment, a random id, and - for files on this computer only -
 * Node's file system, loaded when first needed so the library itself imports nothing.
 */

import { WheelmachineError } from "./errors.js";

/** An environment variable, where the runtime has them. */
export function env(name: string): string | undefined {
  const proc = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process;
  const value = proc?.env?.[name];
  return typeof value === "string" ? value : undefined;
}

/** Whether this runs in a web page or a web worker, where an API key would reach everyone who opens the page. */
export function inBrowser(): boolean {
  const g = globalThis as { window?: unknown; document?: unknown; importScripts?: unknown; WorkerGlobalScope?: unknown };
  if (typeof g.window !== "undefined" && typeof g.document !== "undefined") return true;
  return typeof g.WorkerGlobalScope !== "undefined" && typeof g.importScripts === "function";
}

interface WebCrypto {
  randomUUID?: () => string;
  getRandomValues?: (array: Uint8Array) => Uint8Array;
}

/** A random (version 4) UUID: from the runtime's Web Crypto, or Node's crypto where that is not global (Node 18). */
export async function uuid4(): Promise<string> {
  const c = (globalThis as { crypto?: WebCrypto }).crypto;
  if (typeof c?.randomUUID === "function") return c.randomUUID();
  let bytes: Uint8Array;
  if (typeof c?.getRandomValues === "function") {
    bytes = c.getRandomValues(new Uint8Array(16));
  } else {
    const nodeCrypto = (await load("node:crypto")) as { randomBytes(n: number): Uint8Array };
    bytes = new Uint8Array(nodeCrypto.randomBytes(16));
  }
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export interface FileStats {
  size: number;
  isFile(): boolean;
}

export interface FileHandle {
  stat(): Promise<FileStats>;
  read(buffer: Uint8Array, offset: number, length: number, position: number): Promise<{ bytesRead: number }>;
  write(buffer: Uint8Array, offset: number, length: number): Promise<{ bytesWritten: number }>;
  close(): Promise<void>;
}

export interface FileSystem {
  open(path: string, flags: string, mode?: number): Promise<FileHandle>;
  rename(from: string, to: string): Promise<void>;
  rm(path: string, options: { force: boolean }): Promise<void>;
}

export interface Paths {
  resolve(...parts: string[]): string;
  dirname(path: string): string;
  basename(path: string): string;
  join(...parts: string[]): string;
}

async function load(name: string): Promise<unknown> {
  // a name in a variable keeps bundlers for the web from pulling Node's modules in
  const specifier = name;
  return import(specifier);
}

/** Node's file system and paths, for files on this computer. */
export async function nodeFiles(): Promise<{ fs: FileSystem; path: Paths }> {
  try {
    const [fs, path] = await Promise.all([load("node:fs/promises"), load("node:path")]);
    return { fs: fs as FileSystem, path: path as Paths };
  } catch {
    throw new WheelmachineError(
      "a local path is read and written with Node's file system, which this runtime does not have: give bytes, a Blob or a stream",
    );
  }
}
