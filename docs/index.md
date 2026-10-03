---
layout: home

hero:
  name: SmartClass Webcam Server
  text: Camera streaming and recording backend
  tagline: A self-hosted service that connects classroom cameras over an authenticated WebSocket, manages them from one API, and keeps recordings in object storage you control.
  actions:
    - theme: brand
      text: Get Started
      link: /guide/getting-started
    - theme: alt
      text: Deploy
      link: /guide/deploy

features:
  - title: Camera registry and live status
    details: Register each camera with a name, location, and capture profile, and see whether it is online, idle, or recording.
  - title: Remote camera control
    details: Apply configuration changes and start or stop recordings from the management API, with no visit to the classroom.
  - title: Recording sessions with playable segments
    details: Every recording is a session with its own ordered segment list, downloadable on demand for playback or archiving.
  - title: Storage where you want it
    details: Send recordings to nsc-filehouse, the fleet's object storage service, or to any S3-compatible endpoint.
  - title: Enterprise sign-in and permissions
    details: Operators sign in through teamusers; the service keeps no local accounts and enforces the webcam:cameras:any permission on every management call.
  - title: Metadata you control
    details: Cameras, recording sessions, and segments stay in your own PostgreSQL database, with schema updates applied automatically on startup.
---
