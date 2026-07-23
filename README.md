# Syncdesic

*Content is identity, path is property.*

[![MPLv2 License](https://img.shields.io/badge/license-MPLv2-blue.svg?style=flat-square)](LICENSE)

Syncthing fork. Content-object layer on existing block-level CAS. Wire-compatible with upstream — same BEP, same DB schema, upstream never knows.

## Why

Syncthing already identify blocks by SHA-256. `BlocksHash` is de facto CID. But ignore system, sync scheduler, file model all treat path as identity, content as derivative. Flip it.

## What

- `ObjectID` = `BlocksHash` — content is identity, path is property
- `pin` refcount — keep blocks even when all paths ignored
- `(?cid)a1b2...` — content-level ignore patterns
- `vfs/` — read by ObjectID+offset, no full file sync needed
- Zero BEP changes. Upstream nodes see a normal Syncthing peer

## Build

```
go run build.go
```

`go.mod` has `replace github.com/syncthing/syncthing => ./ref`. Upstream packages import as normal.

## Status

Early dev. Not production. Research fork by [Mindires Research Institute](https://mindires.org).

## License

MPLv2. Inherited from Syncthing.
