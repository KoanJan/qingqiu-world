# background-upload-restart: Uploaded background survives a restart, then deletes

This deterministic case verifies the server's background-image API across two real server processes. It checks three concrete contracts: bytes read back from the server equal the uploaded asset, an upload with a disallowed extension is rejected without creating a file, and a deleted background can no longer be read. A handler test alone does not establish that the startup, storage, reload, and deletion path works against an isolated data root.

| Item | This case uses |
| --- | --- |
| Executor | [scripts/run.py](scripts/run.py), using the actual server binary and HTTP API. No Agent Skill or semantic judge is needed. |
| Data | The committed assets [sample-background.png](assets/sample-background.png) (a valid 8×8 PNG) and [invalid-background.txt](assets/invalid-background.txt) (a `.txt` file used only for the rejected-extension branch). The runner copies the upload source into the run directory as binary evidence. |
| Product entry | `POST /api/appearance/backgrounds` (multipart field `file`), `GET /api/appearance/backgrounds`, `GET /api/appearance/backgrounds/:filename`, and `DELETE /api/appearance/backgrounds/:filename`, before and after a graceful server restart. |
| Oracle | Upload succeeds with a `.png` filename; the returned file is stored under the isolated `DATA_ROOT/product/settings/backgrounds`; readback bytes and SHA-256 equal the asset before and after restart; the `.txt` upload returns business code `1` and adds no file; after `DELETE` the listing is empty, the file is gone from disk, and a read returns HTTP 404. |

## Run from the repository root

Requires Python 3.10 or later, Go 1.26.1 or later, and locally available Go dependencies. The runner builds `server/cmd`, starts two owned processes on an available loopback port, and assigns a unique `DATA_ROOT`, `LOG_DIR`, and related storage roots. It does not call an LLM or embedding provider and does not use a developer's data directory.

~~~bash
python3 tests/cases/D/background-upload-restart/scripts/run.py
~~~

The command creates a unique, Git-ignored `reports/<run>/` under this case, prints its path, and exits `0` for `PASS`, `1` for an observed contract `FAIL`, or `2` for a setup or execution `ERROR`. Every invocation starts with a new data root and build cache. Keep the printed directory when reviewing a failure; remove it when no longer needed.

## Expected behavior and evidence

1. On fresh data, `GET /api/appearance/backgrounds` returns an empty listing.
2. A multipart `POST` of `assets/sample-background.png` succeeds and returns a generated `bg-<uuid>.png` filename. The file exists only under `data/product/settings/backgrounds/`, and its size and SHA-256 match the asset.
3. A raw `GET` of that filename returns the exact uploaded bytes. The runner stores the readback beside the source as binary evidence.
4. Uploading `assets/invalid-background.txt` returns business code `1` (`Invalid file type`) with no data, leaves the listing unchanged, writes no `.txt` file, and adds no file to the backgrounds directory.
5. After graceful shutdown and restart against the same isolated data root, the file is still listed and readback bytes still match the asset.
6. `DELETE` succeeds, the listing becomes empty, the file disappears from disk, and a subsequent raw `GET` returns HTTP 404.

Inspect `result.json` for each assertion and the tested revision, and `report.md` for the summary. Under `data/`, `responses.json` holds actual API requests and responses (multipart uploads are recorded by filename, size, and SHA-256), `binary_evidence.json` and `readback_evidence.json` hold the compared hashes, `upload-source.png`, `readback-before-restart.png`, and `readback-after-restart.png` are the literal bytes, `server-1.log` / `server-2.log` process behavior, and `build.log` build errors. `data/product/` contains this run's application state. The case covers the server process and API; it does not claim browser rendering, Electron packaging, or model behavior.
