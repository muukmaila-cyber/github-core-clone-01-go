// Entry point for the github-core-clone-01 Go forge.
//
// The runtime runs `node /bot/main.ts`. This file only spawns the vendored,
// statically linked Go binary next to it (`./forge`) and forwards its exit
// code. The binary itself owns the whole lifecycle: it binds an ephemeral
// port, registers it with $MAKEFUN_ENV_URL, serves the forge until the env
// says the trial is done, then exits 0.
//
// The binary is built from the Go sources in this repo (see README.md):
//   CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o forge .

import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const dir = path.dirname(fileURLToPath(import.meta.url));
const bin = path.join(dir, 'forge');

const child = spawn(bin, [], { stdio: 'inherit', env: process.env });
child.on('error', (err) => {
  process.stderr.write(`[entry] cannot start ./forge: ${err.message}\n`);
  process.exit(1);
});
child.on('exit', (code, signal) => {
  if (signal) process.exit(1);
  process.exit(code ?? 0);
});
