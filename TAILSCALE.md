# Running CronLite on Tailscale

CronLite can join your [Tailscale](https://tailscale.com) tailnet with an embedded node. No Tailscale app or sidecar is needed. This gives you two features, each opt-in:

- **Tailnet delivery** (`WEBHOOK_ALLOW_TAILNET=true`): jobs can call services on your tailnet that have no public address, e.g. `https://invoices.<tailnet>.ts.net/hooks/invoice`.
- **Tailnet access** (`TS_LISTEN=true`): the API, admin UI and MCP endpoint are served at `https://cronlite.<tailnet>.ts.net`. With `TS_DISABLE_PUBLIC_LISTENER=true`, CronLite has no public port at all.

This guide takes you from an empty tailnet to both features working. Every setting is described in the [Operator Guide](OPERATORS.md#tailscale).

## What you need

- A Tailscale tailnet where you can edit DNS settings and access controls.
- A CronLite image or binary **built with Tailscale support**. The default build leaves it out, to keep the binary small (24.6 MB instead of 43.8 MB).
- Persistent storage for the node's state, i.e. its identity, keys and HTTPS certificate.

## 1. Prepare the tailnet

In the [Tailscale admin console](https://login.tailscale.com/admin):

1. **DNS:** make sure **MagicDNS** is on; CronLite refuses to start without it. For tailnet access, also turn on **HTTPS Certificates**. Note your tailnet DNS name (e.g. `tail1234.ts.net`); every URL below uses it.

   Certificates are published in public Certificate Transparency logs, so the node's name (`cronlite.tail1234.ts.net`) becomes public. It stays unreachable from outside the tailnet.

2. **Access controls:** give CronLite a tag, and grant only the traffic it needs:

   ```json
   "tagOwners": {
     "tag:cronlite":        ["autogroup:admin"],
     "tag:cronlite-target": ["autogroup:admin"]
   },
   "grants": [
     // people → CronLite's admin UI and API (tailnet access)
     { "src": ["autogroup:member"], "dst": ["tag:cronlite"],        "ip": ["tcp:443", "tcp:80"] },
     // CronLite → the services its jobs may call (tailnet delivery)
     { "src": ["tag:cronlite"],     "dst": ["tag:cronlite-target"], "ip": ["tcp:443"] }
   ]
   ```

   Adjust the ports to what your services listen on. **If your policy still has the default allow-all rule** (`"src": ["*"], "dst": ["*"]`), these grants restrict nothing: CronLite can reach every machine on your tailnet, and `WEBHOOK_TAILNET_HOSTS` is then the only limit on which namespace calls which. Remove allow-all before real use.

3. **Tag the services CronLite should call.** Tag servers, not your own laptop: a tagged device stops belonging to you, so it would drop out of the `autogroup:member` grant above. On each target:

   ```bash
   sudo tailscale up --advertise-tags=tag:cronlite-target
   ```

   If the device already uses non-default flags, `tailscale up` asks you to repeat them. You can also set tags from the machine's menu on the Machines page.

4. **Auth key:** under **Settings → Keys → Generate auth key**:

   | Option | Set to | Why |
   |---|---|---|
   | Reusable | On | Lets you recreate containers or reset the state |
   | Ephemeral | **Off** | The node keeps its identity in its state directory; an ephemeral node is removed when it goes offline |
   | Pre-approved | On | Needed if device approval is enabled |
   | Tags | `tag:cronlite` | Tagged nodes do not expire and get the grants above |

   Treat the key like a password: keep it in a secret store or an env file readable only by you, never in an image, a repository or a chat. It is only used for the first login; after that, the node starts from its saved state.

## 2. Build with Tailscale support

```bash
docker build --build-arg TAGS=tailscale -t cronlite:tailscale .
```

Or, for a plain binary:

```bash
go build -tags tailscale -o cronlite ./cmd/cronlite
```

A build without the tag refuses `WEBHOOK_ALLOW_TAILNET` and `TS_LISTEN`, both at startup and in `cronlite validate`.

## 3. Run CronLite

Put the key in an env file, e.g. `cronlite-ts.env` containing `TS_AUTHKEY=tskey-auth-…`, readable only by you. Then:

```bash
docker run -d --name cronlite \
  --env-file cronlite-ts.env \
  -e DATABASE_URL='postgres://…' \
  -e MIGRATE_ON_START=true \
  -e ADMIN_ENABLED=true \
  -e WEBHOOK_ALLOW_TAILNET=true \
  -e WEBHOOK_TAILNET_HOSTS='billing=invoices;ops=*' \
  -e TS_LISTEN=true \
  -e TS_STATE_DIR=/home/cronlite/tailscale \
  -v cronlite-ts:/home/cronlite \
  -p 8080:8080 \
  cronlite:tailscale
```

- **Mount the volume on `/home/cronlite`**, as shown. The image runs as the `cronlite` user (uid 1000), and Docker creates a volume on a path that doesn't exist in the image (e.g. `/data`) owned by root, so the node could not write its state. A bind mount works too, if the host directory is owned by uid 1000.
- `WEBHOOK_TAILNET_HOSTS` is required with `WEBHOOK_ALLOW_TAILNET=true`. It lists which tailnet hosts each namespace may call, as `namespace=host,host;namespace=host`. Hosts are single names without the tailnet suffix: `invoices`, not `invoices.tail1234.ts.net`. `*` as a host allows every tailnet host. `*` as a namespace applies to every namespace. A namespace may call its own hosts plus the hosts of the `*` entry, so a `*` entry grants those hosts to every namespace. Namespace names are matched exactly (case-sensitive); host names are matched case-insensitively. A namespace not listed can reach no tailnet host. Examples:
  - `billing=invoices;ops=*`: `billing` may call only `invoices`; `ops` may call any tailnet host.
  - `*=*`: every namespace may call every tailnet host. This is the old, installation-wide behaviour.
- `TS_HOSTNAME` (default `cronlite`) sets the node's name, and so the URL.
- tsnet sends diagnostic logs to Tailscale by default. Add `-e TS_NO_LOGS_NO_SUPPORT=true` to turn that off.

## 4. Check that it works

1. **Logs.** Within a few seconds you should see:

   ```
   tailnet: node up on tail1234.ts.net (addresses [100.x.y.z fd7a:…])
   tailnet: webhook delivery enabled for *.tail1234.ts.net (namespaces: billing, ops)
   cronlite: http server listening on https://cronlite.tail1234.ts.net
   ```

   With `TS_LISTEN`, about 30 seconds later on the first run:

   ```
   tailnet: https certificate ready for cronlite.tail1234.ts.net (32.4s)
   ```

   HTTPS requests that arrive before this line fail the TLS handshake. Later restarts reuse the stored certificate.

2. **Machines page:** `cronlite` appears with `tag:cronlite` and key expiry disabled. Leave Funnel off; it would expose CronLite publicly.

3. **Tailnet access,** from a device signed in to your tailnet:

   ```bash
   curl https://cronlite.tail1234.ts.net/health?verbose=true
   ```

   The response should be `{"database":"healthy","status":"ok","tailnet":"up"}`. The admin UI is at `https://cronlite.tail1234.ts.net/admin`. Plain `http://` redirects to HTTPS. API requests still need an API key, as on the public listener.

4. **Tailnet delivery:** create a job whose `webhook_url` is a tagged service's MagicDNS name, e.g. `http://invoices.tail1234.ts.net:8080/hook`. With `WEBHOOK_TAILNET_HOSTS='billing=invoices;ops=*'` from section 3, create it with an API key in namespace `billing`. Then trigger it:

   ```bash
   curl -X POST https://cronlite.tail1234.ts.net/jobs/<job-id>/trigger -H "Authorization: Bearer $CRONLITE_API_KEY"
   ```

   The service receives a signed `POST` from CronLite's tailnet address.

## Tailnet-only mode

Once tailnet access works, add `-e TS_DISABLE_PUBLIC_LISTENER=true` and drop `-p 8080:8080`. CronLite then only answers on the tailnet. Anything that probes the container port, such as a Railway, Docker or Kubernetes health check, will fail, so remove those probes. To monitor the instance, scrape `/metrics` over the tailnet instead.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `this binary was built without Tailscale support` | The image or binary was built without the tag. Rebuild with `TAGS=tailscale` / `-tags tailscale`. |
| `TS_STATE_DIR: required when …` | Set `TS_STATE_DIR` to a persistent, writable directory. |
| `failed to start tailnet node: … invalid key` | The auth key is wrong, revoked or expired, or a single-use key was already used. Generate a new one. |
| Startup gives up after 60s and the log mentions a login URL | No usable auth key and no logged-in state. Set `TS_AUTHKEY`. |
| `permission denied` writing the state directory | The volume is owned by root. Mount it on `/home/cronlite`, or give uid 1000 ownership of the bind-mounted directory. |
| `MagicDNS is disabled on tailnet …` | Turn on MagicDNS in the admin console (DNS). |
| `you must enable HTTPS in the admin panel` | Turn on HTTPS Certificates in the admin console (DNS), or don't set `TS_LISTEN`. |
| TLS handshake error on the first HTTPS request | The certificate is still being issued. Wait for `tailnet: https certificate ready`. |
| `https certificate not ready: … server misbehaving` | The host can't resolve `acme-v02.api.letsencrypt.org`. This happens when CronLite runs on a machine whose own Tailscale client has taken over system DNS. Run `tailscale set --accept-dns=false` there, and restore `/etc/resolv.conf` if needed, or add global nameservers in the admin console. |
| Webhook fails with `destination blocked: … carrier-grade NAT address` | The URL uses a `100.x` IP. Tailnet delivery only works with MagicDNS names under your tailnet, e.g. `invoices.tail1234.ts.net`. |
| Webhook fails with `not a tailnet address` | The name resolved to something other than a Tailscale node. Check the hostname. |
| Webhook fails with `this node's own tailnet address` | The URL points at CronLite itself. This is blocked on purpose. |
| Creating a job gives 422 `webhook_host_not_allowed`, or a delivery fails with `tailnet host not allowed for namespace` | The job's namespace is not allowed to call that host. Add the host to that namespace in `WEBHOOK_TAILNET_HOSTS` and restart. The admin UI says "This namespace may not call that tailnet host. Ask an operator to add it to WEBHOOK_TAILNET_HOSTS." and MCP names `WEBHOOK_TAILNET_HOSTS` in its error. If a host is removed later, existing jobs can still be edited when their URL is unchanged, but their deliveries are blocked and not retried. |
| Webhook times out or is refused | No grant from `tag:cronlite` to that service and port, or the service isn't listening on it. |
| The node shows up as `cronlite-1` | Another machine is already named `cronlite`, or the state directory was lost and the node registered again. Delete the stale machine and keep the state volume. |
| The admin UI is unreachable from your laptop | The laptop isn't signed in to this tailnet, is tagged (and so outside `autogroup:member`), or no grant allows it to reach `tag:cronlite:443`. |

## Security notes

- **Off by default.** Without `WEBHOOK_ALLOW_TAILNET` and `TS_LISTEN`, nothing Tailscale-related runs. Without the build tag, the code isn't even in the binary.
- **The SSRF guard is unchanged for everything else.** Only subdomains of your own tailnet take the tailnet path. IP literals, other tailnets' hosts and look-alike domains go through the normal guard, which still blocks `100.64.0.0/10`. Every redirect hop is checked again.
- **Each namespace reaches only the hosts listed for it in `WEBHOOK_TAILNET_HOSTS`;** keep Tailscale grants to the services and ports CronLite needs.
- **The tailnet grants no extra trust.** Requests over the tailnet go through the same API key, admin session and rate-limit checks as public ones.
- **The auth key is never logged** and never printed by `cronlite config`.
