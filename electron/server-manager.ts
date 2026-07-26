/**
 * Server process lifecycle manager.
 *
 * Handles spawning the Go backend, waiting for it to become
 * ready (health check polling), and graceful shutdown on app quit.
 *
 * In dev mode: spawns the pre-built Go binary from server/
 * In production: spawns the bundled Go binary from resources
 */

import { ChildProcess, spawn } from 'child_process';
import { getServerExecutable, getServerCwd, isDev, SERVER_HOST, getServerPort, setServerPort, findFreePort, getDataRoot, getLogRoot, getLogLevel } from './config';
import http from 'http';
import { existsSync } from 'fs';

let serverProcess: ChildProcess | null = null;

function healthCheck(host: string, port: number, maxRetries: number = 15, intervalMs: number = 1000): Promise<void> {
  return new Promise((resolve, reject) => {
    let attempts = 0;

    const check = () => {
      attempts++;
      const req = http.get(
        `http://${host}:${port}/`,
        { timeout: 2000 },
        (res) => {
          if (res.statusCode === 200) {
            resolve();
          } else if (attempts < maxRetries) {
            setTimeout(check, intervalMs);
          } else {
            reject(new Error(`Server returned status ${res.statusCode} after ${maxRetries} attempts`));
          }
        }
      );

      req.on('error', () => {
        if (attempts < maxRetries) {
          setTimeout(check, intervalMs);
        } else {
          reject(new Error(`Server not ready after ${maxRetries} attempts`));
        }
      });

      req.on('timeout', () => {
        req.destroy();
        if (attempts < maxRetries) {
          setTimeout(check, intervalMs);
        } else {
          reject(new Error(`Server health check timed out after ${maxRetries} attempts`));
        }
      });
    };

    check();
  });
}

const MAX_PORT_RETRIES = 3;

export async function startServer(): Promise<void> {
  for (let attempt = 0; attempt < MAX_PORT_RETRIES; attempt++) {
    const port = await findFreePort();
    setServerPort(port);

    const exe = getServerExecutable();
    const cwd = getServerCwd();

    console.log(`[Server] Spawning server (attempt ${attempt + 1}/${MAX_PORT_RETRIES}):`);
    console.log(`[Server]   exe=${exe}`);
    console.log(`[Server]   cwd=${cwd}`);
    console.log(`[Server]   dev=${isDev()}, port=${port}, platform=${process.platform}`);

    if (!existsSync(exe)) {
      throw new Error(`Server executable not found: ${exe}. Run: cd server && go build -o qingqiu-world-server ./cmd/`);
    }

    const args: string[] = [];

    console.log(`[Server]   args=${args.join(' ') || '(none)'}`);

    const env = {
      ...process.env,
      PORT: String(port),
      DATA_ROOT: getDataRoot(),
      LOG_DIR: getLogRoot(),
      LOG_LEVEL: getLogLevel(),
    };

    let spawnError: Error | null = null;

    serverProcess = spawn(exe, args, {
      cwd,
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    serverProcess.stdout?.on('data', (data: Buffer) => {
      console.log(`[Server] stdout: ${data.toString().trim()}`);
    });

    serverProcess.stderr?.on('data', (data: Buffer) => {
      console.error(`[Server] stderr: ${data.toString().trim()}`);
    });

    serverProcess.on('error', (err) => {
      spawnError = err;
      console.error(`[Server] spawn error: ${err.message} (code=${(err as NodeJS.ErrnoException).code})`);
    });

    serverProcess.on('exit', (code, signal) => {
      if (code !== null && code !== 0) {
        console.error(`[Server] exited with code ${code}, signal ${signal}`);
      }
      serverProcess = null;
    });

    try {
      await healthCheck(SERVER_HOST, port);
      console.log(`Server is ready on port ${port}`);
      return;
    } catch (err) {
      const cause = spawnError || err;
      console.warn(`[Server] Health check failed on port ${port}:`, cause);
      stopServer();
      if (attempt < MAX_PORT_RETRIES - 1) {
        console.log(`[Server] Retrying with a new port...`);
      } else {
        throw new Error(`Failed to start server after ${MAX_PORT_RETRIES} attempts. Last error: ${cause instanceof Error ? cause.message : String(cause)}`);
      }
    }
  }
}

/** Stops the server process gracefully with a SIGTERM, falling back to SIGKILL after 12 seconds. */
export function stopServer(): void {
  if (!serverProcess) {
    return;
  }

  if (process.platform === 'win32') {
    serverProcess.kill();
    serverProcess = null;
  } else {
    serverProcess.kill('SIGTERM');

    const forceKillTimer = setTimeout(() => {
      if (serverProcess) {
        serverProcess.kill('SIGKILL');
        serverProcess = null;
      }
    }, 12000);

    serverProcess.on('exit', () => {
      clearTimeout(forceKillTimer);
      serverProcess = null;
    });
  }
}

/** Returns whether the server process is currently running. */
export function isServerRunning(): boolean {
  return serverProcess !== null;
}
