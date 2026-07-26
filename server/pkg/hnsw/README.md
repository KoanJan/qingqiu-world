# hnsw

Forked from [github.com/coder/hnsw](https://github.com/coder/hnsw) v0.6.1.

## Why Forked

The original `coder/hnsw` depends on [google/renameio](https://github.com/google/renameio) in `SavedGraph.Save()` for atomic file writes. `renameio` explicitly does not support Windows, causing cross-compilation failures:

```
# github.com/coder/hnsw
encode.go:304:23: undefined: renameio.TempFile
```

## Changes

- **Removed** `github.com/google/renameio` dependency from `encode.go`
- **Removed** `SavedGraph.Save()` method (the only consumer of `renameio`)
- **Updated** internal import path: `github.com/coder/hnsw/heap` → `qingqiu-world-server/pkg/hnsw/heap`

Graph persistence is now handled by the caller via `Graph.Export` / `Graph.Import` with standard library (`os.Create` + `os.Rename`), which works across all platforms.

## License

The original project is licensed under [CC0 1.0 Universal](https://github.com/coder/hnsw/blob/main/LICENSE) — the author has waived all copyright and dedicated the work to the public domain.
