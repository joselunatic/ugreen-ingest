# UGREEN Media Ingest

Automated SD/CFexpress card ingest appliance for audiovisual production,
built to run on a UGREEN NASync DXP4800 Pro (UGOS Pro) with USB card readers
plugged directly into the NAS. A tablet in the edit room is the only thing
an operator touches: insert a card, tap a camera, wait for an unambiguous
"safe to eject".

This is **phase 1**: everything runs in `INGEST_MODE=mock`, using local
directories to simulate inserted cards. No USB/NAS hardware is required or
touched. The full requirements live in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md) - this README is the "how do I run it"
companion, the runbook is the spec.

## Visual identity: tv2beat branding

The tablet UI is styled after [tv2beat.com](https://tv2beat.com/es/), Moy
Santana's production company, so it reads as one of their tools rather than
a generic dashboard:

- Background/panels and accent color (`#1a191d` / `#00eebb`) are pulled
  directly from their site's stylesheet, and the header uses their actual
  logo SVG (fetched from their own site, fill `#fff` for the dark UI).
- Buttons and cards use `border-radius: 0`, matching their flat/editorial
  button style exactly.
- Headings use a heavy system-font weight instead of their real display
  face (**Poly Sans Bulky**), which is a licensed commercial font we have
  no rights to embed. This is a visual approximation, not the exact
  typeface. If you have a license and the font files, drop the woff2s in
  `web/static/fonts/` and wire up an `@font-face` in `style.css` - happy to
  do that pass once the files are available.
- No external font/asset CDN is used anywhere: the tablet must keep working
  with no internet access at a venue, so everything brand-related is
  either inlined SVG or a system font.

## Architecture decision: clean implementation, not a fork

The runbook asked us to evaluate forking `michaelpeterswa/cardingest`
instead of starting clean. We chose a clean Go implementation. Reasoning:

- `cardingest`'s value is in its Linux hot-plug/device-detection code, which
  this phase explicitly does not touch (`INGEST_MODE=mock` only, see
  `internal/device/linux.go` for the deliberately-unimplemented stub).
  Adapting an appliance built around real block devices to also run
  cleanly against plain directories would mean fighting its assumptions
  more than reusing them.
- The runbook's principle is "simplicity first" for two people. A fork
  drags in code, structure, and dependencies we'd have to understand and
  maintain regardless of whether we use them.
- Nothing here is copy-pasted from either reference project - no
  attribution/license notice is needed. The *ideas* (single Go monolith,
  SQLite, REST+SSE, a swappable device-detection interface, and
  copy/verify as a distinct phase from copy) come from `cardingest` and
  `innate-inc/sd-card-media-ingest` per runbook sections 7-8, and are
  credited here rather than in code comments.

The design keeps the door open for adopting pieces of `cardingest` later
(e.g. its `/dev/disk/by-path` handling) behind the `device.Provider`
interface, without touching the ingest state machine, API, or UI.

## Requirements

- Windows 11 + WSL2 (Ubuntu), **or** any Linux/macOS machine
- Go 1.26+ (only needed for local `go run`/`go test`, not for Docker)
- Docker + Docker Compose v2 (`docker compose`, not `docker-compose`)

This repo is developed and tested from the WSL2 Linux filesystem
(`~/repos/ugreen-ingest`), not `/mnt/c/...` - see the runbook section 12 for
why (Docker/filesystem performance and semantics).

## Quickstart (Docker, mock mode)

```bash
docker compose -f compose.yml -f compose.dev.yml up --build
```

Then open **http://localhost:8088** (see "About the port" below).

Simulate inserting a card:

```bash
./scripts/create-mock-card.sh CARD_TEST_001
```

Within ~2 seconds the tablet UI shows "TARJETA DETECTADA" and asks for a
camera. Tap one; the card copies to `./dev/data/ingest/...`, gets verified
against the untouched source in `./dev/cards/...`, and the UI shows
**SEGURO RETIRAR** (safe to eject) - never before verification succeeds.

Stop with `Ctrl+C`, or `docker compose -f compose.yml -f compose.dev.yml down`.
History survives restarts: it's in `./dev/data/state/ingest.db`.

### About the port

The compose files publish on **8088**, not 8080, because 8080 was already
in use by other services on the development machine. The container always
listens on 8080 internally. Set `INGEST_HOST_PORT=8080` in `.env` (copy
from `.env.example`) if 8080 is free on your machine.

## Quickstart (without Docker)

```bash
cp .env.example .env   # optional
go run ./cmd/ingest
```

With no config file, it uses the built-in defaults from
`internal/config/config.go`: mock mode, `./dev/cards` as the simulated
reader, `./dev/ingest` as the destination, `./data/state/ingest.db` for
history, listening on `0.0.0.0:8080`. Copy `config/config.example.yaml` to
`config/config.yaml` to override any of that (env vars, listed in
`.env.example`, take precedence over the YAML file).

## Running tests

```bash
go test ./...
```

Covers, per the runbook's acceptance criteria (section 18.1):

- copy preserves the source tree and never modifies it (`internal/copyengine`)
- verification success, missing-file, size-mismatch, and hash-mismatch
  (same-size tampered content) cases (`internal/verify`)
- the full detect → assign camera → copy → verify → SAFE_TO_EJECT → eject
  pipeline, using the real detector/copy/verify/db code
  (`internal/ingest`)
- **the required negative test**: a deliberately corrupted destination file
  always ends the job in `ERROR`, never `SAFE_TO_EJECT`
  (`TestPipeline_CorruptedDestination_EndsInErrorNeverSafeToEject`, plus
  `TestVerify_CorruptedDestination` at the verify-engine level)
- unknown camera, double camera-assignment, destination collision, and
  ejecting before `SAFE_TO_EJECT` are all rejected
- the HTTP API's status codes for the above (`internal/api`)
- events: only one active at a time, switching active events never
  rewrites already-detected ingests, camera validation uses the event a
  card actually belongs to (not whichever is active *now*), duplicate
  event names and invalid names/camera lists are rejected
  (`internal/db/events_test.go`, `internal/ingest/manager_test.go`)

## State machine

```
DETECTED -> WAITING_CAMERA -> COPYING -> VERIFYING -> VERIFIED -> SAFE_TO_EJECT -> EJECTED
                                  \            \
                                   -> ERROR <---
```

`SAFE_TO_EJECT` is only ever set immediately after a `VERIFIED` transition
(see `Manager.runCopyAndVerify` in `internal/ingest/manager.go`) - there is
no code path that can reach it any other way. Formatting
(`POST /api/v1/ingests/:id/format`) is wired up but always returns
`501 Not Implemented`; per the runbook, it's out of scope until the safe
copy/verify/eject path is stable on real hardware.

## Configuration

See `config/config.example.yaml` for the full set of keys
(mode/listen/database/destination_root/mock_root/project/cameras/
verification/formatting) and `.env.example` for the equivalent environment
variables. `project.default_name` and `cameras` are now only the **seed for
the first event** on a brand-new database (see "Events" below) - after that,
manage the active event's name and camera list from `/admin.html` instead of
editing config and restarting.

## Events (proyecto/evento) and the admin panel

One event is active at a time; every newly detected card attaches to it.
Switching the active event never touches ingests already recorded under a
previous one (they keep the event name and camera list they had at
detection time, forever - see `internal/db/events.go`).

Manage events at **http://localhost:8088/admin.html** - deliberately a
separate page, not linked from the tablet UI, so the operator screen stays
exactly as minimal as the runbook asks for (section 4/22: no admin menus on
the tablet). The admin page has no login, same as the tablet: this is a
trusted-LAN tool for two people, not a multi-tenant system.

The admin panel covers the three things asked for when starting phase 2:

- **Create/select the event** - a name (becomes the top-level folder under
  `destination_root`) and its camera list (becomes the buttons the tablet
  offers). Both are validated (path-safe characters only, since both become
  literal directory names) and take effect immediately - the tablet UI
  picks up the new camera list within 5 seconds via `/api/v1/config`,
  no restart needed.
- **Operation log with NAS destinations** - every ingest ever recorded,
  filterable by event, showing the full `destination_path`, not just the
  card number the tablet shows.
- **Storage** - free/total space for `destination_root`, also surfaced as a
  small "Storage: OK/BAJO" indicator on the tablet itself (closing the gap
  with the runbook's section 11.1 mockup, which we hadn't implemented until
  now). **This checks `destination_root` as a local path today**; once the
  NAS is available this may need to change (e.g. if the real mount point
  isn't the same filesystem as where the app itself runs) - flagged as a
  to-revisit-with-hardware item, not solved yet.

## API

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | liveness check |
| GET | `/api/v1/status` | active event name + storage free/total/percent |
| GET | `/api/v1/config` | active event's cameras + name, for the tablet |
| GET | `/api/v1/ingests` | full history, newest first |
| GET | `/api/v1/ingests/{id}` | one job |
| POST | `/api/v1/ingests/{id}/assign-camera` | `{"camera":"CAM01"}`; starts copy+verify |
| POST | `/api/v1/ingests/{id}/eject` | only from `SAFE_TO_EJECT` |
| POST | `/api/v1/ingests/{id}/format` | always `501`, not implemented yet |
| GET | `/api/v1/projects` | list events with per-event stats |
| POST | `/api/v1/projects` | `{"name":"...","cameras":["CAM01",...]}`; create an event |
| POST | `/api/v1/projects/{id}/activate` | switch the active event |
| GET | `/api/v1/events` | SSE: `ingest_updated` and `progress` events (yes, `events` is the SSE stream and `projects` is the event/festival resource - the runbook fixed the SSE path name in section 16 before "event" as in "festival" existed as a concept here) |
| GET | `/` | the tablet UI |
| GET | `/admin.html` | the admin panel |

## Repository layout

```
cmd/ingest/          entrypoint: wires config, db, device provider, manager, API
internal/config/     YAML + env config loading
internal/model/      shared types (Ingest, Status, Progress)
internal/db/         SQLite persistence (modernc.org/sqlite, pure Go, no CGO); ingests + events tables
internal/device/     Provider interface; mock (directories) + real (stub) implementations
internal/copyengine/ recursive tree copy with progress, source is read-only
internal/verify/     re-read + hash comparison, pluggable Hasher
internal/ingest/     the state machine (Manager), event/project management, pub/sub event bus
internal/diskspace/  free/total space for a path (syscall.Statfs, Linux-only)
internal/api/        HTTP handlers, SSE, serves the embedded web UI + admin panel
web/static/          tablet UI (vanilla HTML/CSS/JS, no framework) + admin.html/css/js
dev/cards/           mock card fixtures (gitignored except .gitkeep)
scripts/             create-mock-card.sh
docs/RUNBOOK.md       the full spec this was built against
```

## What's pending (needs real hardware)

Everything below is explicitly out of scope for this phase and gated
behind interfaces so it can be added without rewriting the state machine,
per runbook section 22:

- **Real USB/CFexpress detection** (`INGEST_MODE=real`): `internal/device/linux.go`
  is a stub that returns "not implemented". Needs validation under WSL2 +
  `usbipd-win` first (runbook section 13), then physically on the UGOS Pro
  NAS (`/dev/disk/by-path`, hot-plug, mount/umount - runbook section 9 and
  21).
- **Formatting**: intentionally disabled/`501` until the copy/verify/eject
  path has been proven on real cards.
- **ASC-MHL and xxhash**: `verify.Hasher` is pluggable (only `sha256` is
  implemented); MHL export was explicitly deferred by the runbook.
- **UGOS Pro Docker specifics** (Strategy A vs B for device passthrough,
  `SYS_ADMIN`/AppArmor needs) can only be settled by SSH-ing into the NAS
  once available - see runbook section 21's checklist.

## Known limitations of this phase

- The tablet UI omits the "FORMATEAR" button shown in the runbook mockups:
  since formatting isn't implemented, a visible-but-dead button would be
  more confusing than not showing it. `POST .../format` still exists and
  returns `501` for when that changes.
- Progress events are throttled to ~5/second; for the tiny mock fixtures
  created by `scripts/create-mock-card.sh` a copy often finishes inside one
  throttle window, so the progress bar can jump straight to 100% - this is
  a fixture-size artifact, not a bug (progress reporting is unit-tested in
  `internal/copyengine` and used as-is once real card-sized data is copied).
