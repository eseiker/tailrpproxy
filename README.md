# tailrpproxy

> **Usage:** `docker run ghcr.io/eseiker/tailrpproxy:latest`

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
3. `native` when neither environment is detected and both `/dev/net/tun` and
   the configured or default tailscaled LocalAPI socket are available.
4. `tsnet` when native prerequisites are unavailable. With no credentials or
   persisted state, this path prints an interactive login URL.

A partial Operator environment is rejected instead of silently falling back.
Set `RPPROXY_TRANSPORT` or pass `-transport` to force `native`, `tsnet`, or
`operator`; a command-line flag overrides the environment variable.
Explicit `native` mode does not fall back, so configuration errors remain
visible when that transport is requested intentionally.

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

The daemon prints the Tailscale login URL once and waits until authentication
completes. The normal startup timeout does not apply while waiting for this
interactive login; press Ctrl+C to cancel. Auto mode also uses this path when
the native TUN device or tailscaled LocalAPI socket is unavailable.

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

The plain manifest in `deploy/operator/tailrpproxy.yaml` replaces a Connector's
Tailscale container with `tailrpproxy` and advertises the default synthetic
route:

```sh
kubectl apply -f deploy/operator/tailrpproxy.yaml
kubectl get proxyclass,connector tailrpproxy
```

The `ProxyClass` and `Connector` are cluster-scoped. The Operator creates the
StatefulSet and its state Secret in the Operator namespace, normally
`tailscale`; a namespace on either custom resource does not move that workload.

The Operator performs authentication and supplies the one-time auth key and
mutable state Secret. Do not add `TS_AUTHKEY` or `TSNET_FORCE_LOGIN` to the
`ProxyClass`. The manifest omits `spec.tags`, so the Connector uses the
Operator's `proxyConfig.defaultTags` (normally `tag:k8s`). If you add
`spec.tags`, the Operator OAuth client must own those tags.

Before applying the manifest:

- Allow the Operator namespace to run the privileged Connector Pod required by
  the Operator's subnet-router template. A Pod Security `baseline` policy will
  reject it.
- Ensure every node eligible to run the Pod can reach the Kubernetes API
  Service. `tailrpproxy` cannot read or update its state Secret otherwise.
- Auto-approve or manually approve `10.7.0.1/32`, and allow the iOS device to
  reach that route and the Connector tag to connect back to the device's
  RPPairing port.

The two `reloader.stakater.com/auto: "false"` annotations are intentional. The
Operator state Secret changes during normal tsnet startup. A Reloader instance
started with `--auto-reload-all=true` can otherwise roll the StatefulSet during
authentication and leave startup failing with `tsnet.Up: context canceled`.

The image uses `RPPROXY_SYNTHETIC_ROUTE=10.7.0.1/32` by default. If you change
the Connector's advertised route, set the same value on
`spec.statefulSet.pod.tailscaleContainer.env`. One Connector handles any number
of iOS peers dynamically; it does not need a per-device address map.

Replacing `tailscaleContainer` relies on the Operator's internal config and
state Secret contract. Test the image against each Operator upgrade and keep
the `tailscale.com` module version aligned with the deployed Operator. Pin a
release tag or digest instead of `latest` for reproducible production rollouts.

## Build

```sh
make test
make vet
make version
make build
make build-linux
```

`RPP_REVISION` contains the tailrpproxy patch revision. All supported build
paths derive the main version from the pinned stable `tailscale.com` module and
stamp Tailscale's `longStamp` and `shortStamp`. For example, Tailscale v1.102.3
with revision 1 reports `1.102.3-rpp.1` from `tailrpproxy -version` and in the
Tailscale admin console instead of `ERR-BuildInfo`. Increment `RPP_REVISION`
for tailrpproxy-only releases. A Tailscale dependency update changes the
leading version and resets the revision to 1. Release CI compares the new tag
with the preceding `v*-rpp.*` tag and enforces both the reset and monotonically
increasing revisions for the same Tailscale version.

Dependabot tracks only the direct `tailscale.com` module and opens a weekly
update pull request. Because the checked-in version is stable, Dependabot keeps
following stable releases; CI rejects prerelease and pseudo-version pins. Each
pull request runs race tests, vet, Linux amd64/arm64 builds, and the
multi-platform container build without publishing artifacts. After every check
passes, a trusted workflow validates that the PR contains only the expected
dependency and revision files. It resets `RPP_REVISION` to 1 and reruns CI when
needed, then squash-merges the verified commit, creates its version tag, and
dispatches release CI. `hack/update-tailscale.sh` performs the same stable-only
update locally when an immediate refresh is needed. The script also aligns the
directly imported `wireguard-go` revision with the version required by the
selected stable Tailscale module.

GitHub Actions uploads Linux amd64 and arm64 binaries as workflow artifacts.
Pushing a tag such as `v1.102.3-rpp.1` also publishes both binaries and
`SHA256SUMS` in a normal GitHub Release. CI rejects a release tag that does not
match the version derived from `go.mod` and `RPP_REVISION`. Pushes to `main`
and version tags publish a multi-platform image to
`ghcr.io/eseiker/tailrpproxy`.

## Credits

- [SideStore](https://github.com/SideStore/SideStore) for RPPairing-based
  on-device app installation and refresh.
- [xddxdd/sidestore-vpn](https://github.com/xddxdd/sidestore-vpn) for the
  network-wide packet-reflection design and its public-domain implementation.
- [StosVPN](https://github.com/SideStore/StosVPN) for the SideStore loopback
  tunnel approach.
- [Tailscale](https://github.com/tailscale/tailscale) for the tailnet, tsnet,
  and Kubernetes Operator building blocks used by this project.

## License

tailrpproxy's original code is released into the public domain under the
[Unlicense](LICENSE). Third-party components and dependencies retain their
respective licenses.
