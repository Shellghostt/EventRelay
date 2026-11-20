import { spawn } from "node:child_process";
import { mkdirSync, renameSync, rmSync, writeFileSync } from "node:fs";

const runtime = new URL("../../tests/browser/.runtime/", import.meta.url);
const completion = new URL("browser-server-exit.json", runtime);
const temporaryCompletion = new URL("browser-server-exit.tmp", runtime);
mkdirSync(runtime, { recursive: true });
rmSync(completion, { force: true });
rmSync(temporaryCompletion, { force: true });

const child = spawn("go", ["run", "./tests/browser"], {
  cwd: new URL("../../", import.meta.url),
  stdio: "inherit",
  windowsHide: true,
  env: { ...process.env, GOTOOLCHAIN: "go1.26.11" },
});
child.on("exit", (code) => {
  // Go exits only after its deferred worker, Redis and schema cleanup finishes.
  writeFileSync(temporaryCompletion, JSON.stringify({ code: code ?? 1 }));
  renameSync(temporaryCompletion, completion);
  process.exitCode = code ?? 1;
});
let stopping = false;
const stop = async () => {
  if (stopping) return;
  stopping = true;
  try {
    await fetch("http://127.0.0.1:58084/__test/shutdown", {
      method: "POST",
      signal: AbortSignal.timeout(5000),
    });
  } catch {
    child.kill();
  }
};
process.on("SIGINT", () => {
  void stop();
});
process.on("SIGTERM", () => {
  void stop();
});
