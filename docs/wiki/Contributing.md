## Ground rules

- **Reuse, never rewrite.** Git keeps skills (through warp), pirn runs them and Temporal retries them. Ask before adding anything that reimplements part of them.
- **Never check or run a plan against the newest skills.** Use the versions it pins.
- **A mistake isn't fixed until it has a row on [Invariants](Invariants)** and a guard that fails on the old code. CI checks the page against the tests in both directions.
- **Update this wiki in the same change.** The pages live in `docs/wiki/` and publish on every push to `main`.
- Supported platforms are Linux (amd64, arm64) and macOS on Apple Silicon.

## Before opening a pull request

```bash
gofmt -l .        # prints nothing
go vet ./...
go test -race ./...
bash .github/invariants.sh
```
