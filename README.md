# Cloud-Config → Butane Transpiler (Prototype)

![CI](https://github.com/shipitdev/Flatcar-transpiler/actions/workflows/validate.yml/badge.svg)

**TL;DR:** Cluster API defaults to generating cloud-config for node
provisioning, but a real, current Cluster API provider's own source
documents that user customizations (`write_files`, `runcmd`, `ntp`) are
silently dropped when provisioning Flatcar nodes via Ignition — and a
real 2023 bug report shows this happening on an actual cluster. This is
a working Go prototype that translates cloud-config into Butane YAML to
close that gap, verified end-to-end against the real `butane` CLI.

> **Status**: Working prototype — covers the CAPI-relevant subset of
> cloud-config fields (`users`, `write_files`, `runcmd`). Designed to be
> extended.

## The Problem

Cluster API defaults to generating cloud-config because most Linux
distributions use cloud-init. Flatcar doesn't — it uses Ignition, with
Butane as its YAML front-end. When CAPI hands cloud-config to a Flatcar
node, user-supplied customizations (`write_files`, `runcmd`, SSH keys)
are **silently dropped**. The core cluster bootstrap works through CAPI's
native Ignition support, but the user customization layer has no
translator.

This project builds that translator.

```mermaid
flowchart LR
    A[Cluster API] -->|generates| B[cloud-config YAML]
    B --> C{{This Transpiler}}
    C -->|generates| D[Butane YAML]
    D -->|compiled by\nexisting butane tool| E[Ignition JSON]
    E -->|read once, at first boot| F[Flatcar Node]

    style C fill:#2b6cb0,stroke:#1a365d,color:#fff
```

Only the highlighted node — **This Transpiler** — is what this project
builds. Everything else (Cluster API, the `butane` compiler, Ignition,
Flatcar's boot process) already exists.

## Evidence This Gap Is Real

- **[KubeVirt provider bug report (2023)](https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt/issues/241)** —
  a real Flatcar node's boot log rejecting `runcmd`, `sudo`, and a
  `groups` type mismatch outright, on a real cluster.
- **[Official Flatcar CAPI bootstrap provider](https://github.com/flatcar/cluster-api-bootstrap-provider-kubeadm-ignition)** —
  a real, currently active effort under the CNCF Node Bootstrapping
  working group. This confirms the problem is live right now, not
  historical.

## How This Maps to What the Project Is Looking For

| Listed skill | Where it shows up here |
|---|---|
| **Go** | `transform()` in `transpiler/main.go` — the entire translation logic |
| **YAML** | Parses cloud-config YAML, generates Butane YAML (JSON used as a stand-in for `yaml.v3` during prototyping — mechanical swap, see Extending section) |
| **Cluster API** | See "Evidence" above — real, cited gap in current CAPI provider behavior |
| **Linux Based OS** | See "Design Decisions" below — reasoning about Ignition's boot-time constraints and systemd unit semantics |

## What's Implemented

The architecture is **parse → transform → generate**:

1. **Parse** — structs shaped like cloud-config's input format (verified
   against [`flatcar/coreos-cloudinit`](https://github.com/flatcar/coreos-cloudinit)
   config structs)
2. **Transform** — the only place real logic lives; converts input shape
   to output shape
3. **Generate** — structs shaped like Butane's output format (verified
   against [`coreos/butane`](https://github.com/coreos/butane)
   `base/v0_5/schema.go`)

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

## Design Decisions

### `runcmd` → systemd unit

`runcmd` has no direct Butane equivalent — Ignition is deliberately
declarative-only and can't execute arbitrary commands. Two approaches
were considered:

- **Rejected: separate systemd units chained via `After=`/`Requires=`**
  for each command. Gives per-command visibility, but adds generated
  YAML and more moving parts for no correctness benefit once the
  simpler option was verified.
- **Chosen: one `Type=oneshot` unit with multiple `ExecStart=` lines.**
  Verified against the real `systemd.service(5)` man page: `oneshot`
  units run multiple `ExecStart=` lines sequentially and stop
  automatically on first failure — this preserves cloud-config's
  ordering guarantee with less generated output. Matches the unit
  generation pattern used by Butane's own `mountUnitFromFS`.

### `variant: flatcar`, not `variant: fcos`

Early prototyping used the generic `fcos` (Fedora CoreOS) Butane variant
while learning the general shape of Butane. Reading Butane's actual
variant registration code (`RegisterTranslator("flatcar", "1.1.0", ...)`
in `config/config.go`) surfaced that Flatcar has its own dedicated
variant. This was independently re-verified by compiling output with
`variant: flatcar, version: 1.1.0` against the real `butane` binary
(`--strict` mode) before adopting it — not just trusted on inspection.

## Running

```bash
go run transpiler/main.go
```

### Testing

```bash
go test ./... -v
```

Table-driven unit tests cover all three fields (`users`, `write_files`
including the octal-mode conversion, and `runcmd` ordering), plus the
`variant`/`version` header and empty-input behavior.

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

This exact output has been validated end-to-end: converted to YAML and
compiled cleanly through the real `butane` CLI with `--strict` mode — see
`.github/workflows/validate.yml`, which runs this same validation on
every push.

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
