// Writes src/version.ts from package.json, the one place the library's version is kept. npm run build runs it before
// tsc, so the built library, and the User-Agent it sends, carry the version npm publishes it under; the tests fail on a
// version.ts left behind by a change to package.json.
import { readFile, writeFile } from "node:fs/promises";

const root = new URL("../", import.meta.url);
const { version } = JSON.parse(await readFile(new URL("package.json", root), "utf8"));
const file = new URL("src/version.ts", root);
const text = `// Written from package.json by scripts/version.mjs (npm run build): change the version there.
export const VERSION = ${JSON.stringify(version)};
`;
if ((await readFile(file, "utf8").catch(() => "")) !== text) {
  await writeFile(file, text);
}
