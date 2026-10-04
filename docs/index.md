---
layout: home

hero:
  name: SmartClass Webcam Server
  text: Camera streaming and recording backend
  tagline: A self-hosted service that registers classroom devices over a token-authenticated handshake, drives their cameras over one WebSocket session, and keeps the recordings in object storage you control.
  actions:
    - theme: brand
      text: Get Started
      link: /guide/getting-started
    - theme: alt
      text: Deploy
      link: /guide/deploy

features:
  - title: Device registry and live status
    details: Register each classroom device once, hand it a device token, and see whether it is online and which cameras its current registration reports.
  - title: Remote camera control
    details: Switch the active camera, start and stop recordings, and capture still photos from the management API, with no visit to the classroom.
  - title: Recording sessions with playable segments
    details: Every recording is a session with its own ordered segment list, downloadable on demand for playback or archiving.
  - title: Storage where you want it
    details: Send recordings to nsc-filehouse, the fleet's object storage service, or to any S3-compatible endpoint.
  - title: Enterprise sign-in and permissions
    details: Operators sign in through teamusers; the service keeps no local accounts and splits access into cam:read, cam:manage, and cam:control, each scoped to own, team, or any.
  - title: Metadata you control
    details: Devices, recording sessions, segments, and photos stay in your own PostgreSQL database, with schema updates applied automatically on startup.
---
