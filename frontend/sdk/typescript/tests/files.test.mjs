// Files in and out of a machine: streamed with their length, read whole or into a file.

import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { createReadStream } from "node:fs";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import { test } from "node:test";

import { BadRequestError, NotFoundError, UnavailableError } from "../dist/index.js";
import { setup } from "./fake-api.mjs";

async function rig(t) {
  const s = await setup(t);
  const machine = await s.client.machines.create();
  const dir = await mkdtemp(join(tmpdir(), "wheelmachine-test-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  return { ...s, machine, dir };
}

const lastPut = (api) => api.requests("PUT", "/files$").at(-1);

test("bytes go in and come back", async (t) => {
  const { api, client, machine } = await rig(t);
  assert.equal(await client.machines.writeFile(machine.id, "work/notes.txt", new TextEncoder().encode("hello\n")), undefined);
  const seen = lastPut(api);
  assert.equal(seen.query.get("path"), "work/notes.txt");
  assert.equal(seen.headers["content-length"], "6");
  assert.equal(seen.headers["content-type"], "application/octet-stream");
  assert.equal(seen.headers["transfer-encoding"], undefined);
  const back = await client.machines.readFile(machine.id, "work/notes.txt");
  assert.ok(back instanceof Uint8Array);
  assert.equal(new TextDecoder().decode(back), "hello\n");
});

test("a file on this computer streams from disk with its length", async (t) => {
  const { api, client, machine, dir } = await rig(t);
  const data = randomBytes(300_000);
  const local = join(dir, "data.bin");
  await writeFile(local, data);
  await client.machines.writeFile(machine.id, "home/data.bin", { path: local });
  const seen = lastPut(api);
  assert.equal(seen.headers["content-length"], String(data.length));
  assert.equal(seen.headers["transfer-encoding"], undefined);
  assert.ok(api.files.get(`${machine.id} home/data.bin`).equals(data));
});

test("a Blob, an ArrayBuffer and a Node stream with its length", async (t) => {
  const { api, client, machine, dir } = await rig(t);
  await client.machines.writeFile(machine.id, "work/blob", new Blob(["from a blob"]));
  assert.equal(api.files.get(`${machine.id} work/blob`).toString(), "from a blob");
  await client.machines.writeFile(machine.id, "work/ab", new Uint8Array([7, 8, 9]).buffer);
  assert.deepEqual([...api.files.get(`${machine.id} work/ab`)], [7, 8, 9]);
  const local = join(dir, "s.bin");
  const data = randomBytes(150_000);
  await writeFile(local, data);
  await client.machines.writeFile(machine.id, "work/s.bin", createReadStream(local), { contentLength: data.length });
  assert.ok(api.files.get(`${machine.id} work/s.bin`).equals(data));
  assert.equal(lastPut(api).headers["content-length"], String(data.length));
});

test("a stream needs its length, and one already read is not sent again", async (t) => {
  const { api, client, machine, sleeps } = await rig(t);
  await assert.rejects(client.machines.writeFile(machine.id, "work/x", Readable.from([Buffer.from("abc")])), TypeError);
  const web = new ReadableStream({
    start(c) {
      c.enqueue(new TextEncoder().encode("abc"));
      c.close();
    },
  });
  await client.machines.writeFile(machine.id, "work/x", web, { contentLength: 3 });
  assert.equal(api.files.get(`${machine.id} work/x`).toString(), "abc");
  api.fault("PUT", "/files$", { status: 503, code: "unavailable", retryAfter: 1 });
  await assert.rejects(
    client.machines.writeFile(machine.id, "work/z", Readable.from([Buffer.from("once")]), { contentLength: 4 }),
    UnavailableError,
  );
  assert.deepEqual(sleeps, []);
});

test("bytes and files are sent again after a temporary error", async (t) => {
  const { api, client, machine, sleeps, dir } = await rig(t);
  api.fault("PUT", "/files$", { status: 503, code: "unavailable", retryAfter: 1 });
  await client.machines.writeFile(machine.id, "work/y", new TextEncoder().encode("again"));
  assert.equal(api.files.get(`${machine.id} work/y`).toString(), "again");
  const local = join(dir, "again.bin");
  await writeFile(local, "from disk, twice");
  api.fault("PUT", "/files$", { status: 503, code: "unavailable", retryAfter: 1 });
  await client.machines.writeFile(machine.id, "work/again", { path: local });
  assert.equal(api.files.get(`${machine.id} work/again`).toString(), "from disk, twice");
  assert.deepEqual(sleeps, [1000, 1000]);
});

test("text and other things are refused before anything is sent", async (t) => {
  const { api, client, machine, dir } = await rig(t);
  await assert.rejects(client.machines.writeFile(machine.id, "work/x", "some text"), TypeError);
  await assert.rejects(client.machines.writeFile(machine.id, "work/x", 12), TypeError);
  await assert.rejects(client.machines.writeFile(machine.id, "work/x", new Uint8Array(3), { contentLength: 5 }), TypeError);
  await assert.rejects(client.machines.writeFile(machine.id, "work/x", { path: join(dir, "missing") }), /ENOENT/);
  assert.deepEqual(api.requests("PUT", "/files$"), []);
});

test("reading into a path writes it whole", async (t) => {
  const { api, client, machine, dir } = await rig(t);
  const data = randomBytes(500_000);
  api.files.set(`${machine.id} work/out.bin`, data);
  const dest = join(dir, "out.bin");
  assert.equal(await client.machines.readFile(machine.id, "work/out.bin", { to: dest }), data.length);
  assert.ok((await readFile(dest)).equals(data));
  assert.deepEqual(await readdir(dir), ["out.bin"]);
  assert.equal(api.seen.at(-1).headers.accept, "application/octet-stream");
});

test("a file that is not there leaves nothing behind", async (t) => {
  const { client, machine, dir } = await rig(t);
  const e = await client.machines.readFile(machine.id, "work/none", { to: join(dir, "none.bin") }).catch((err) => err);
  assert.ok(e instanceof NotFoundError);
  assert.equal(e.detail, "no such file in the machine");
  assert.deepEqual(await readdir(dir), []);
});

test("reading into a WritableStream", async (t) => {
  const { api, client, machine } = await rig(t);
  api.files.set(`${machine.id} home/a`, Buffer.from("abc".repeat(1000)));
  const parts = [];
  const out = new WritableStream({
    write(chunk) {
      parts.push(Buffer.from(chunk));
    },
  });
  assert.equal(await client.machines.readFile(machine.id, "home/a", { to: out }), 3000);
  assert.equal(Buffer.concat(parts).toString(), "abc".repeat(1000));
});

test("a path outside the two folders is refused", async (t) => {
  const { client, machine } = await rig(t);
  await assert.rejects(client.machines.readFile(machine.id, "etc/passwd"), BadRequestError);
});

test("an error before the body is the answer", async (t) => {
  const { client } = await rig(t);
  // the API refuses a write to a machine that is not there before it reads the body, and closes
  const e = await client.machines.writeFile(`m_${"7".repeat(26)}`, "work/big", randomBytes(8 << 20)).catch((err) => err);
  assert.ok(e instanceof NotFoundError, String(e));
});
