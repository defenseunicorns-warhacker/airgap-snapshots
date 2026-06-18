# UDS Package Snapback

This package deploys Snapback on [UDS Core](https://github.com/defenseunicorns/uds-core).

> Snapback is an operator which replicates Velero backups between clusters using peat.

## Prerequisites

This package expects to be deployed on top of [UDS Core](https://github.com/defenseunicorns/uds-core). Document any additional dependencies (databases, operators, etc.) here.

## Flavors

This package ships an `upstream` flavor by default. Add `registry1` or `unicorn` flavors as needed — see [`zarf.yaml`](./zarf.yaml).

## Releases

Released packages are available in [GHCR](https://github.com/defenseunicorns-warhacker/airgap-snapshots/pkgs/container/snapback).

## Local development

Requires the [UDS CLI](https://github.com/defenseunicorns/uds-cli?tab=readme-ov-file#install).

```bash
uds run default     # spin up a local k3d cluster, build, and deploy
uds run dev         # iterate on an existing cluster
uds run --list      # show all available tasks
```

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md).
