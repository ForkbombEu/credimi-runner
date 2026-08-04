# Multi-device runner implementation plan

## Scope and final model

This plan covers **only `credimi-runner`**. `credimi-2` and `credimi-extra`
already define the device-facing contracts this repository must consume; do not
ask their repositories for compatibility changes and do not reintroduce old
runner-target APIs.

One runner process is a host. It owns one public server URL and one Temporal
worker per namespace. It may own many independently addressable execution
devices. The host identity is `runner_id`; every execution target is the
canonical child ID `device_id` (`org/runner/device`). A device semaphore lives
in Credimi, so a busy/offline device must not block its siblings.

The root `.env` is the only runtime configuration file. There is no
`devices/*.env` layout, migration, fallback, scan, import, or cleanup.

```dotenv
# Minimum direct `credimi-runner serve` environment
CREDIMI_URL=https://credimi.example
CREDIMI_INTERNAL_ADMIN_KEY=...
CREDIMI_RUNNER_ID=org/runner
CREDIMI_DEVICE_COUNT=2
CREDIMI_DEVICE_1_ID=org/runner/phone
CREDIMI_DEVICE_2_ID=org/runner/emulator
```

`NAME`, `TYPE`, `MODE`, serial/UDID, Wi-Fi details, AVD data, local paths,
ports, and emulator/redroid settings are dashboard setup metadata or selected
device-local runtime values. They must never be required merely to start
`serve`. `CREDIMI_DEVICE_n_ENABLED` is optional and defaults to true.

The dashboard must require name/type/mode when it creates/registers a device,
obtain its canonical ID from Credimi preview, and never allow manual editing of
the canonical ID. Device name and ID are immutable once registered: create a
new device instead of silently renaming/moving one used by pipelines.

## Audited baseline — 2026-08-03

The branch has three committed foundation commits and a substantial **uncommitted
worktree**. Preserve all useful changes, finish and test them, then commit in
logical groups. Do not discard unrelated work.

| Area | Current state |
| --- | --- |
| Indexed inventory | Implemented in `internal/dashboard/runtime/inventory.go`: root indexed blocks, contiguous index and child-ID validation, deterministic writer, optional `ENABLED`, direct-serve validation requiring only indexed IDs. `MigrateLegacySingleTarget` remains intentionally; the forbidden `devices/*.env` migration code is removed in the worktree and must stay removed. |
| Direct configuration | `.env.example` has been reduced to direct-serve variables. The user-local ignored `.env` has one indexed emulator. Documentation and compose examples still need a coherent final pass. |
| Credimi client | Runner/device preview and upsert request structs exist in `runtime/credimi.go`; runner upsert no longer sends target fields. Dashboard routes for preview/register/enable/disable/remove have been added, but the UI/workflow is not yet a functional multi-device editor. |
| Lifecycle | Runner lifecycle payloads carry a `devices` list, initially from configured enabled devices. It is not yet refreshed from real per-device readiness/probes. |
| Runner API | GoA design, generated code use, handler mapping, artifact multipart forwarding, legacy `runner_identifier` rejection, device-inventory checks, device/run artifact roots, and readiness map are present in the worktree. Review and complete tests/regeneration. |
| Worker | Inventory is passed to one worker per namespace; concurrency is currently device-count based. `DeviceDispatcher`/`DeviceGate` exist, but real Credimi mobile activities are still globally constructed and do not use the dispatcher or a device-bound configuration. |
| Dashboard/runtime | The template still contains the legacy single-target form and fields such as `CREDIMI_RUNNER_TYPE`, `CREDIMI_RUNNER_DEVICE_MODE`, `BASE_NAME`. Normalization, compose planning, controller readiness, provisioning endpoints, JavaScript, and tests still assume one target. This is the main remaining implementation. |

The existing committed foundation is `2ec16ec`, `9058bf5`, and `885f5f2`.
`0fff9b8` added the now-forbidden directory migration; do not preserve its
directory-layout behavior. The current worktree has already removed it, so make
that removal part of the next relevant commit.

## Required Credimi contracts

All identifiers are stored and emitted without a leading slash; accepting one
at an HTTP boundary is fine only when immediately normalized.

### Runner/dashboard → Credimi

| Operation | Endpoint | Required JSON body |
| --- | --- | --- |
| Preview host | `POST /api/mobile-runner/preview-id` | `{organization?, name}` |
| Upsert host | `POST /api/mobile-runner` | `{runner_id?, organization?, name, ip, port?, description?, published?}` — never type, serial, mode, or local settings |
| Preview device | `POST /api/mobile-device/preview-id` | `{organization?, runner_id, name}` |
| Upsert device | `POST /api/mobile-device` | `{organization?, device_id?, runner_id, name, description?, type, serial?}` |
| Resume host | `POST /api/mobile-runner/lifecycle/resume` | `{runner_id}` |
| Host heartbeat | `POST /api/mobile-runner/lifecycle/heartbeat` | `{runner_id, devices:[{device_id, online, reason?}]}` for every configured device |
| Pause host | `POST /api/mobile-runner/lifecycle/pause` | `{runner_id, reason?}` |

Never send `mode`, Wi-Fi values, AVD/redroid/iOS fields, paths, ports, or
secrets to Credimi. A disabled/unready local device is reported `online:false`;
Credimi has no separate device enabled flag.

### Credimi → shared runner server

Every request carries `device_identifier`; no `runner_identifier` alias:

| Endpoint | Required fields |
| --- | --- |
| `POST /credimi/installer-action` | `version_identifier`, `platform`, `device_identifier`; optional action/skip flag |
| `POST /credimi/pipeline-result` | `run_identifier`, `platform`, `device_identifier`, video/last-frame/log paths |
| `POST /credimi/execution-screenshots` | `run_identifier`, `device_identifier`, `step_id`, screenshot paths |

The runner validates local membership before filesystem/network/target access.
Artifacts live beneath `<managed-root>/<safe-device-id>/<safe-run-id>/...` and
cleanup may affect only that root. Multipart forwarding preserves
`device_identifier` to `api/wallet/store-pipeline-result` and
`api/pipeline/store-step-screenshots`.

## Implementation sequence

### 1. Stabilize the configuration model

1. Keep `RunnerRuntimeConfig` and `DeviceRuntimeConfig` as immutable values.
   Runtime parsing must require only runner ID, positive count, contiguous
   blocks, and one child device ID per block. Reject unknown/malformed indexed
   keys, duplicate IDs, bad `ENABLED`, and a non-child ID.
2. Retain optional metadata in a block, validate uniqueness only when a value
   is set, and add a dedicated dashboard registration validation requiring
   name/type/mode. Do not make `serve` use it.
3. Split host-field registry/defaults/normalization from device-field
   normalization. Delete execution-target semantics from unindexed
   `CREDIMI_RUNNER_TYPE`, `CREDIMI_RUNNER_SERIAL`,
   `CREDIMI_RUNNER_DEVICE_MODE`, `BASE_NAME`, etc. Legacy fields may exist
   only inside `MigrateLegacySingleTarget`, never as an active configuration
   path.
4. Make `SaveRuntimeConfig` preserve runner-owned lines/comments and write
   deterministic numbered device blocks. Adding/removing/reordering reindexes
   blocks; all device-specific values stay indexed.
5. Update README, `.env.example`, Docker Compose, Coolify Compose, scripts,
   and tests. Direct Coolify/`serve` requires root host credentials, runner ID,
   count, and IDs; deploy-specific device settings remain indexed. Do not
   require dashboard-only metadata in this example.

### 2. Make the dashboard a real device inventory editor

1. Initial wizard configures host identity/server/network, upserts only the
   runner, then offers Add first device. A runner with no device is valid for
   dashboard setup but cannot start `serve`/workers.
2. Replace the legacy one-target `/devices` form and JS. Render an inventory
   table/cards with canonical ID, display name, type/mode, local validity,
   transport summary, enabled state, registration state, and probe/readiness.
3. Add a device-specific add/edit form. It writes only
   `CREDIMI_DEVICE_n_*`; type controls reveal only that device's provisioning
   fields. Scope provisioning endpoints and JS requests by device block/index
   or device ID. Never read/write global target keys.
4. Add server actions: preview canonical ID from device name, save draft,
   register/update, enable, disable, reconnect/disconnect, and confirmed
   remove. Registration calls `ValidateDeviceRegistration`, then device preview
   and upsert. Existing IDs cannot be manually changed.
5. Removal is local block removal/reindex after explicit confirmation. It must
   disable/stop only that device's target runtime and report it offline. Do not
   pause host lifecycle or stop namespace workers. Runner stop remains global.
6. Update overview, SSE/hub snapshots, workers views, logs, and templates to
   distinguish host health from each device. A host remains healthy when one
   device is offline.

### 3. Finish registration and lifecycle

1. Keep host registration once per runner. Register each device separately;
   collect per-device errors and surface them without corrupting sibling blocks.
2. Ensure controller lifecycle validates every device registration field before
   sending it. Persist server-returned canonical IDs from preview/upsert.
3. Replace the current static lifecycle device list with readiness/probe-derived
   state for every configured device on every heartbeat. Include disabled,
   missing serial/UDID, disconnected, and failed emulator states as offline
   with a useful non-secret reason.
4. Resume once after host readiness; pause once on runner shutdown. A device
   disable updates its heartbeat entry only, never invokes host pause.

### 4. Complete runner server isolation

1. Treat the current GoA/device-artifact work as a partial implementation:
   regenerate `pkg/gen` and public OpenAPI from `pkg/design/runner_server.go`,
   then ensure no generated/client/contract type retains `runner_identifier`.
2. Require `device_identifier` in all three Credimi endpoints, reject missing,
   unknown, disabled, and cross-runner IDs before work. Ensure installer
   downloads and all cleanup use the selected device/run root even when a
   runtime inventory is unavailable only in narrowly scoped legacy unit tests.
3. Remove the readiness legacy fallback (`ANDROID_SERIAL`,
   `CREDIMI_RUNNER_SERIAL`, `DeviceSerial`, `DeviceState`) from production
   multi-device behavior. Return a keyed device readiness map based on indexed
   IDs and each selected serial/transport.
4. Update health/mobile control APIs that still touch a default device: they
   need explicit `device_identifier` or must be removed if obsolete. In
   particular audit `TouchFingerprint` and all ADB helpers for first-device
   behavior.

### 5. Bind real activities to a device

1. Keep one Temporal worker per namespace and task queue
   `<runner_id>-TaskQueue`. The worker inventory snapshot contains all enabled
   devices. Worker count is never device count.
2. Replace `activities.New*()` global registrations in
   `pkg/workermanager/worker_manager.go` with wrappers/factories that extract a
   required `device_id` from every target-touching activity payload, resolve it
   through `DeviceDispatcher`, acquire its `DeviceGate`, and pass an immutable
   device-scoped config to Credimi Extra activities. Never mutate process env.
3. Integrate the `credimi-extra` device-aware activity contract already planned
   there. Preserve shared runner configuration separately. Add `runner.id` and
   `device.id` telemetry; execution telemetry must not call device ID
   `runner_id`.
4. Set activity concurrency high enough for enabled devices, retain a per-device
   local gate, and ensure disabling a device refreshes worker inventory without
   stopping sibling namespace workers. Coordinate safely with running work.

### 6. Make host/device runtimes coexist

1. Rework compose/runtime/controller plans so one host service can manage many
   target runtimes. Model runner-wide server/network services separately from
   device-specific emulator/redroid/simulator child resources.
2. Device resource names, AVD names, ports, containers, volumes, work dirs,
   and emulator paths must derive from safe device identity and be unique.
3. Multiple USB devices require explicit serials; multiple emulators require
   unique resources; USB and emulators may coexist. Validate collisions before
   save/start. No command may silently select the first ADB device.

## Tests and handoff gates

Add/update tests beside changed code for:

- direct serve with ID-only blocks; dashboard registration rejection without
  metadata; no `devices/*.env` migration/support;
- preview/upsert/lifecycle bodies and per-device partial registration failure;
- per-device dashboard add/edit/remove/enable/disable and unchanged siblings;
- one namespace/two devices → one worker; two namespaces → two workers;
  task queue remains runner-scoped; same-device work serializes and different
  devices can run concurrently;
- each runner endpoint rejects legacy/missing/unknown device IDs, forwards
  multipart `device_identifier`, and isolates equal run/file names;
- readiness/heartbeat maps include all configured devices independently;
- compose/controller collision checks and device-specific provisioning.

Before each logical commit: run the repository formatter and `task lint` as
required by `PURIA.md`, inspect staged files for secrets, and never stage `.env`.
Before final handoff: regenerate GoA/OpenAPI, run the focused test packages and
the required full suite, `git diff --check`, and verify generated files are
current. Manual final validation uses one runner with two USB devices and two
emulators: all four register, are independently scheduled, and a stopped or
busy device does not affect siblings.
