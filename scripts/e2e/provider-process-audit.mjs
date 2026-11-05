// Read-only process metadata audit. Never print/store arguments, environments,
// credentials, or details from unrelated processes; return only scoped PIDs.
import { spawnSync } from "node:child_process";
import { resolve } from "node:path";

export function auditProviderProcesses(directory) {
  const root = resolve(directory).replace(/^\/private\/var\//, "/var/");
  const scoped = (value) => value.replaceAll("/private/var/", "/var/").includes(root + "/");
  const snapshot = spawnSync("ps", ["-axo", "pid=,ppid=,comm="], { encoding: "utf8", timeout: 5000, maxBuffer: 4 * 1024 * 1024 });
  if (snapshot.status !== 0) throw new Error("process metadata audit failed");
  const processes = snapshot.stdout.trim().split("\n").flatMap((line) => {
    const match = line.trim().match(/^(\d+)\s+(\d+)\s+(.+)$/);
    return match ? [{ pid: Number(match[1]), parent: Number(match[2]), executable: match[3] }] : [];
  });
  const ancestors = new Set();
  for (let pid = process.pid; pid > 0 && !ancestors.has(pid); pid = processes.find((p) => p.pid === pid)?.parent ?? 0) ancestors.add(pid);
  const matches = [];
  for (const item of processes) {
    if (ancestors.has(item.pid)) continue;
    const native = /(?:^|\/)(?:codex|opencode|node)(?:$|\s)/.test(item.executable);
    let match = scoped(item.executable);
    if (!match && native) {
      const args = spawnSync("ps", ["-p", String(item.pid), "-o", "args="], { encoding: "utf8", timeout: 2000, maxBuffer: 1024 * 1024 });
      match = scoped(args.stdout ?? "");
      if (!match && /(?:codex|opencode)/.test(item.executable)) {
        const files = spawnSync("lsof", ["-nP", "-p", String(item.pid), "-Fn"], { encoding: "utf8", timeout: 3000, maxBuffer: 4 * 1024 * 1024 });
        match = scoped(files.stdout ?? "");
      }
    }
    if (match) matches.push({ pid: item.pid, parent: item.parent });
  }
  return matches;
}
