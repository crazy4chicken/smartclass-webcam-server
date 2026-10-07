---
title: Permissions
outline: 2
---

# Permissions

Operator calls are authorized by [teamusers](https://github.com/crazy4chicken/nsc-teamusers).
The webcam server keeps no local accounts: it verifies the bearer token, resolves the caller's
grants, and compares them against the device or collection being accessed.

Control is split into three **independent** permissions. None implies another:

| Permission | Grants |
| --- | --- |
| `cam:read:<scope>` | Read devices, their recording sessions, segments, and photos. |
| `cam:manage:<scope>` | Create, update, and delete devices; rotate device tokens. |
| `cam:control:<scope>` | Send commands to a device: switch camera, start and stop recordings, take a photo. |

An operator who watches and records a classroom needs `cam:read` **and** `cam:control`; an
administrator who only provisions hardware needs `cam:manage` alone. Because the permissions are
parallel, a read-only auditor never gains command access, and a device administrator never gains
access to the recordings.

## Key grammar

Every key is a three-segment teamusers permission:

```text
[!]resource:action:scope
```

- `resource` is always `cam`.
- `action` is `read`, `manage`, or `control`.
- `scope` is `own`, `team`, or `any` (and the `*` wildcard, see below).
- A leading `!` marks a deny key; see [Deny and invalid keys](#deny-and-invalid-keys).

teamusers matches each segment **literally** - there is no scope hierarchy in the matching itself.
The only wildcard is `*`, which matches any value in that position, so `cam:read:*` matches
requests for `cam:read:any`, `cam:read:team`, and `cam:read:own`. The any/team/own ladder below is
resolved by this service, mirroring teamusers' own administrative resolver.

## Scope resolution

Every request carries an action (`read`, `manage`, or `control`). The server builds the candidate
key list for that action, in order, and the first key the caller is allowed to use wins.

### Device requests

The server has already loaded the target device and knows its `team_id` and `owner_id`:

```text
keys = ["cam:<action>:any"]
if device.team_id  != "" and device.team_id  == caller.team:    keys += ["cam:<action>:team"]
if device.owner_id != "" and device.owner_id == caller.subject: keys += ["cam:<action>:own"]
```

- `any` needs no relationship to the device.
- `team` is only considered when the device belongs to the caller's team.
- `own` is only considered when the caller owns the device.
- If no candidate key is allowed, the request fails with `403`.

The device's `team_id` and `owner_id` are therefore what give the scoped keys their meaning; an
unowned, team-less device can only be reached through `cam:<action>:any`.

### Collection requests

For `GET /api/devices` and `POST /api/devices` there is no single device to compare against, so
the ladder degrades to the caller's own identity, and the matching scope becomes a filter:

| Key found | Resulting view |
| --- | --- |
| `cam:<action>:any` | All devices. |
| `cam:<action>:team` (only when the caller has a team) | Devices filtered by `team_id = caller.team`. |
| `cam:<action>:own` (only when the caller has a subject) | Devices filtered by `owner_id = caller.subject`. |

For `POST /api/devices` the resolved scope also constrains what the caller may set: see
[Device ownership](#device-ownership).

## Device ownership

A device records who is responsible for it:

- `owner_id` - the **subject of the user** who created the device.
- `team_id` - that user's **team** at creation time.

These two fields drive the `own` and `team` scopes, so the server constrains how they can be set:

- `cam:manage:any` callers may set or override either field when creating or updating a device.
- `cam:manage:team` callers may only create devices for their own team.
- `cam:manage:own` callers create devices owned by themselves (`owner_id` is their subject).

An `any`-scoped administrator can therefore hand a device to another owner or team, which is the
supported way to transfer responsibility.

### ABAC conditions

A grant may carry a condition, and teamusers evaluates it against the same device the scope
resolution uses. The expression environment exposes `subject.id`, `subject.kind`, the resource's
`owner_id` and `team_id`, and `request.time`.

For example, an administrator can hold `cam:manage:any` but only act on devices they own by
attaching the condition:

```text
resource.owner_id == subject.id
```

Conditions live on role bindings, not on catalog keys, so the same permission key can be
unrestricted for one role and conditional for another.

## Endpoint matrix

`<scope>` in the permission column is resolved with the ladder above; the target column states
whether the candidate keys are compared against one device or against the collection.

| Route | Permission | Target |
| --- | --- | --- |
| `GET /api/devices` | `cam:read:<scope>` | Collection |
| `GET /api/devices/{device_id}/` | `cam:read:<scope>` | Device |
| `GET /api/devices/{device_id}/streams` | `cam:read:<scope>` | Device |
| `GET /api/devices/{device_id}/photos` | `cam:read:<scope>` | Device |
| `GET /api/streams/{stream_id}/` | `cam:read:<scope>` | Device owning the stream |
| `GET /api/streams/{stream_id}/segments` | `cam:read:<scope>` | Device owning the stream |
| `GET /api/photos/{photo_id}/` | `cam:read:<scope>` | Device owning the photo |
| `POST /api/devices` | `cam:manage:<scope>` | Collection |
| `PUT /api/devices/{device_id}/` | `cam:manage:<scope>` | Device |
| `DELETE /api/devices/{device_id}/` | `cam:manage:<scope>` | Device |
| `POST /api/devices/{device_id}/token` | `cam:manage:<scope>` | Device |
| `POST /api/devices/{device_id}/camera/switch` | `cam:control:<scope>` | Device |
| `POST /api/devices/{device_id}/recording/start` | `cam:control:<scope>` | Device |
| `POST /api/devices/{device_id}/recording/stop` | `cam:control:<scope>` | Device |
| `POST /api/devices/{device_id}/photo` | `cam:control:<scope>` | Device |

The remaining routes carry no teamusers permission:

| Route | Authentication |
| --- | --- |
| `GET /healthz`, `GET /readyz` | None. |
| `GET /ws/register` | Device token (`Authorization: Bearer wdt_...`). |
| `GET /ws/device/{device_websocket_id}` | Registration ticket. |

The generated [API reference](/api/overview) repeats the scope rules for each operation through
its `x-teamusers-permission` metadata, which lists the `any` and `team` keys an endpoint accepts.
Because that extension has only those two slots, the `own` scope is documented only on this page.

## Catalog registration

Permission keys are catalog data in teamusers: **a role cannot carry a key that was not
registered first**. Register all nine keys once per teamusers deployment:

```sh
TEAMUSERS_URL=http://127.0.0.1:8081
TEAMUSERS_ADMIN_TOKEN='<teamusers admin token>'

for key in \
  cam:read:any cam:read:team cam:read:own \
  cam:manage:any cam:manage:team cam:manage:own \
  cam:control:any cam:control:team cam:control:own
do
  curl -sS -X POST "$TEAMUSERS_URL/permissions" \
    -H "Authorization: Bearer $TEAMUSERS_ADMIN_TOKEN" \
    -H 'Content-Type: application/json' \
    --data "{\"key\":\"$key\",\"description\":\"SmartClass webcam server\",\"registered_by\":\"smartclass-webcam-server\"}"
done
```

The nine keys:

- `cam:read:any`, `cam:read:team`, `cam:read:own`
- `cam:manage:any`, `cam:manage:team`, `cam:manage:own`
- `cam:control:any`, `cam:control:team`, `cam:control:own`

As a shorthand you may register one wildcard key per action instead - `cam:read:*`,
`cam:manage:*`, `cam:control:*` - and bind it wherever you would have bound all three scoped
keys.

## Roles and bindings

Attach registered keys to a role, then bind the role to users or groups. In the teamusers admin
API:

```http
PUT /roles/{role_id}/permissions
Content-Type: application/json

{"permission_keys": ["cam:read:any", "cam:control:any"]}
```

```http
POST /bindings
Content-Type: application/json

{
  "team_id": "01J8Z3TEAM000000000000001",
  "role_id": "01J8Z3ROLE000000000000001",
  "subject_kind": "user",
  "subject_id": "01J8Z3USER000000000000001",
  "condition": "resource.owner_id == subject.id"
}
```

The condition in this example turns the role's grants into ownership-limited ones: the binding
applies the condition to every permission the role carries, so the user may only reach devices
whose `owner_id` equals their subject. Omit `condition` for an unconditional binding, which is
the common case for `any`-scoped roles.

A typical split:

- a school administrator role: `cam:manage:any`, `cam:read:any`
- a teacher role: `cam:read:team`, `cam:control:team`
- a device owner role: `cam:read:own`, `cam:control:own`

## Deny and invalid keys

- **Deny keys.** A key prefixed with `!` (for example `!cam:control:any`) is a deny key. Register
  it exactly as written, including the `!`, and bind it to a role to block the permission: a
  matching deny grant cancels the allow grants for the same key, and the check reports the request
  as denied.
- **Invalid keys.** A key that is not registered cannot be attached to a role; the teamusers admin
  API rejects the request and names the offending key. A key whose scope is not one of
  `own|team|any|*` is rejected as invalid at registration time.
- **Fail closed.** A check against a key with no matching grant is denied, so a request for an
  unregistered or unbound permission answers `403`. A missing, malformed, or unverifiable token
  answers `401`.
- **Response shape.** A `401` answers with the teamusers decision body
  `{"allow": false, "reason": "..."}`. A `403` from the scope ladder is an RFC 9457 problem detail
  whose `detail` is `permission denied` followed by every key the ladder tried and the cause each
  check reported - for example
  `permission denied: cam:read:any (no matching grant); cam:manage:own (condition denied)` - so a
  denial that comes from a missing grant, a false condition and an unreachable scope reads
  differently without a look at the service log.

## Service credential

The server itself is a teamusers client. Its credential - `WEBCAM_TEAMUSERS_CLIENT_ID` and
`WEBCAM_TEAMUSERS_CLIENT_SECRET` - authorizes the permission checks behind every `/api` request,
and it is the only credential that can authenticate the service's filehouse calls: with
`WEBCAM_FILEHOUSE_URL` set, startup fails unless both are configured (the static token is
not used for storage). `WEBCAM_TEAMUSERS_SVC_TOKEN` is a fallback for permission checks when no
client credentials are set; it expires after ten minutes, so client credentials are the
recommended choice. Without any credential every authorization check fails closed. See the
[deployment guide](/guide/deploy) for provisioning.
