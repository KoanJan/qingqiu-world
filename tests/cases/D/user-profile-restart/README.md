# user-profile-restart: User profile survives a server restart

This deterministic case verifies the server's user-profile API and SQLite persistence across two real server processes. It checks a concrete product rule: a user's name stays fixed when their bio is updated, and the update is still visible after restart. A package or handler test alone does not establish that the full startup, API, shutdown, and reload path works.

| Item | This case uses |
| --- | --- |
| Executor | [scripts/run.py](scripts/run.py), using the actual server binary and HTTP API. No Agent Skill or semantic judge is needed. |
| Data | A fresh, isolated SQLite database created by the server. The runner creates `profile-restart-user` with a short test bio; it uses no fixture file or real provider. |
| Product entry | `GET /api/user-profile` and `PUT /api/user-profile`, before and after a graceful server restart. |
| Oracle | Exact response codes, ID, name, bio, and person type; one persisted human row after both processes stop. |

## Run from the repository root

Requires Python 3.10 or later, Go 1.26.1 or later, and locally available Go dependencies. The runner builds `server/cmd`, starts two owned processes on an available loopback port, and assigns a unique `DATA_ROOT`, `LOG_DIR`, and related storage roots. It does not call an LLM or embedding provider and does not use a developer's data directory.

~~~bash
python3 tests/cases/D/user-profile-restart/scripts/run.py
~~~

The command creates a unique, Git-ignored `reports/<run>/` under this case, prints its path, and exits `0` for `PASS`, `1` for an observed contract `FAIL`, or `2` for a setup or execution `ERROR`. Every invocation starts with a new data root and build cache. Keep the printed directory when reviewing a failure; remove it when no longer needed.

## Expected behavior and evidence

1. On fresh data, `GET /api/user-profile` returns an empty profile.
2. `PUT /api/user-profile` creates one human profile with the supplied name and bio.
3. A second `PUT` requests a different name and an updated bio. The response keeps the original ID and name but contains the new bio.
4. After graceful shutdown and restart against the same isolated data root, `GET` returns that same ID, original name, updated bio, and human type.
5. A read-only SQLite query after the second shutdown finds exactly one human profile row with those values.

Inspect `result.json` for each assertion and the tested revision, and `report.md` for the summary. Under `data/`, `responses.json` holds actual API requests and responses, `db_state.json` the final database row, `server-1.log` / `server-2.log` process behavior, and `build.log` build errors. `data/product/` contains this run's application data. The case covers the server process and API; it does not claim Electron or browser behavior, database upgrade compatibility, or model behavior.
