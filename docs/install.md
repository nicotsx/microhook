# Install

Microhook is host-binary-first. The primary v1 deployment target is a Linux host with a locally installed binary managed by `systemd`.

## Supported v1 Target

- Linux on `amd64` and `arm64`
- Local SQLite storage
- `systemd` service management
- Reverse proxy only when remote access is explicitly required

## Install From Release Tarball

1. Download the correct artifact for your host.
2. Extract it:

```bash
tar -xzf microhook_<version>_linux_amd64.tar.gz
cd microhook_<version>_linux_amd64
```

3. Install the binary:

```bash
sudo install -m 0755 microhook /usr/local/bin/microhook
```

4. Create the service user and directories:

```bash
sudo useradd --system --home /var/lib/microhook --shell /usr/sbin/nologin microhook
sudo install -d -m 0750 -o microhook -g microhook /etc/microhook /var/lib/microhook
```

5. Copy the example config and replace the sample token:

```bash
sudo install -m 0600 examples/config.yml /etc/microhook/config.yml
sudo /usr/local/bin/microhook generate-token
```

6. Validate the config before starting the service:

```bash
sudo /usr/local/bin/microhook validate-config -config /etc/microhook/config.yml
```

7. Install the `systemd` unit:

```bash
sudo install -m 0644 systemd/microhook.service /etc/systemd/system/microhook.service
sudo systemctl daemon-reload
sudo systemctl enable --now microhook
```

8. Confirm the service is healthy:

```bash
curl http://127.0.0.1:9464/healthz
systemctl status microhook
journalctl -u microhook -f
```

## Default Paths

- Binary: `/usr/local/bin/microhook`
- Config: `/etc/microhook/config.yml`
- Data: `/var/lib/microhook/microhook.db`

Microhook also respects `MICROHOOK_CONFIG`. If that environment variable is set, it overrides the default config path.

## systemd Notes

The packaged unit file is at `packaging/systemd/microhook.service`.

It intentionally stays conservative:

- Runs as a dedicated `microhook` user
- Restarts on failure
- Sets `NoNewPrivileges=true`
- Uses `UMask=0077`
- Leaves room for actions that need real host access

If you add stricter `systemd` hardening, re-test every action. Overly aggressive sandboxing can break legitimate `cwd`, filesystem, or service-control operations.

## Docker

Docker is a convenience distribution path, not the primary model.

Build the image locally:

```bash
make docker-build
```

Run it with an explicit config mount and persistent storage:

```bash
docker run --rm \
  -p 127.0.0.1:9464:9464 \
  -v "$PWD/config.yml:/etc/microhook/config.yml:ro" \
  -v microhook-data:/var/lib/microhook \
  microhook:local serve
```

Use the container image only when that tradeoff makes sense for your environment. If your actions need direct host access, a host-installed binary is usually simpler and clearer.

## Reverse Proxy Guidance

Microhook binds to `127.0.0.1:9464` by default. Keep that default when the caller is local.

If remote callers are required:

- Put Microhook behind a reverse proxy or other trusted network boundary
- Keep bearer tokens secret
- Do not expose it broadly to the public internet
- Reconfirm that every configured action is safe for remote triggering
