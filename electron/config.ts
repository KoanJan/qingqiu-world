/**
 * Application configuration constants for Electron main process.
 *
 * Centralizes paths, ports, and environment detection used by
 * the main process and server manager.
 */

import { app } from 'electron';
import path from 'path';
import net from 'net';

/** The application display name. */
export const APP_NAME = 'Qingqiu World';

/** The server listen hostname. */
export const SERVER_HOST = '127.0.0.1';

let assignedPort: number | null = null;

export async function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, SERVER_HOST, () => {
      const addr = server.address();
      if (addr && typeof addr !== 'string' && addr.port) {
        const port = addr.port;
        server.close(() => resolve(port));
      } else {
        server.close();
        reject(new Error('Failed to get address from server'));
      }
    });
    server.on('error', (err: NodeJS.ErrnoException) => {
      if (err.code === 'EACCES' || err.code === 'EADDRNOTAVAIL') {
        server.listen(0, () => {
          const addr = server.address();
          if (addr && typeof addr !== 'string' && addr.port) {
            const port = addr.port;
            server.close(() => resolve(port));
          } else {
            server.close();
            reject(new Error('Failed to get address from server'));
          }
        });
      } else {
        reject(err);
      }
    });
  });
}

/** Returns the assigned server port, defaulting to 8000 if not set. */
export function getServerPort(): number {
  return assignedPort ?? 8000;
}

/** Sets the server port to the given value. */
export function setServerPort(port: number): void {
  assignedPort = port;
}

/** Returns whether the app is running in development mode (unpackaged). */
export function isDev(): boolean {
  return !app.isPackaged;
}

/** Returns the project root directory path, resolving differently in dev vs production. */
export function getProjectRoot(): string {
  if (isDev()) {
    return path.resolve(__dirname, '..', '..');
  }
  return path.dirname(app.getPath('exe'));
}

/** Returns the path to the server executable binary, located in server/ under project root or resources. */
export function getServerExecutable(): string {
  if (isDev()) {
    const exeName = process.platform === 'win32' ? 'qingqiu-world-server.exe' : 'qingqiu-world-server';
    return path.join(getProjectRoot(), 'server', exeName);
  }
  const exeName = process.platform === 'win32' ? 'qingqiu-world-server.exe' : 'qingqiu-world-server';
  return path.join(process.resourcesPath, 'server', exeName);
}

/** Returns the working directory for spawning the server process. */
export function getServerCwd(): string {
  if (isDev()) {
    return path.join(getProjectRoot(), 'server');
  }
  return path.join(process.resourcesPath, 'server');
}

/** Returns the web distribution path (dev server URL in dev mode, or bundled index.html in production). */
export function getWebDistPath(): string {
  if (isDev()) {
    return `http://localhost:5173`;
  }
  return path.resolve(__dirname, '..', '..', 'web-dist', 'index.html');
}

/** Returns the full server URL using the assigned host and port. */
export function getServerUrl(): string {
  return `http://${SERVER_HOST}:${getServerPort()}`;
}

/** Returns the data root directory, injected as DATA_ROOT env var when spawning the Go server. */
export function getDataRoot(): string {
  return path.join(app.getPath('userData'), 'data');
}

/** Returns the log directory, injected as LOG_DIR env var when spawning the Go server. */
export function getLogRoot(): string {
  return path.join(app.getPath('userData'), 'logs');
}

/** Returns the log level for the Go server (DEBUG in dev mode, INFO in production). */
export function getLogLevel(): string {
  return isDev() ? 'DEBUG' : 'INFO';
}

/** Checks and resets data on pre-release version changes to avoid schema migration issues. */
export function checkPreReleaseDataReset(): void {
  if (isDev()) return; // Skip in dev mode

  const currentVersion = app.getVersion();
  const isPreRelease = currentVersion.startsWith('0.0.');
  if (!isPreRelease) return;

  const dataRoot = getDataRoot();
  const versionFile = path.join(dataRoot, '.data-version');
  const fs = require('fs');

  let storedVersion = '';
  try {
    storedVersion = fs.readFileSync(versionFile, 'utf8').trim();
  } catch {
    // Version file doesn't exist yet — first run, no data to wipe
  }

  if (storedVersion === currentVersion) return;

  // Version changed in 0.0.x — wipe data directory
  if (storedVersion && fs.existsSync(dataRoot)) {
    console.log(`[Config] Pre-release version changed (${storedVersion} → ${currentVersion}), wiping data directory: ${dataRoot}`);
    fs.rmSync(dataRoot, { recursive: true, force: true });
  }

  // Ensure data directory exists and write current version
  fs.mkdirSync(dataRoot, { recursive: true });
  fs.writeFileSync(versionFile, currentVersion, 'utf8');
  console.log('[Config] Data version marker set to:', currentVersion);
}
