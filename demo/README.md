# Snapback cross-cluster demo

Deploys Snapback source and destination on two separate k3d clusters running on different hosts
on the same LAN. The source replicates Velero backups to the destination over a peer-to-peer
[iroh](https://iroh.computer/) QUIC connection.

## Prerequisites

- Both hosts have k3d + UDS CLI installed
- The two hosts can reach each other over UDP on port 51820 (LAN, no NAT between them)
- You have run `uds zarf package create` from the repo root to produce
  `zarf-package-snapback-*-upstream.tar.zst`

## How it works

Each bundle deploys the Snapback package with a different `role` (`source` or `destination`).
The peat-node sidecar on each side is given the other side's iroh **endpoint ID** and **host
address** at deploy time. Endpoint IDs are deterministic: given the same `sharedKey` and
`nodeId`, `peat-node derive-id` always produces the same ID, so each side can know the other's
ID before either is deployed.

k3d's serverlb publishes `host:51820/udp → node:51820` (nginx stream proxy). Because UDS Core
uses MetalLB (not k3d's klipper servicelb), a pod `hostPort` is required to make the CNI wire a
node→pod DNAT — the bundles enable this via `peat.hostPort.enabled`, and the chart ships a
matching Pepr `RestrictHostPorts` exemption automatically.

## Setup

### 1. Generate a shared key (do this once, use the same value on both sides)

```bash
openssl rand 32 | base64
```

Keep this value secret. The throwaway dev key baked into the bundles
(`AAAA...=`, 32 zero bytes) is fine for a local LAN demo.

### 2. Derive endpoint IDs

Run this wherever you have the `peat-node` binary (you must build it from https://github.com/defenseunicorns/peat):

```bash
# Source endpoint ID — paste into destination/uds-config.yaml as peer_id
peat-node derive-id --shared-key <key> --node-id snapback-source

# Destination endpoint ID — paste into source/uds-config.yaml as peer_id
peat-node derive-id --shared-key <key> --node-id snapback-destination
```

With the default dev key the IDs are already filled in in the config files.

### 3. Edit the config files

**`source/uds-config.yaml`** (runs on the source host):

```yaml
variables:
  snapback:
    peer_id: "<destination endpoint ID>"
    peer_addr: "<destination host LAN IP>:51820"
```

**`destination/uds-config.yaml`** (runs on the destination host):

```yaml
variables:
  snapback:
    peer_id: "<source endpoint ID>"
    peer_addr: "<source host LAN IP>:51820"
```

### 4. Update the k3d dev cluster to publish the UDP port

Both hosts need the iroh UDP port published through the serverlb:

```bash
k3d cluster edit uds --port-add "51820:51820/udp@loadbalancer"
```

### 5. Deploy

On the **source** host:

```bash
cd demo/source
uds create . --confirm
uds deploy uds-bundle-snapback-source-*.tar.zst --config uds-config.yaml --confirm
```

On the **destination** host:

```bash
cd demo/destination
uds create . --confirm
uds deploy uds-bundle-snapback-destination-*.tar.zst --config uds-config.yaml --confirm
```

## Verifying the connection

Once both are deployed, check the peat-node logs on either side:

```bash
# Source — should show "connected to peer <destination endpoint ID>"
kubectl logs -n snapback snapback-0 -c peat-node | grep "connected to peer"

# Destination — should show attachments arriving
kubectl logs -n snapback snapback-0 -c peat-node | grep "attachment written to inbox"
```

If you see `failed to connect to peer: timed out`, see [Troubleshooting](#troubleshooting).

## Troubleshooting

**`timed out` on both sides**

Confirm the UDP port is actually reachable cross-host. From the destination host:

```bash
nc -u -z -v <source host IP> 51820
```

If this fails from the remote host but works locally, the source host's firewall is likely
blocking inbound UDP 51820. On macOS:

```bash
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate
# If enabled:
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --setglobalstate off
```

Also confirm the k3d serverlb is publishing on `0.0.0.0` (not `127.0.0.1`):

```bash
docker ps --format '{{.Names}}\t{{.Ports}}' | grep serverlb
# Should show: 0.0.0.0:51820->51820/udp
```

**`invalid peer certificate: UnknownIssuer`**

The `sharedKey` or `appId` differs between source and destination, or stale iroh identity
data is on disk. Ensure both bundles use the identical `peat.sharedKey` value, then restart
both StatefulSets:

```bash
kubectl rollout restart statefulset/snapback -n snapback
```
