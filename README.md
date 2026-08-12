# Cloud-Config → Butane Transpiler (Prototype)

A Go prototype that translates **cloud-config** YAML (used by [Cluster API](https://cluster-api.sigs.k8s.io/) for node provisioning) into **[Butane](https://coreos.github.io/butane/)** YAML (used by [Flatcar Container Linux](https://www.flatcar.org/) for first-boot provisioning via Ignition).

> **Status**: Working prototype — covers the CAPI-relevant subset of cloud-config fields. Designed to be extended.

## The Problem

Cluster API defaults to generating cloud-config because most Linux distributions use cloud-init. Flatcar doesn't — it uses Ignition, with Butane as its YAML front-end. When CAPI hands cloud-config to a Flatcar node, user-supplied customizations (`write_files`, `runcmd`, SSH keys) are **silently dropped**. The core cluster bootstrap works through CAPI's native Ignition support, but the user customization layer has no translator.

This project builds that translator.

```
Cluster API
  → cloud-config YAML          (CAPI's default output)
  → [THIS TRANSPILER]          (what this project builds)
  → Butane YAML                (Flatcar's configuration format)
  → butane compiler            (already exists — coreos/butane)
  → Ignition JSON              (read once at first boot)
```

## What's Implemented

The architecture is **parse → transform → generate**:

1. **Parse** — structs shaped like cloud-config's input format (verified against [`flatcar/coreos-cloudinit`](https://github.com/flatcar/coreos-cloudinit) config structs)
2. **Transform** — the only place real logic lives; converts input shape to output shape
3. **Generate** — structs shaped like Butane's output format (verified against [`coreos/butane`](https://github.com/coreos/butane) `base/v0_5/schema.go`)

### Field Mappings

| cloud-config | Butane | Transformation |
|---|---|---|
| `users[].name` | `passwd.users[].name` | Direct |
| `users[].ssh_authorized_keys` | `passwd.users[].ssh_authorized_keys` | Direct |
| `users[].groups` | `passwd.users[].groups` | Direct |
| `write_files[].path` | `storage.files[].path` | Direct |
| `write_files[].content` | `storage.files[].contents.inline` | Nested into `Resource.Inline`; Butane's compiler handles data-URL encoding |
| `write_files[].permissions` | `storage.files[].mode` | Octal string `"0644"` → integer `420` |
| `runcmd[]` | `systemd.units[]` | All commands → single `Type=oneshot` unit with multiple `ExecStart=` lines |

### Design Decision: `runcmd` → systemd unit

`runcmd` has no direct Butane equivalent. The chosen approach: a single `Type=oneshot` systemd unit with multiple `ExecStart=` lines, which systemd executes sequentially, stopping on first failure. This is verified correct per `systemd.service(5)` and matches the unit generation pattern used by Butane's own `mountUnitFromFS`.

## Running

```bash
go run transpiler/main.go
```

### Example Output

Given this cloud-config input:
```json
{
  "users": [
    {"name": "core", "ssh_authorized_keys": ["ssh-rsa AAAA..."], "groups": ["sudo", "docker"]}
  ],
  "write_files": [
    {"path": "/etc/motd", "content": "Welcome", "permissions": "0644"}
  ],
  "runcmd": ["echo first command", "echo second command"]
}
```

The transpiler produces Butane-shaped output targeting `variant: flatcar`, `version: 1.1.0`:

```json
{
  "variant": "flatcar",
  "version": "1.1.0",
  "passwd": {
    "users": [{ "name": "core", "ssh_authorized_keys": ["ssh-rsa AAAA..."], "groups": ["sudo", "docker"] }]
  },
  "storage": {
    "files": [{ "path": "/etc/motd", "contents": { "inline": "Welcome" }, "mode": 420 }]
  },
  "systemd": {
    "units": [{
      "name": "runcmd.service",
      "enabled": true,
      "contents": "[Unit]\nDescription=Translated runcmd commands\n\n[Service]\nType=oneshot\nRemainAfterExit=true\nExecStart=/bin/sh -c \"echo first command\"\nExecStart=/bin/sh -c \"echo second command\"\n\n[Install]\nWantedBy=multi-user.target\n"
    }]
  }
}
```

## Schema Verification

Output structs are verified against the real Butane source, not just documentation:

- **Flatcar variant**: `config/flatcar/v1_1/schema.go` embeds `base/v0_5` — registered as `variant: "flatcar"`, `version: "1.1.0"` in `config/config.go`
- **PasswdUser**: matches `base/v0_5/schema.go` lines 169–185 (pointer fields for optional data, type aliases for `Group`/`SSHAuthorizedKey`)
- **File + Resource**: matches `base/v0_5/schema.go` lines 62–70, 201–208 (`Resource.Inline` is a Butane extension; `translate.go` converts it to data URLs via `MakeDataURL`)
- **Unit**: matches `base/v0_5/schema.go` lines 251–258 (`Name string`, `Enabled *bool`, `Contents *string`)

## What's Intentionally Not Implemented

This is a **prototype scoped to what CAPI actually needs** for worker node provisioning:

- **`packages:`** — Flatcar has no host package manager. Nothing to transpile to.
- **`bootcmd:`** — Would require Ignition-stage execution, which Ignition deliberately doesn't support.
- **Full cloud-config spec** — Only the subset CAPI generates is in scope.

## Extending

The architecture makes extension mechanical — for each new cloud-config field:

1. Add a parse struct (input shape)
2. Add a generate struct (output shape)
3. Add a case in `transform()` to map between them

The `json` struct tags are a stand-in for `gopkg.in/yaml.v3` during prototyping — swapping is a mechanical change (identical tag names, snake_case throughout).

## References

- [`coreos/butane`](https://github.com/coreos/butane) — Butane YAML → Ignition JSON compiler (output schema source of truth)
- [`flatcar/coreos-cloudinit`](https://github.com/flatcar/coreos-cloudinit) — Legacy cloud-config parser for Flatcar (input schema reference)
- [`flatcar/cluster-api-bootstrap-provider-kubeadm-ignition`](https://github.com/flatcar/cluster-api-bootstrap-provider-kubeadm-ignition) — The CAPI bootstrap provider for Flatcar
- [Butane Config Transpiler docs](https://www.flatcar.org/docs/latest/provisioning/butane/transpiler/) — Flatcar's Butane documentation
