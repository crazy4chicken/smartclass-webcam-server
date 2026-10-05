---
title: Deployment
outline: 2
---

# Deployment

This guide takes `smartclass-webcam-server` from a source checkout to a supervised production service.
It covers the Nekostick/svchost path used by the fleet and a plain systemd alternative for operators who
run their own host management.

## Prerequisites

- **PostgreSQL 16 or newer.** The service owns one database and applies its embedded schema migrations on
  every startup, so no external migration tool is required. Its tables live in the fixed
  `smartclass_webcam_server` schema, which the service creates itself, so the database role needs no
  privileges on `public`.
- **A teamusers IAM instance.** Operators sign in there, and the service validates every management call
  against it. The service keeps no local accounts and never stores user passwords. The `cam:read`,
  `cam:manage`, and `cam:control` keys must be registered in its catalog; see
  [Permissions and access control](/guide/permissions).
- **Object storage.** Either [nsc-filehouse](https://github.com/crazy4chicken/nsc-filehouse) or any
  S3-compatible endpoint (MinIO, Ceph, or a cloud S3 service). The filehouse bucket must already exist;
  the S3 bucket is created on startup when missing. When no storage is configured the service still
  tracks sessions in PostgreSQL but discards the recorded payloads.
- **Go 1.26 or newer** (build host only).

## Build

Build the static Linux/amd64 artifact:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags '-s -w' -o smartclass-webcam-server ./cmd/server
```

The binary has no runtime dependencies; configuration and credentials are supplied through the
environment. If you install through the release workflow, the published asset is named
`smartclass-webcam-server_<version>_<arch>.zip` and contains the binary at the ZIP root, which is the
layout svchost expects.

## Configuration

The service reads all configuration from environment variables.

| Variable | Default | Required | Purpose |
| --- | --- | --- | --- |
| `WEBCAM_LISTEN_ADDR` | `:8080` | No | HTTP listen address. |
| `WEBCAM_DB_URL` | - | Yes | PostgreSQL connection string. |
| `WEBCAM_TEAMUSERS_URL` | - | Yes | Base URL of the teamusers service. Not required when `WEBCAM_DEV=true`. |
| `WEBCAM_TEAMUSERS_AUD` | `teamusers` | No | Expected JWT audience. It must match the issuer's `TEAMUSERS_TOKEN_AUDIENCE`. |
| `WEBCAM_TEAMUSERS_CLIENT_ID` | - | Conditional | teamusers service credential ID. Required for filehouse storage; with the secret below it is also the recommended way to authorize permission checks. |
| `WEBCAM_TEAMUSERS_CLIENT_SECRET` | - | Conditional | teamusers service credential secret. Required together with the ID (filehouse storage needs both). Treat as a secret. |
| `WEBCAM_TEAMUSERS_SVC_TOKEN` | - | No | Static teamusers service token, used for permission checks only when no client credentials are set. It expires after ten minutes, so prefer client credentials. |
| `WEBCAM_WS_TICKET_TTL` | `60s` | No | Lifetime of a device registration ticket issued by `GET /ws/register`. Go duration syntax. |
| `WEBCAM_FILEHOUSE_URL` | - | Conditional | nsc-filehouse base URL. Setting it selects filehouse storage. |
| `WEBCAM_FILEHOUSE_BUCKET` | `webcam-segments` | No | Filehouse bucket for segments. It must exist before startup. |
| `WEBCAM_S3_ENDPOINT` | `localhost:9000` | No | S3-compatible endpoint as `host:port`. |
| `WEBCAM_S3_ACCESS_KEY` | - | No | S3 access key. Setting it selects S3 storage when no filehouse URL is set. |
| `WEBCAM_S3_SECRET_KEY` | - | No | S3 secret key. Treat as a secret. |
| `WEBCAM_S3_BUCKET` | `webcam-streams` | No | S3 bucket for segments. Created on startup when missing. |
| `WEBCAM_S3_USE_SSL` | `false` | No | Use TLS for the object storage connection. |
| `WEBCAM_DEV` | `false` | No | Disables authentication entirely with a synthetic subject; every request is treated as `any`-scoped. Local development only; never enable in production. |

Storage backend precedence: nsc-filehouse when `WEBCAM_FILEHOUSE_URL` is set, otherwise S3 when
`WEBCAM_S3_ACCESS_KEY` is set, otherwise a discarding no-op backend.

Security notes:

- Store `WEBCAM_DB_URL` and `WEBCAM_TEAMUSERS_CLIENT_SECRET` as secrets. Never commit them to source
  control, paste them into examples, or print them in logs. On Nekostick, service environment values are
  stored as plaintext in the host configuration; restrict who can read service entities and their backups.
- The service never stores user passwords: sign-in and account lifecycle belong to teamusers.
- Filehouse access reuses the teamusers service credential, so that credential must also be allowed to
  read and write the filehouse bucket.

## First-time setup

1. **Create the database.** For example:

   ```sh
   createdb webcam
   export WEBCAM_DB_URL='postgres://webcam:<password>@127.0.0.1:5432/webcam?sslmode=require'
   ```

   The schema is applied automatically on the next startup. Tables live in the `smartclass_webcam_server`
   schema, not `public`: the service creates that schema itself, so the role must own the database or
   hold `CREATE` on it. A DBA can pre-create it instead with
   `CREATE SCHEMA smartclass_webcam_server AUTHORIZATION <role>;`, in which case the role only needs
   usage and create rights inside it. Inspecting the tables by hand needs
   `SET search_path TO smartclass_webcam_server;` first. A database that an older version left in
   `public` has its tables (including the migration ledger) moved into the schema on the first startup
   of the new version, keeping the data and not re-running applied migrations.

2. **Prepare object storage.**

   - nsc-filehouse: create the bucket (for example `webcam-segments`) through the filehouse API and grant
     the service account access to it. The service does not create filehouse buckets.
   - S3-compatible: supply credentials; the service creates the bucket on startup if it is missing.

3. **Register the permissions in teamusers.** Add the nine keys (or the `cam:<action>:*` wildcards)
   to the teamusers permission catalog and bind them to the roles that operators hold. Without a
   binding every `/api` request is rejected. The [Permissions guide](/guide/permissions) has the
   registration snippet, the key grammar, and role examples.

4. **Create the service credential.** In teamusers, create a service credential for this service and set
   its `client_id` and `client_secret` as `WEBCAM_TEAMUSERS_CLIENT_ID` and
   `WEBCAM_TEAMUSERS_CLIENT_SECRET` (both are required for filehouse storage). The
   [Permissions guide](/guide/permissions#service-credential) describes what the credential authorizes.

5. **Start the service** and confirm `GET /healthz` answers.

## Nekostick (svchost) deployment

With the [svchost](https://github.com/crazy4chicken/nekostick-svchost) extension, the service is declared
in a `svchost-compose.yaml` document. The example below is a complete configuration for this service.

```yaml
strictSources: true
serviceScope: global
services:
  smartclass-webcam-server:
    source:
      # Pushing a v* tag publishes smartclass-webcam-server_<version>_<arch>.zip
      # release assets through the release workflow.
      release: "github:crazy4chicken/smartclass-webcam-server@v0.2.0"
      # SHA-256 of smartclass-webcam-server_0.2.0_x64.zip.
      sha256: "736d17eac5f296ff4acf2eab7fc81717f9fe49d738ad1aaf11ef6b983c8a35f9"
    env:
      # The host injects HOST and PORT for every launch. This service does not
      # read PORT itself: WEBCAM_LISTEN_ADDR is the only listen setting, and it
      # defaults to :8080. Bind it to the injected lease so the supervisor's
      # health check and routing reach the child.
      WEBCAM_LISTEN_ADDR: "${HOST}:${PORT}"
      WEBCAM_DB_URL: "postgres://webcam:<db-password>@10.0.0.2:5432/webcam?sslmode=require"
      WEBCAM_TEAMUSERS_URL: "http://10.0.0.3:8081"
      WEBCAM_TEAMUSERS_CLIENT_ID: "smartclass-webcam-server"
      WEBCAM_TEAMUSERS_CLIENT_SECRET: "<teamusers-client-secret>"
      WEBCAM_FILEHOUSE_URL: "http://10.0.0.4:8082"
      WEBCAM_FILEHOUSE_BUCKET: "webcam-segments"
    start: eager
    restart: on-failure
    health:
      type: http
      path: /healthz
      timeout: 5s
    route:
      prefix: /webcam
      strip: true # /webcam/api/devices reaches the child as /api/devices
```

Notes:

- Do not set `PORT` or `HOST` in `env` or `args`: the host injects them per launch and rejects a
  conflicting override. Because `WEBCAM_LISTEN_ADDR` defaults to `:8080` and the service does not read
  `PORT`, a deployment that leaves `WEBCAM_LISTEN_ADDR` unset will not bind the leased port. Set it to
  `"${HOST}:${PORT}"` as above, or verify by other means that the deployment binds the leased address.
- `strictSources: true` requires the explicit `sha256`. The pinned digest above is the x64 asset;
  `smartclass-webcam-server_0.1.0_arm64.zip` is
  `3df8f27d02b42eb5f9221e00d88371b4831bf90fd0118dd912b7f82c8bdc47eb`. One digest pins one architecture's
  ZIP, so on mixed-architecture fleets either drop `strictSources` (the lock still pins the first
  observed digest per node) or split per-architecture documents under `serviceScope: document`. Every
  release lists the digests of its assets on the release page, so a version bump is a matter of copying
  the new digest for the architecture you deploy.
- Do not add `--dev` equivalents here: leaving `WEBCAM_DEV` unset keeps authentication enabled, which is
  required in production.
- The device plane is exposed under the same prefix: a device registers over
  `https://<host>/webcam/ws/register` with its device token, then upgrades
  `wss://<host>/webcam/ws/device/<device_websocket_id>`.
- The service CWD is the svchost service root, not the artifact directory. This service keeps no state on
  disk, so no path configuration is needed.

## systemd alternative

For hosts without Nekostick, install the binary at `/opt/smartclass-webcam-server/smartclass-webcam-server`
and run it under systemd:

```ini
[Unit]
Description=SmartClass Webcam Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=webcam
Group=webcam
EnvironmentFile=/etc/smartclass-webcam-server.env
ExecStart=/opt/smartclass-webcam-server/smartclass-webcam-server
Restart=on-failure
RestartSec=2s
# The service drains in-flight requests for up to 30 seconds after SIGTERM.
KillSignal=SIGTERM
TimeoutStopSec=40
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Put the environment variables from the configuration table in
`/etc/smartclass-webcam-server.env` (`WEBCAM_DB_URL=...`, one per line, mode `0600`) and verify the
service with `curl -fsS http://127.0.0.1:8080/healthz`.

## Reverse proxy and TLS

The service speaks plain HTTP and does not terminate TLS. Terminate TLS at the edge proxy and forward
plain HTTP over the trusted internal network. The proxy must:

- forward the `Authorization` header unchanged, because every `/api` request is authorized with a
  teamusers bearer token and every device authenticates with its device token;
- forward GET requests with a JSON body unchanged: device registration is a `GET /ws/register` whose
  body carries the camera list, and a proxy must not drop bodies on GET;
- forward WebSocket upgrades (`Upgrade` and `Connection` headers) for the device plane at
  `/ws/device/{device_websocket_id}`, and allow long-lived connections there;
- route `/webcam/*` to the service when using an external prefix, or expose the service at the root.

## Operations

- **Health.** `GET /healthz` returns `200 {"status":"ok"}` without touching the database; use it as the
  liveness and startup probe. `GET /readyz` returns `200 {"status":"ready"}` while the HTTP server
  serves requests; it does not probe the database, so gate on your database monitoring separately.
- **Logs.** The service writes structured JSON records to stdout and logs nothing to disk. Nekostick
  captures child stdout/stderr line by line; under systemd use `journalctl -u smartclass-webcam-server`.
  Never log or forward the connection string, client secret, or access tokens.
- **Shutdown.** On `SIGTERM` the service stops accepting connections and drains in-flight handlers for up
  to 30 seconds before exiting. Nekostick provides a 15-second grace period, so give it a longer one
  (the systemd unit above uses 40 seconds) if the proxy needs to drain first.
- **Credential rotation.** To rotate the teamusers client secret, create a new credential in teamusers,
  update `WEBCAM_TEAMUSERS_CLIENT_SECRET` in the deployment, and restart the service; remove the old
  credential after the restart. The [Permissions guide](/guide/permissions#service-credential) describes
  what the credential authorizes and how it relates to `WEBCAM_TEAMUSERS_SVC_TOKEN`.
- **Device credentials.** A device token is issued once when the device is created
  (`POST /api/devices`) and rotated with `POST /api/devices/{device_id}/token`. Rotation invalidates
  the old token, drops the device's pending registration ticket, and closes its live connection, so
  the device agent must be reconfigured and register again. Deleting a device
  (`DELETE /api/devices/{device_id}/`) removes its sessions, segments, and photo records with it and
  stops the token from working. A token is never recoverable; store the device id and token where the
  agent can read them. See the [Device registration](/protocol/registration) reference for the
  registration steps.
