# Configuration

A router is a directory in git:

```
my-router/
├── talos.yaml      Talos documents: links, aliases, addresses, VLANs, WireGuard, ingress rules
├── router.yaml     Routal documents, read by routald
└── secrets/        key files referenced by talos.yaml (git-ignored, or decrypted by CI)
```

Every field is in [`examples/reference/router.yaml`](../examples/reference/router.yaml). All
examples are checked by `make check` (routald, the real daemons, and `talosctl validate`).

## Router documents (`apiVersion: v1alpha1`)

Every document, every field and a template to copy: the
[reference on the web page](https://j0n17.github.io/routal/#documents), generated from the schema.
Unknown fields are errors. routald programs its own `inet routal` nftables table; Talos keeps
`inet talos`.

## Naming links

Links are kernel names or Talos `LinkAliasConfig` aliases (a letter first, at most 15
characters). An alias is chosen in talos.yaml by an expression on the link's properties, so that
router.yaml never depends on the order in which the kernel finds the NICs:

- `mac(link.permanent_addr) == "52:54:00:00:00:01"`
- `link.bus_path == "0000:00:03.0"`
- `link.driver == "igc"`

The VM test selects its WAN by MAC address and its LAN by bus path.

Leave IP forwarding to routald (`ForwardingConfig`): do not set it in `machine.sysctls`.

## Secrets

Talos wants WireGuard keys inline. In `talos.yaml`, reference a file instead (path relative to
`talos.yaml`), like a key path on an EdgeRouter:

```yaml
kind: WireguardConfig
name: wg0
privateKeyFile: secrets/wg0.key        # wg genkey > secrets/wg0.key
peers:
  - publicKey: ...
    presharedKeyFile: secrets/phone.psk  # wg genpsk > secrets/phone.psk
```

- `routald talos-render talos.yaml` prints the documents with the keys inlined: apply its output,
  never commit it.
- `secrets/` is in `.gitignore` (the examples hold placeholders).
- router.yaml does the same for its own secrets (`passwordFile` of PPPoE and dynamic DNS, relative
  to router.yaml): `routald talos-patch` ships each file next to router.yaml on the router.

## Apply

```sh
routald validate router.yaml
routald render router.yaml                       # nftables ruleset routald will apply
routald render-services router.yaml out/         # daemon configs (Kea, Unbound, radvd, chrony...)

# machine (links, addresses, WireGuard...)
routald talos-render talos.yaml > /tmp/talos.yaml
talosctl patch mc --patch @/tmp/talos.yaml --mode try --timeout 2m

# router: Talos reverts the config unless the router reports healthy with it
bin/routal-apply router.yaml
```

`bin/routal-apply`:

1. validates the file;
2. turns it into a Talos patch (`routald talos-patch -current`);
3. applies it with `--mode try`;
4. confirms it only when the router is healthy with it: routald's own report says so and every
   `--check 'command'` passes.

A config that locks you out is never confirmed: Talos takes it back after the timeout.

### What `talos-patch` does

- wraps router.yaml in an `ExtensionServiceConfig` (Talos restarts routald with the new file);
- turns `to: self` rules, port forwards and UPnP into Talos `NetworkRuleConfig` documents (Talos
  filters traffic to the router's addresses before routald's DNAT runs);
- with `-current`, also deletes the `routal-*` rules the config no longer needs (patches otherwise
  only add).

## Health

routald reports its health after every reconcile (every 5 s): whether the config is applied, and
for each daemon whether it runs and stays up. Changes are logged (`health: NOT OK: ...`), and the
report can be read at any time:

```sh
talosctl read /run/routal/routal/status.json | routald status -   # exit status 1 when not healthy
```

## Editor support

`router.yaml` has a JSON schema, generated from routald's types. With the
[YAML language server](https://github.com/redhat-developer/yaml-language-server) (VS Code's YAML
extension, Neovim, Helix, JetBrains...) the editor completes kinds and fields, shows their
documentation and underlines mistakes while you type. Put this on the first line of router.yaml:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/j0n17/routal/main/pkg/schema/router.schema.json
```

or keep the schema of your routald version next to the file (`routald schema > router.schema.json`,
then `$schema=./router.schema.json`).

The schema sees one document at a time: types, unknown fields, allowed values, link names,
addresses, ports. What needs the whole file (a rule naming a zone that does not exist, a DHCP range
outside its subnet, a missing secret) is `routald validate`'s.
