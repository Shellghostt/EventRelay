import { readFileSync } from "node:fs";

const lock = JSON.parse(
  readFileSync(new URL("../package-lock.json", import.meta.url), "utf8"),
);
const allowed = new Set([
  "MIT",
  "ISC",
  "Apache-2.0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "0BSD",
  "CC0-1.0",
  "BlueOak-1.0.0",
  "Python-2.0",
  "(MIT OR CC0-1.0)",
  "MIT OR Apache-2.0",
]);
let count = 0;
const failures = [];
for (const [path, pkg] of Object.entries(lock.packages)) {
  if (!path) continue;
  count++;
  const mplTool =
    pkg.license === "MPL-2.0" &&
    /^node_modules\/(?:@axe-core\/playwright|axe-core|lightningcss(?:-[^/]+)?)$/.test(
      path,
    );
  if (!allowed.has(pkg.license) && !mplTool)
    failures.push(
      `${path}@${pkg.version}: ${pkg.license ?? "license missing"}`,
    );
}
if (failures.length) {
  process.stderr.write(`${failures.join("\n")}\n`);
  process.exitCode = 1;
} else
  process.stdout.write(
    `Reviewed ${count} locked packages: declared licenses match the documented allowlist, including MPL-2.0 development tools.\n`,
  );
