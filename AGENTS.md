# RainMusic development operations

- Use mise for tasks and locked development dependencies. Implement automation in Go; do not add shell or PowerShell scripts.
- Keep current behavior in `docs/specification.md` and its linked details, architecture in `docs/architecture.md`, and task procedures in `docs/development.md`. Update the relevant current document when changing behavior. Keep README focused on setup and entry points; preserve dated verification/migration reports as historical evidence. See `docs/index.md` for the document map.
- For the development Bot, use only `mise run bot-start`, `bot-stop`, `bot-restart`, `bot-status`, and `bot-logs`. `serve` is an alias of `bot-start`.
- Do not launch the Bot with ad hoc `Start-Process`, raw executable invocations, or `go run ... serve`. Do not use process-name-based termination. Do not treat an empty process result from a restricted environment as evidence that the host Bot is stopped.
- Run lifecycle tasks with host process visibility. If inspection fails, an unmanaged process is found, or graceful shutdown times out, report the error and investigate; never bypass the guard by launching another instance or force-killing a process automatically.
- After changing runtime code, use `bot-restart`; verify `bot-status` reports exactly one managed process and `ready=true`. Restart builds before stopping the current instance. Build failure must leave the current Bot running.
- DAVE backend selection uses `bot-restart-go` (Pure Go, default) or `bot-restart-native` (libdave rollback); these call the same guarded restart operation. `build-voice-native` builds the rollback backend without starting it.
- Queue persistence is automatic. Managed restart checkpoints playback/settings and resumes an active session from the track beginning; stopped sessions remain stopped. Use URL-less `/play` after stop/start, first migration, or reconnect failure. Do not erase `.dev/state` during ordinary development or tests.
- Do not print `mise.local.toml`, environment secrets, or `.dev/run/control.json` (it contains the local control token). Logs and runtime files stay under ignored `.dev`.
