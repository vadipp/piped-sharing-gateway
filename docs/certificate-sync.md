# Optional certificate synchronization utility

`cmd/certsync` is an independent operator utility for copying a certificate from
an authoritative renewal host to another TLS endpoint. The sharing gateway does
not load certificates or need this tool. Deployment configuration remains private.

A receiving host uses a dedicated SSH identity restricted at the source to a
forced command exporting exactly `fullchain.pem` and `privkey.pem` in a tar
archive. For example, an operator can restrict the authorized key with `restrict`,
a specific source address, and a fixed `tar -chf - -C /path/to/certificate`
command listing those two files. Do not grant this identity a shell, forwarding,
a PTY or the ability to choose a path. Source-side files and authorized-key
configuration must remain writable only by their administrator.

On the receiver, configure OpenSSH with `BatchMode yes`, `IdentitiesOnly yes`,
`StrictHostKeyChecking yes`, a dedicated known-hosts file, and the restricted
identity. Obtain the host key through an already trusted administrative channel.
Never disable host-key checking or copy the issuer's DNS credential to receivers.

The utility fetches a bounded archive, rejects extra/duplicate files, validates
certificate/key agreement, hostname coverage, chain trust and remaining validity,
then installs a versioned pair. It atomically switches a symlink so the two files
always come from the same version, retains the previous version for rollback,
and can write a durable reload notification. An unchanged certificate causes no
replacement or new notification. It never logs PEM or private-key data.

```sh
certsync --ssh-config /run/secrets/ssh_config --ssh-alias certificate-source \
  --hostname video.example.com --destination /certificates \
  --current live/shared --changed-file /certificates/needs-reload
```

Build the optional image with `Dockerfile.certsync`. Run it as a periodic, isolated
one-shot Compose service with a read-only root filesystem, no capabilities, a
64 MiB memory/zero-swap limit, CPU/PID bounds, read-only SSH configuration/key/
known-hosts mounts and one writable certificate-directory mount. It needs no
listening ports and must not receive the Docker socket. The image uses UID0 to
read root-only credentials and install root-only private keys; drop all container
capabilities and enable no-new-privileges.

The web server must see the containing certificate directory so it can resolve
the relative version symlink. Do not bind-mount only the symlink's current target.
After a successful sync, the host scheduler should test for the notification,
validate nginx configuration, reload nginx, and remove the notification only when
both succeed. A failed fetch or validation preserves the previous pair. A failed
nginx reload leaves the notification for retry. Polling hourly also catches a
missed renewal transfer without coupling issuer availability to receivers.

CI tests validation, archive boundaries, atomic installation, unchanged pairs,
key permissions and retention, and exports the optional image with checksums in
the same revision-tagged build release.
