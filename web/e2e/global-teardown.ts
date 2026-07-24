import { spawnSync } from "node:child_process";
import fs from "node:fs";
import { PID_FILE } from "./constants";

export default async function globalTeardown() {
  try {
    const pid = Number(fs.readFileSync(PID_FILE, "utf8").trim());
    if (pid) {
      console.log(`[e2e] stopping node pid ${pid}`);
      if (process.platform === "win32") {
        spawnSync("taskkill", ["/pid", String(pid), "/t", "/f"], { encoding: "utf8" });
      } else {
        process.kill(pid, "SIGTERM");
      }
    }
    fs.rmSync(PID_FILE, { force: true });
  } catch (e) {
    console.warn("[e2e] teardown: could not stop node:", String(e));
  }
}
