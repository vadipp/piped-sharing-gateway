# Piped sharing gateway

Permanent, revocable links to a single video through an existing private Piped
backend. Visitors receive no Piped account or identity-provider session. A small
standalone player provides quality selection, speed, seeking, fullscreen and
supported subtitles, without search, recommendations or account navigation.

## Architecture

The service has **two separate listeners**:

- Public, `:8080`: `/s/TOKEN`, `/s/TOKEN/video`, and opaque resource URLs beneath
  `/s/TOKEN/r/`. Only the share's video and its backend-issued resources are served.
- Management, `:8081`: create/list/revoke shares. Keep this listener private;
  authenticate users at the reverse proxy and inject a server-side bearer secret.

Tokens contain 256 random bits. SQLite stores only their SHA-256 hashes, fixed
video IDs, creation timestamps and revocation state. There is no expiry. Keep the
database and its WAL safe and backed up; use SQLite's online backup or stop the
service before copying the database files. Restore the database to preserve links.
A token is a bearer capability: anyone with the link can forward it.

The gateway calls `/streams/VIDEO_ID` on the configured internal Piped backend,
without visitor credentials. It decodes an allowlist of metadata fields and
registers only that response's media, thumbnail and subtitle URLs. The public API
never accepts a target URL or video ID. Its resource URLs are scoped to a share;
query-string overrides and arbitrary proxying are rejected. All media flows
through the configured internal Piped proxy virtual host. Neither upstream URLs
nor signatures are disclosed to the browser.

HLS manifests are rewritten recursively, including URI attributes for keys and
alternate renditions. Foreign origins and redirects fail closed. For ordinary
VOD, the player generates DASH from the backend's adaptive formats, or offers
combined-file playback with quality and speed controls inside the player settings.
Browsers without Shaka support retain native playback with an in-player quality
selector and their native speed controls. Upstream DASH-only/OTF manifests without
usable formats are currently unsupported. Formats, quality and availability are
limited by the existing Piped extractor. Non-proxied subtitles are omitted.

No video is downloaded in advance. Playback URLs are refreshed from Piped after
the five-minute metadata cache expires; link lifetime is independent of upstream
media URL lifetime. Reload the player if an already-open stream expires. Live
videos can be shared while available; permanent links do not archive live streams.

## Configuration

| Variable | Meaning |
| --- | --- |
| `PUBLIC_ORIGIN` | Public HTTPS share origin, e.g. `https://share.example.com` |
| `ADMIN_ORIGIN` | Authenticated frontend origin allowed to create/revoke shares |
| `BACKEND_ORIGIN` | Internal Piped backend origin, e.g. `http://piped:8080` |
| `MEDIA_ORIGIN` | Exact media origin returned by Piped, e.g. `https://media.example.com` |
| `MEDIA_UPSTREAM` | Internal proxy ingress, e.g. `http://nginx-piped:80`; requests use the `MEDIA_ORIGIN` Host |
| `ADMIN_SECRET_FILE` | File containing a random management secret of at least 32 characters |
| `ADMIN_SECRET` | Alternative to a secret file; never send this value to a browser |
| `DATABASE_PATH` | Default `/data/shares.db` |
| `PUBLIC_LISTEN` | Default `:8080` |
| `ADMIN_LISTEN` | Default `:8081` |

Origins must have no credentials, path or query. See `compose.example.yml`. Create
the data directory owned by UID/GID 65532, create a random secret file readable by
that UID, and join the existing internal Piped network. Do not expose port 8081 to
the internet. Start with the example resource limits and measure your workload.
The image is non-root, supports a read-only root filesystem, and includes a native
`/gateway healthcheck` command. The share database is the only writable mount.

## Reverse proxy integration

Serve the public listener on a dedicated hostname with TLS. Do not forward
cookies, Authorization or identity headers. Disable access logging of URLs on
**every ingress layer**, because the token is in the path. Disable proxy caching
for these routes. The application sets no-referrer, no-store, noindex and CSP
headers and does not log requests. Avoid analytics and external assets.

Apply bounded per-client request rates at ingress, with a burst suitable for a
player's concurrent media requests. The service bounds concurrent extractions,
streams and cached share resource sets. This is intended for personal sharing,
not an unrestricted public video hosting service.

On the authenticated frontend hostname, route `/_piped/shares` and
`/_piped/shares/ID` to the management listener's `/shares` paths. Retain the normal
Authelia `auth_request`. Overwrite Authorization with `Bearer SERVER_SECRET`,
clear Cookie and identity headers, preserve the browser Origin and disable proxy
caching. Keep that configuration private. Validate Origin before making requests
outside a browser as well: mutations require exactly `ADMIN_ORIGIN`.

Do **not** grant an anonymous Authelia user access to the main instance, nor add
anonymous exceptions to the ordinary API/proxy. A public share hostname should
route only to this service. Parent-domain cookies may arrive at that hostname;
they are ignored and must not be forwarded upstream.

The companion [Piped fork](https://github.com/vadipp/Piped) supports optional
runtime configuration in an operator-provided script loaded before the app:

```js
window.PIPED_SHARING_API = "/_piped/shares";
```

Without this setting the frontend retains normal sharing. With it, the existing
dialog requests a capability, keeps the copy/open/QR/timecode controls, and hides
playlist sharing. The secret belongs only at the reverse proxy.

### Management API

All requests require `Authorization: Bearer SECRET` on the private listener.
POST and DELETE also require `Origin: https://piped.example.com`.

- `POST /shares`, JSON `{"videoId":"abcdefghijk"}` → `201`,
  `{"url":"https://share.example.com/s/TOKEN","id":"HASH"}`.
- `GET /shares` → latest 1000 share records, including IDs, video IDs, timestamps
  and revoked status. Raw tokens are never returned or retained after creation.
- `DELETE /shares/HASH` → `204`; subsequent page, metadata and resource requests
  fail immediately. Already-delivered or in-flight bytes cannot be recalled.

An authenticated browser can list or revoke through the frontend proxy using its
normal session cookie. A separate share-management UI is not included.

## Development and images

```sh
npm ci --ignore-scripts
npm run vendor
go test -race ./...
go vet ./...
go build .
```

Shaka Player is bundled locally at its lockfile version; no CDN is needed.
Tests cover cross-share isolation, byte ranges, credential stripping, token
persistence, revocation, management authentication/CSRF, manifest URL rewriting
and redirect rejection. CI tests and builds the image on hosted runners, runs a
restricted-container smoke test, and publishes a revision-tagged image bundle
with checksums as a GitHub release. Verify the revision, archive checksum and
loaded image ID before deployment. Releases are build artifacts, not deployments.

## License

MIT. Bundled Shaka Player is Apache-2.0; its license remains in the image's
embedded assets. Dependency licenses remain with their respective projects.
