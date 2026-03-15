# vSphere Datastore Cluster (StoragePod) Support

This document describes how to use vSphere Datastore Clusters (also known as StoragePods) for automatic datastore selection when provisioning virtual machines with virtrigaud.

**Note: StoragePod support is specific to the vSphere provider and is not available for Libvirt or Proxmox.**

## Overview

A vSphere **Datastore Cluster** (internally called a `StoragePod`) is a logical grouping of datastores managed together as a single unit. When you specify a Datastore Cluster instead of an individual datastore, virtrigaud automatically selects the datastore within the cluster that has the **most available free space** at provisioning time.

This simplifies VM placement in environments with multiple datastores: instead of tracking which individual datastore has capacity, you point to the cluster and let virtrigaud choose.

## Datastore Selection Strategy

virtrigaud uses a simple, predictable strategy: **pick the datastore with the most free space**. This distributes VMs across the cluster over time as datastores fill up.

> vSphere Storage DRS is not required to be enabled on the cluster. virtrigaud queries datastore summaries directly via the vSphere API.

## Configuration

### Per-VM Placement (VirtualMachine spec)

Specify `storagePod` inside `spec.placement` on a `VirtualMachine` resource:

```yaml
apiVersion: infra.virtrigaud.io/v1beta1
kind: VirtualMachine
metadata:
  name: my-vm
  namespace: virtrigaud-system
spec:
  providerRef:
    name: vsphere-prod
  classRef:
    name: standard-2cpu-4gb
  imageRef:
    name: ubuntu-24-04
  placement:
    cluster: prod-cluster
    storagePod: "Production-DS-Cluster"   # Datastore Cluster name
    folder: /prod/vms
```

virtrigaud will inspect every datastore in `Production-DS-Cluster` and clone the VM onto the one with the most free space.

### Provider-Level Default

Set `spec.defaults.storagePod` on the `Provider` resource to apply a Datastore Cluster as the default for all VMs that do not specify their own placement:

```yaml
apiVersion: infra.virtrigaud.io/v1beta1
kind: Provider
metadata:
  name: vsphere-prod
  namespace: virtrigaud-system
spec:
  type: vsphere
  endpoint: https://vcenter.example.com
  credentialSecretRef:
    name: vsphere-credentials
  defaults:
    cluster: prod-cluster
    storagePod: "Production-DS-Cluster"   # cluster-wide default
    folder: /prod/vms
  runtime:
    image: ghcr.io/projectbeskar/virtrigaud-provider-vsphere:latest
```

Alternatively, pass the default through the provider pod's environment by adding it to `spec.runtime.env`:

```yaml
spec:
  runtime:
    env:
      - name: PROVIDER_DEFAULT_STORAGE_POD
        value: "Production-DS-Cluster"
```

## Precedence Rules

When multiple sources specify storage placement, virtrigaud applies the following priority (highest to lowest):

| Priority | Source | Field |
|----------|--------|-------|
| 1 | VM spec — explicit datastore | `spec.placement.datastore` |
| 2 | VM spec — StoragePod | `spec.placement.storagePod` |
| 3 | Provider default — StoragePod | `spec.defaults.storagePod` / `PROVIDER_DEFAULT_STORAGE_POD` |
| 4 | Provider default — datastore | `spec.defaults.datastore` / `PROVIDER_DEFAULT_DATASTORE` |

An explicit `datastore` always wins. `storagePod` is only consulted when no explicit `datastore` is set.

## Examples

### StoragePod only (recommended for large environments)

```yaml
apiVersion: infra.virtrigaud.io/v1beta1
kind: VirtualMachine
metadata:
  name: web-server-01
  namespace: virtrigaud-system
spec:
  providerRef:
    name: vsphere-prod
  classRef:
    name: web-4cpu-8gb
  imageRef:
    name: ubuntu-24-04
  placement:
    cluster: prod-cluster
    storagePod: "SSD-Datastore-Cluster"
```

### Override with an explicit datastore (e.g. for compliance)

When you need a specific datastore—for example, a datastore dedicated to regulated workloads—set `datastore` and `storagePod` is ignored:

```yaml
  placement:
    cluster: prod-cluster
    datastore: "regulated-ds-01"    # StoragePod is ignored when this is set
    storagePod: "SSD-Datastore-Cluster"
```

### Use different clusters for different teams via a shared Provider

Combine a provider-level StoragePod default with per-VM overrides:

```yaml
# Provider default: route most VMs to the general-purpose cluster
spec:
  defaults:
    cluster: general-cluster
    storagePod: "General-DS-Cluster"
```

```yaml
# High-performance VM overrides both cluster and StoragePod
spec:
  placement:
    cluster: nvme-cluster
    storagePod: "NVMe-DS-Cluster"
```

## How it Works Internally

When a VM is created and a StoragePod is resolved:

1. virtrigaud creates a container view scoped to the vSphere root folder and searches for `StoragePod` managed objects.
2. It matches the named StoragePod and reads its `childEntity` list—the set of datastores it contains.
3. For each child datastore the `summary.freeSpace` property is retrieved via the property collector.
4. The datastore with the highest `freeSpace` is selected and used as the target in the clone specification (`VirtualMachineRelocateSpec.Datastore`).

The selection happens at provisioning time; it is not re-evaluated on subsequent reconciliations or reboots.

## Troubleshooting

### "StoragePod 'X' not found"

- Verify the name exactly matches the Datastore Cluster name in vCenter (case-sensitive).
- Confirm the vCenter user account has `Datastore.Browse` privilege on the Datastore Cluster.
- Check provider pod logs for the container view query.

### "StoragePod 'X' contains no datastores"

The Datastore Cluster exists but is empty (no datastores are members). Add datastores to the cluster in vCenter.

### "failed to retrieve datastores from StoragePod"

The provider account lacks permission to read datastore summary properties. Grant `Datastore.Browse` on the individual datastores within the cluster.

### VM is always placed on the same datastore

This is expected when one datastore consistently has significantly more free space. It is not a bug.

### Checking which datastore was selected

The provider logs an `INFO` message at VM creation time:

```
INFO  Selected datastore from StoragePod  storagePod=Production-DS-Cluster  datastore=vsanDatastore-02  freeSpaceGiB=812
```

Check the provider pod logs to see the selection for any specific VM.

## Limitations

- **Free-space only**: virtrigaud does not use vSphere Storage DRS policies, IOPS limits, or storage tags when selecting a datastore. Only free space is considered.
- **Point-in-time selection**: The datastore is chosen once at clone time. Subsequent Storage vMotion by Storage DRS is not prevented.
- **No rebalancing**: virtrigaud does not rebalance existing VMs when free space changes.
- **vSphere only**: This feature has no equivalent for Libvirt or Proxmox providers.
