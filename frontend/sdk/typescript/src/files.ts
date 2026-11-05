/** Files: what `writeFile` sends, with its length, and how `readFile` streams an answer into a file. */

import { APIConnectionError, APITimeoutError, WheelmachineError } from "./errors.js";
import { nodeFiles, uuid4, type FileHandle } from "./runtime.js";

/**
 * What `writeFile` sends: bytes, a Blob, a file on this computer (`{ path: "data.csv" }`, streamed from disk), or a
 * ReadableStream or async iterable of bytes (a Node stream, say), which needs `contentLength`.
 */
export type FileData =
  | Uint8Array
  | ArrayBuffer
  | ArrayBufferView
  | Blob
  | ReadableStream<Uint8Array>
  | AsyncIterable<Uint8Array>
  | { path: string };

/** A body ready for one attempt. */
export interface Prepared {
  body: Uint8Array | Blob | ReadableStream<Uint8Array>;
  length: number;
  /** A stream body, which fetch sends as it reads it. */
  streaming: boolean;
  close(): Promise<void>;
}

/** A request's body, which can be prepared again for another attempt when `canRepeat()` says so. */
export interface BodySource {
  prepare(): Promise<Prepared>;
  canRepeat(): boolean;
}

const CHUNK = 64 * 1024;
const noop = async (): Promise<void> => {};

class BytesSource implements BodySource {
  readonly #body: Uint8Array | Blob;
  readonly #length: number;

  constructor(body: Uint8Array | Blob, length: number) {
    this.#body = body;
    this.#length = length;
  }

  async prepare(): Promise<Prepared> {
    return { body: this.#body, length: this.#length, streaming: false, close: noop };
  }

  canRepeat(): boolean {
    return true;
  }
}

/** A file on this computer, streamed from disk with its length; opened afresh for each attempt. */
class PathSource implements BodySource {
  readonly #path: string;

  constructor(path: string) {
    this.#path = path;
  }

  async prepare(): Promise<Prepared> {
    const { fs } = await nodeFiles();
    const where = this.#path;
    const handle = await fs.open(where, "r");
    let size: number;
    try {
      const stats = await handle.stat();
      if (!stats.isFile()) throw new WheelmachineError(`not a file: ${where}`);
      size = stats.size;
    } catch (err) {
      await handle.close();
      throw err;
    }
    let closed = false;
    const close = async (): Promise<void> => {
      if (closed) return;
      closed = true;
      await handle.close().catch(() => undefined);
    };
    let sent = 0;
    const body = new ReadableStream<Uint8Array>(
      {
        async pull(controller) {
          if (sent >= size) {
            controller.close();
            await close();
            return;
          }
          const buffer = new Uint8Array(Math.min(CHUNK, size - sent));
          const { bytesRead } = await handle.read(buffer, 0, buffer.byteLength, sent);
          if (bytesRead === 0) {
            await close();
            controller.error(new WheelmachineError(`the file ${where} ended after ${sent} of its ${size} bytes`));
            return;
          }
          sent += bytesRead;
          controller.enqueue(bytesRead === buffer.byteLength ? buffer : buffer.subarray(0, bytesRead));
        },
        async cancel() {
          await close();
        },
      },
      { highWaterMark: 0 },
    );
    return { body, length: size, streaming: true, close };
  }

  canRepeat(): boolean {
    return true;
  }
}

/** A stream the caller made: sent again only if nothing was read from it yet. */
class StreamSource implements BodySource {
  readonly #next: () => Promise<IteratorResult<unknown>>;
  readonly #stop: () => Promise<void>;
  readonly #length: number;
  #touched = false;

  constructor(data: ReadableStream<Uint8Array> | AsyncIterable<Uint8Array>, length: number) {
    this.#length = length;
    if (isReadableStream(data)) {
      let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
      this.#next = () => {
        reader ??= data.getReader();
        return reader.read() as Promise<IteratorResult<unknown>>;
      };
      this.#stop = async () => {
        await reader?.cancel().catch(() => undefined);
      };
    } else {
      let iterator: AsyncIterator<Uint8Array> | undefined;
      this.#next = () => {
        iterator ??= data[Symbol.asyncIterator]();
        return iterator.next();
      };
      this.#stop = async () => {
        await iterator?.return?.().catch(() => undefined);
      };
    }
  }

  async prepare(): Promise<Prepared> {
    const next = this.#next;
    const stop = this.#stop;
    const touch = (): void => {
      this.#touched = true;
    };
    const body = new ReadableStream<Uint8Array>(
      {
        async pull(controller) {
          touch();
          const { value, done } = await next();
          if (done) {
            controller.close();
            return;
          }
          if (!(value instanceof Uint8Array)) {
            controller.error(new WheelmachineError("the stream gave something other than bytes (Uint8Array chunks)"));
            await stop();
            return;
          }
          controller.enqueue(value);
        },
        async cancel() {
          await stop();
        },
      },
      { highWaterMark: 0 },
    );
    return { body, length: this.#length, streaming: true, close: noop };
  }

  canRepeat(): boolean {
    return !this.#touched;
  }
}

function isReadableStream(v: unknown): v is ReadableStream<Uint8Array> {
  return typeof v === "object" && v !== null && typeof (v as { getReader?: unknown }).getReader === "function";
}

function isAsyncIterable(v: unknown): v is AsyncIterable<Uint8Array> {
  return typeof v === "object" && v !== null && typeof (v as { [Symbol.asyncIterator]?: unknown })[Symbol.asyncIterator] === "function";
}

function sameLength(given: number | undefined, actual: number): void {
  if (given !== undefined && given !== actual) {
    throw new TypeError(`contentLength says ${given} bytes, the data holds ${actual}`);
  }
}

/** The body for `writeFile`'s data. */
export function fileBody(data: FileData, contentLength?: number): BodySource {
  if (typeof data === "string") {
    throw new TypeError(
      'writeFile takes bytes, a Blob, a stream, or a file on this computer as { path: "notes.txt" }; for text, give new TextEncoder().encode(text)',
    );
  }
  if (contentLength !== undefined && (!Number.isSafeInteger(contentLength) || contentLength < 0)) {
    throw new TypeError("contentLength is a whole number of bytes, 0 or more");
  }
  if (data instanceof Uint8Array) {
    sameLength(contentLength, data.byteLength);
    return new BytesSource(data, data.byteLength);
  }
  if (data instanceof ArrayBuffer) {
    sameLength(contentLength, data.byteLength);
    return new BytesSource(new Uint8Array(data), data.byteLength);
  }
  if (ArrayBuffer.isView(data)) {
    sameLength(contentLength, data.byteLength);
    return new BytesSource(new Uint8Array(data.buffer, data.byteOffset, data.byteLength), data.byteLength);
  }
  if (typeof Blob !== "undefined" && data instanceof Blob) {
    sameLength(contentLength, data.size);
    return new BytesSource(data, data.size);
  }
  if (isReadableStream(data) || isAsyncIterable(data)) {
    if (contentLength === undefined) {
      throw new TypeError(
        'a stream\'s length is not known: give it as contentLength, or give a file on this computer as { path: "..." }',
      );
    }
    return new StreamSource(data, contentLength);
  }
  if (typeof data === "object" && data !== null && typeof (data as { path?: unknown }).path === "string") {
    if (contentLength !== undefined) {
      throw new TypeError("contentLength is for a stream; a file's length is its own");
    }
    return new PathSource((data as { path: string }).path);
  }
  throw new TypeError('writeFile takes bytes, a Blob, a stream, or a file on this computer as { path: "notes.txt" }');
}

// --- reading an answer into a file -------------------------------------------------------------------------------

/** An answer's body, a piece at a time, each piece coming within `idleMs` or the call ends as a timeout. */
export async function* pieces(response: Response, idleMs: number, where: string): AsyncGenerator<Uint8Array> {
  if (response.body === null) return;
  const reader = response.body.getReader();
  try {
    for (;;) {
      let timer: ReturnType<typeof setTimeout> | undefined;
      const idle = new Promise<"idle">((resolve) => {
        timer = setTimeout(() => resolve("idle"), idleMs);
      });
      let got: ReadableStreamReadResult<Uint8Array> | "idle";
      try {
        got = await Promise.race([reader.read(), idle]);
      } catch (err) {
        throw new APIConnectionError(`the connection to ${where} was lost while the file came`, { cause: err });
      } finally {
        clearTimeout(timer);
      }
      if (got === "idle") {
        throw new APITimeoutError(`nothing came from ${where} for ${idleMs / 1000} seconds while the file came`);
      }
      if (got.done) return;
      yield got.value;
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}

/**
 * Streams an answer into a file beside `dest`, which takes `dest`'s name once the whole file is in. A new file is made
 * with `mode` (as the umask leaves it): readable by everyone by default, as a copy usually is.
 */
export async function saveTo(response: Response, dest: string, idleMs: number, where: string, mode = 0o666): Promise<number> {
  let files: Awaited<ReturnType<typeof nodeFiles>>;
  try {
    files = await nodeFiles();
  } catch (err) {
    await response.body?.cancel().catch(() => undefined);
    throw err;
  }
  const { fs, path } = files;
  const full = path.resolve(dest);
  const part = path.join(path.dirname(full), `.${path.basename(full)}.${(await uuid4()).slice(0, 12)}.part`);
  let handle: FileHandle;
  try {
    handle = await fs.open(part, "wx", mode);
  } catch (err) {
    await response.body?.cancel().catch(() => undefined);
    throw err;
  }
  let n = 0;
  try {
    for await (const piece of pieces(response, idleMs, where)) {
      for (let at = 0; at < piece.byteLength; ) {
        const { bytesWritten } = await handle.write(piece, at, piece.byteLength - at);
        at += bytesWritten;
      }
      n += piece.byteLength;
    }
    await handle.close();
    await fs.rename(part, full);
    return n;
  } catch (err) {
    await handle.close().catch(() => undefined);
    await fs.rm(part, { force: true }).catch(() => undefined);
    throw err;
  }
}

/** Streams an answer into a WritableStream, which is left open; gives how many bytes came. */
export async function writeTo(response: Response, out: WritableStream<Uint8Array>, idleMs: number, where: string): Promise<number> {
  const writer = out.getWriter();
  let n = 0;
  try {
    for await (const piece of pieces(response, idleMs, where)) {
      await writer.write(piece);
      n += piece.byteLength;
    }
    return n;
  } finally {
    writer.releaseLock();
  }
}
