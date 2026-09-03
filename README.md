# tailrpproxy

`tailrpproxy` reflects SideStore RPPairing connections through a Tailscale
tailnet. Every transport exposes the same synthetic subnet route. The default
route is `10.7.0.1/32`; there is no fixed proxy port or application framing
protocol.

The initial release target is Linux on amd64 and arm64. macOS and Windows are
not currently supported release targets.

## Data paths

The daemon supports three transports:

- `native` uses the host's existing `tailscaled`. It creates a Linux TUN,
  routes the synthetic address to that interface, swaps each matching IPv4
  packet's source and destination addresses, and reinjects the packet.
- `tsnet` runs an embedded, persisted Tailscale node. It advertises the
  synthetic route and reflects each intercepted TCP flow to its authenticated
  source peer on the original destination port.
- `operator` replaces a Tailscale Kubernetes Operator Connector's
  `tailscaleContainer`. It consumes the Operator-generated config and state
  Secret and uses the same tsnet subnet-reflection path.

The default `auto` transport selects a runtime from explicit environment
signals:

1. `operator` when both `TS_EXPERIMENTAL_VERSIONED_CONFIG_DIR` and
   `TS_KUBE_SECRET` are present.
2. `tsnet` when `TS_AUTHKEY`, `TS_AUTH_KEY`, or `TS_CLIENT_SECRET` is present,
   or when `RPPROXY_TSNET_STATE_DIR` contains `tailscaled.state`.
3. `native` when neither environment is detected.

A partial Operator environment is rejected instead of silently falling back.
Set `RPPROXY_TRANSPORT` or pass `-transport` to force `native`, `tsnet`, or
`operator`; a command-line flag overrides the environment variable.

## Native host mode

Native mode needs all of the following:

- Linux with `/dev/net/tun`.
- `CAP_NET_ADMIN` or root.
- `net.ipv4.ip_forward=1`.
- A running host `tailscaled` reachable through its LocalAPI socket.
- Subnet-route SNAT disabled so the TUN receives the iPhone's Tailscale source
  address.
- Stateful subnet filtering disabled so the reflected connection can return to
  the iPhone.

Start the daemon as root:

```sh
sudo sysctl -w net.ipv4.ip_forward=1
sudo tailrpproxy -transport=native
```

The daemon adds `10.7.0.1/32` to the host's existing advertised routes. It also
sets `NoSNAT` and disables stateful subnet filtering when the synthetic route is
the host's only advertised route. If the host already advertises another route
with incompatible settings, the daemon fails instead of changing global
subnet-router behavior. Configure those settings explicitly before retrying:

```sh
sudo tailscale set \
  --advertise-routes=10.7.0.1/32,EXISTING_ROUTES \
  --snat-subnet-routes=false \
  --stateful-filtering=false
```

Approve or auto-approve `10.7.0.1/32` in the tailnet policy. Native mode leaves
the advertised route in the host Tailscale preferences after shutdown; closing
the process removes the ephemeral TUN and its kernel route.

## Standalone tsnet mode

Run an embedded tsnet subnet reflector and persist its identity:

```sh
TS_AUTHKEY='tskey-auth-REPLACE_ME' \
tailrpproxy \
  -transport=tsnet \
  -tsnet-state-dir=/var/lib/tailrpproxy \
  -tsnet-hostname=tailrpproxy \
  -tsnet-tags=tag:sidestore-egress
```

The auth key is only needed when persisted tsnet state does not contain an
authenticated node. Approve or auto-approve the synthetic route advertised by
the tsnet node. The optional tag must be owned by the auth key or OAuth client.
OAuth login uses `TS_CLIENT_SECRET` and requires at least one `-tsnet-tags`
value that the OAuth client owns.

To authenticate interactively, select `tsnet` explicitly and omit auth
credentials:

```sh
tailrpproxy \
  -transport=tsnet \
  -tsnet-state-dir=/var/lib/tailrpproxy \
  -tsnet-hostname=tailrpproxy
```

The daemon prints the Tailscale login URL and waits until authentication
completes. The normal startup timeout does not apply while waiting for this
interactive login; press Ctrl+C to cancel. Auto mode cannot infer interactive
intent, so an empty state directory without credentials still selects `native`.

## Container usage

The image uses `auto` transport by default. Native mode must run in the host
network namespace and access both the host TUN device and tailscaled socket:

```sh
docker run --rm \
  --network=host \
  --user=0 \
  --cap-add=NET_ADMIN \
  --device=/dev/net/tun:/dev/net/tun \
  -v /var/run/tailscale/tailscaled.sock:/var/run/tailscale/tailscaled.sock \
  -e RPPROXY_TRANSPORT=native \
  ghcr.io/eseiker/tailrpproxy:latest
```

Docker Desktop cannot use the host macOS Tailscale network namespace for this
path. Test native mode on a Linux host or VM whose `tailscaled`, TUN, and daemon
share the same network namespace.

Run an embedded tsnet node without elevated network privileges:

```sh
docker volume create tailrpproxy-state

docker run --rm \
  -e TS_AUTHKEY \
  -e RPPROXY_TSNET_TAGS=tag:sidestore-egress \
  -v tailrpproxy-state:/var/lib/tailrpproxy \
  -p 9002:9002 \
  ghcr.io/eseiker/tailrpproxy:latest
```

On later runs the persisted `tailscaled.state` selects `tsnet` even when
`TS_AUTHKEY` is omitted. Use `RPPROXY_TRANSPORT=tsnet` to force tsnet while
testing an empty state directory or interactive login.

Container environment defaults are:

- `RPPROXY_TRANSPORT=auto`
- `RPPROXY_HEALTH_LISTEN=:9002`
- `RPPROXY_SYNTHETIC_ROUTE=10.7.0.1/32`
- `RPPROXY_TSNET_STATE_DIR=/var/lib/tailrpproxy`
- `RPPROXY_NATIVE_TUN_NAME=tailrpproxy`

`RPPROXY_TAILSCALED_SOCKET`, `RPPROXY_TSNET_HOSTNAME`, and
`RPPROXY_TSNET_TAGS` also map to their corresponding command-line options.

The standalone binary serves health and aggregate metrics on
`127.0.0.1:9090` by default:

- `/healthz`
- `/readyz`
- `/metrics`

## Kubernetes Operator

Build and publish the container, then set the image in `deploy/operator`:

```sh
docker build -t ghcr.io/eseiker/tailrpproxy:dev .
docker push ghcr.io/eseiker/tailrpproxy:dev

cd deploy/operator
kustomize edit set image tailrpproxy=ghcr.io/eseiker/tailrpproxy:dev
kubectl apply -k .
```

The base Connector omits `spec.tags`, so the Operator uses Helm
`proxyConfig.defaultTags`. If `spec.tags` is present, that list replaces the
Operator defaults. The Operator OAuth client must own the selected tags.
The Operator encodes the selected tags into the one-time auth key consumed by
`tailrpproxy`; the versioned config file does not carry tags. Consequently,
`RPPROXY_TSNET_TAGS` only affects standalone `tsnet` mode and never overrides
`Connector.spec.tags` in Operator mode.

The tailnet policy must auto-approve the synthetic route, allow the iPhone to
reach that route, and allow the Connector tag to connect back to the iPhone's
RPPairing port.

Replacing `tailscaleContainer` relies on the Operator's internal config and
state Secret contract. Test the image against each Operator upgrade and keep
the `tailscale.com` module version aligned with the deployed Operator.

## Build

```sh
make test
make vet
make build-linux
```

GitHub Actions uploads Linux amd64 and arm64 binaries as workflow artifacts.
Pushing a `v*` tag also publishes both binaries and `SHA256SUMS` in a GitHub
prerelease. Pushes to `main` and `v*` tags publish a multi-platform image to
`ghcr.io/eseiker/tailrpproxy`.
