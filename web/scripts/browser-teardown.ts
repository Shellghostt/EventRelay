import { readFile } from "node:fs/promises";
import { setTimeout } from "node:timers/promises";

export default async function teardown() {
  const response = await fetch("http://127.0.0.1:58084/__test/shutdown", {
    method: "POST",
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok) throw new Error("Browser fixture shutdown was rejected");

  const completion = new URL(
    "../../tests/browser/.runtime/browser-server-exit.json",
    import.meta.url,
  );
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    let result: string;
    try {
      result = await readFile(completion, "utf8");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      await setTimeout(100);
      continue;
    }
    const { code } = JSON.parse(result) as { code: number };
    if (code !== 0)
      throw new Error(`Browser fixture cleanup failed with exit code ${code}`);
    return;
  }
  throw new Error("Browser fixture did not finish cleanup within 30 seconds");
}
