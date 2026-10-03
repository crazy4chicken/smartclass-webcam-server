# SmartClass Webcam Server

SmartClass Webcam Server is a self-hosted backend for classroom camera fleets. Cameras connect over an
authenticated WebSocket, operators manage cameras and recordings from a single REST API, and every
recording is kept in the object storage your organisation already runs.

## Capabilities

- **Camera registry and live status.** Register each camera with a name, location, and capture profile,
  and see whether it is online, idle, or recording.
- **Remote camera control.** Apply configuration changes and start or stop recordings from the API, with
  no visit to the classroom.
- **Recording sessions with playable segments.** Every recording is a session with its own ordered
  segment list, downloadable on demand for playback or archiving.
- **Storage where you want it.** Recordings go to [nsc-filehouse](https://github.com/crazy4chicken/nsc-filehouse),
  the fleet's object storage service, or to any S3-compatible endpoint.
- **Enterprise sign-in and permissions.** Operator accounts live in
  [teamusers](https://github.com/crazy4chicken/nsc-teamusers); the service keeps no local accounts and
  checks every management call against the `webcam:cameras:any` permission.
- **Metadata you control.** Camera, session, and segment metadata stays in your own PostgreSQL database.

## Quick start

Prerequisites:

- Go 1.26 or newer to build the service.
- PostgreSQL 16 or newer.
- A teamusers instance that signs operators in.
- nsc-filehouse or an S3-compatible object store for recordings.

Set the required configuration and storage target:

```sh
export WEBCAM_DB_URL='postgres://<user>:<password>@127.0.0.1:5432/webcam?sslmode=disable'
export WEBCAM_TEAMUSERS_URL='http://127.0.0.1:8081'
export WEBCAM_TEAMUSERS_CLIENT_ID='smartclass-webcam-server'
export WEBCAM_TEAMUSERS_CLIENT_SECRET='<teamusers client secret>'
export WEBCAM_FILEHOUSE_URL='http://127.0.0.1:8082'
export WEBCAM_FILEHOUSE_BUCKET='webcam-segments'
```

Using S3 instead of nsc-filehouse? Set `WEBCAM_S3_ENDPOINT`, `WEBCAM_S3_ACCESS_KEY`, and
`WEBCAM_S3_SECRET_KEY` (optionally `WEBCAM_S3_BUCKET` and `WEBCAM_S3_USE_SSL`) and leave the filehouse
variables unset.

Build, run, and check the service:

```sh
go build -o smartclass-webcam-server ./cmd/server
./smartclass-webcam-server
curl -fsS http://127.0.0.1:8080/healthz
```

The service applies its database schema automatically on startup. See the deployment guide for creating
the database, the storage bucket, and the teamusers permission and credential.

## Documentation

- [Deployment guide](docs/deploy.md) - prerequisites, configuration reference, svchost compose example,
  systemd unit, TLS, and operations.
  Published at <https://crazy4chicken.github.io/smartclass-webcam-server/deploy>.
- [API reference](https://crazy4chicken.github.io/smartclass-webcam-server/api/overview) - the management
  endpoints and the camera WebSocket protocol.
- [Getting started](https://crazy4chicken.github.io/smartclass-webcam-server/guide/getting-started) - a
  full walkthrough from an empty database to a first recording.

## License

Licensed under the GNU Affero General Public License v3.0 (AGPL-3.0).
