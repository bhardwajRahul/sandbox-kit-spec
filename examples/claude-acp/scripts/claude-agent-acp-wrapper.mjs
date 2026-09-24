#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { createInterface } from "node:readline";

const adapter =
  "/opt/claude-agent-acp/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js";
const child = spawn(process.execPath, [adapter, ...process.argv.slice(2)], {
  env: process.env,
  stdio: ["pipe", "pipe", "inherit"],
});

const exited = new Promise((resolve, reject) => {
  child.once("error", reject);
  child.once("exit", (code, signal) => resolve({ code, signal }));
});

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.once(signal, () => child.kill(signal));
}

function cliIsAuthenticated() {
  const executable =
    process.env.CLAUDE_CODE_EXECUTABLE || "/usr/local/bin/claude";
  const probe = spawnSync(executable, ["auth", "status", "--json"], {
    encoding: "utf8",
    env: process.env,
    timeout: 5_000,
  });

  if (probe.status !== 0) return false;
  try {
    return JSON.parse(probe.stdout).loggedIn === true;
  } catch {
    return false;
  }
}

function isFalseLoggedOutNotification(line) {
  try {
    const message = JSON.parse(line);
    return (
      message.method === "_auth/status_update" &&
      message.params?.authStatus?.kind === "none" &&
      cliIsAuthenticated()
    );
  } catch {
    return false;
  }
}

function translateClientMessage(line) {
  try {
    const message = JSON.parse(line);
    if (message.method !== "session/set_model") return line;

    message.method = "session/set_config_option";
    message.params = {
      sessionId: message.params?.sessionId,
      configId: "model",
      value: message.params?.modelId,
    };
    return JSON.stringify(message);
  } catch {
    return line;
  }
}

async function forwardInput() {
  const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
  for await (const line of lines) {
    const translated = translateClientMessage(line);
    if (!child.stdin.write(`${translated}\n`))
      await once(child.stdin, "drain");
  }
  child.stdin.end();
}

async function forwardOutput() {
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  for await (const line of lines) {
    if (isFalseLoggedOutNotification(line)) continue;
    if (!process.stdout.write(`${line}\n`))
      await once(process.stdout, "drain");
  }
}

void forwardInput();
const output = forwardOutput();
const { code, signal } = await exited;
await output;
process.exitCode = code ?? (signal ? 1 : 0);
