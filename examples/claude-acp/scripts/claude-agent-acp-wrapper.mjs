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

const exited = new Promise((resolve) => {
  child.once("error", (error) => {
    console.error(`claude-agent-acp: ${error.message}`);
    resolve({ code: 1, signal: null });
  });
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

const inputLines = createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});

async function forwardInput() {
  try {
    for await (const line of inputLines) {
      const translated = translateClientMessage(line);
      if (!child.stdin.write(`${translated}\n`))
        await once(child.stdin, "drain");
    }
  } catch (error) {
    if (child.exitCode === null && child.signalCode === null) throw error;
  } finally {
    if (!child.stdin.destroyed) child.stdin.end();
  }
}

async function forwardOutput() {
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  for await (const line of lines) {
    if (isFalseLoggedOutNotification(line)) continue;
    if (!process.stdout.write(`${line}\n`))
      await once(process.stdout, "drain");
  }
}

const input = forwardInput();
const output = forwardOutput();
const { code, signal } = await exited;
inputLines.close();
process.stdin.pause();
await Promise.all([input, output]);
process.exitCode = code ?? (signal ? 1 : 0);
