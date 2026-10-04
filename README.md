# SmartClass Webcam Server

SmartClass Webcam Server is a self-hosted backend for classroom camera fleets. Each classroom device
registers over HTTP with a long-lived device token, holds an authenticated WebSocket that carries
control commands and media, operators manage devices and recordings from a single REST API, and every
recording is kept in the object storage your organisation already runs.

## Capabilities

Device registry with live status, remote camera control, recording sessions with playable
segments, storage in nsc-filehouse or any S3-compatible endpoint, teamusers-based permissions, and
metadata in your own PostgreSQL. Each capability is described on the
[documentation home](https://crazy4chicken.github.io/smartclass-webcam-server/).

## Quick start

A local run needs Go 1.26+, PostgreSQL 16+, a teamusers instance, and a filehouse or
S3-compatible bucket; the service applies its database schema on startup. The
[getting started guide](https://crazy4chicken.github.io/smartclass-webcam-server/guide/getting-started)
covers the database and bucket, the required `WEBCAM_*` configuration, building and running, and
the first recording.

## Documentation

- [Getting started](https://crazy4chicken.github.io/smartclass-webcam-server/guide/getting-started) - a
  full walkthrough from an empty database to a first recording.
- [Device protocol](https://crazy4chicken.github.io/smartclass-webcam-server/protocol/) -
  the authoritative device contract: registration, ticket lifecycle, framing, and every command.
- [Permissions and access control](https://crazy4chicken.github.io/smartclass-webcam-server/guide/permissions) -
  the `cam:<action>:<scope>` permissions, the scope ladder, and teamusers catalog registration.
- [Service integration](https://crazy4chicken.github.io/smartclass-webcam-server/guide/service-integration) -
  how other services retrieve recordings and photos: discovery, presigned downloads, and direct bucket access.
- [Deployment guide](https://crazy4chicken.github.io/smartclass-webcam-server/guide/deploy) - prerequisites,
  configuration reference, svchost compose example, systemd unit, TLS, and operations.
- [API reference](https://crazy4chicken.github.io/smartclass-webcam-server/api/overview) - the management
  endpoints and the device WebSocket protocol.

## License

Licensed under the GNU Affero General Public License v3.0 (AGPL-3.0).
