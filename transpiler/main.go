package main

import (
	"encoding/json" // stand-in for gopkg.in/yaml.v3 during prototyping;
	// swap to yaml.v3 with identical struct tags in the real project
	"fmt"
	"strconv"
	"strings"
)

// =====================================================================
// PARSE: cloud-config INPUT shape
// =====================================================================
//
// Modelled after flatcar/coreos-cloudinit config/ package:
//   - User struct:  coreos-cloudinit/config/user.go
//   - File struct:  coreos-cloudinit/config/file.go
//   - CloudConfig: coreos-cloudinit/config/config.go
//
// We only model the CAPI-relevant subset (users, write_files, runcmd).
// coreos-cloudinit doesn't have runcmd (it's a standard cloud-init
// feature), so we add it ourselves for CAPI compatibility.

type CloudConfigUser struct {
	Name              string   `json:"name"`
	SSHAuthorizedKeys []string `json:"ssh_authorized_keys"`
	Groups            []string `json:"groups"`
}

type CloudConfigFile struct {
	Path        string  `json:"path"`
	Content     *string `json:"content"`     // optional — an empty file is valid
	Permissions *string `json:"permissions"` // optional; string like "0644", validated by coreos-cloudinit as ^0?[0-7]{3,4}$
	// Encoding and Owner exist in coreos-cloudinit but aren't needed for
	// our CAPI-scoped subset.
}

type CloudConfig struct {
	Users      []CloudConfigUser `json:"users"`
	WriteFiles []CloudConfigFile `json:"write_files"`
	Runcmd     []string          `json:"runcmd"` // not in coreos-cloudinit; standard cloud-init field used by CAPI
}

// =====================================================================
// GENERATE: Butane OUTPUT shape
// =====================================================================
//
// Modelled after coreos/butane base/v0_5/schema.go, which is the base
// schema embedded by the Flatcar v1.1 variant:
//   config/flatcar/v1_1/schema.go → base "github.com/coreos/butane/base/v0_5"
//   Registered as: variant "flatcar", version "1.1.0" (config/config.go:81)
//
// Key conventions from the real schema:
//   - Pointer fields (*string, *bool, *int) for optional data, matching
//     the real PasswdUser, File, Unit structs.
//   - type Group string, type SSHAuthorizedKey string are type aliases
//     in the real code — we use plain []string which serializes identically.
//   - Contents in File is a Resource struct (value, not pointer) with an
//     Inline *string field. We model just the subset we need.
//   - Unit.Contents is *string (raw unit-file text, not a nested struct).

// --- passwd ---
// Real: base/v0_5/schema.go PasswdUser (lines 169-185)

type ButaneUser struct {
	Name              string   `json:"name"`
	SSHAuthorizedKeys []string `json:"ssh_authorized_keys,omitempty"` // real type: []SSHAuthorizedKey
	Groups            []string `json:"groups,omitempty"`              // real type: []Group
}

type Passwd struct {
	Users []ButaneUser `json:"users"`
}

// --- storage ---
// Real: base/v0_5/schema.go File (lines 62-70), Resource (lines 201-208)
//
// The Resource struct in Butane has an Inline *string field (line 205,
// comment: "Added, not in ignition spec"). During Butane→Ignition
// translation (translate.go:173-187), this inline content gets converted
// to a data URL via baseutil.MakeDataURL. We just pass the plain string
// through and let Butane's compiler handle the encoding.

type ButaneFileContents struct {
	Inline *string `json:"inline,omitempty"`
}

type ButaneFile struct {
	Path     string             `json:"path"`
	Contents ButaneFileContents `json:"contents,omitempty"` // value type, not pointer — matches real File.Contents Resource
	Mode     *int               `json:"mode,omitempty"`     // real: File.Mode *int (line 69)
}

type Storage struct {
	Files []ButaneFile `json:"files"`
}

// --- systemd ---
// Real: base/v0_5/schema.go Unit (lines 251-258)

type SystemdUnit struct {
	Name     string  `json:"name"`
	Enabled  *bool   `json:"enabled,omitempty"`  // real: *bool (line 255)
	Contents *string `json:"contents,omitempty"` // real: *string (line 252)
}

type Systemd struct {
	Units []SystemdUnit `json:"units"`
}

// --- top-level ---
// Real: base/v0_5/schema.go Config (lines 30-38)
// Note: real schema uses value types (Passwd, Storage, Systemd) not
// pointers — yaml.v3 handles zero-value omission. We use pointers +
// omitempty here because json.Marshal doesn't omit empty structs by
// default. When migrating to yaml.v3, switch to value types to match.

type ButaneConfig struct {
	Variant string   `json:"variant"`
	Version string   `json:"version"`
	Passwd  *Passwd  `json:"passwd,omitempty"`
	Storage *Storage `json:"storage,omitempty"`
	Systemd *Systemd `json:"systemd,omitempty"`
}

// =====================================================================
// TRANSFORM: the only place real logic lives
// =====================================================================
//
// Architecture: parse → transform → generate
// This function is the bridge between cloud-config's input shape and
// Butane's output shape. Each section is independent.

func transform(input CloudConfig) ButaneConfig {
	output := ButaneConfig{
		// Flatcar variant, version 1.1.0 — matches the real registration:
		// config/config.go:81  RegisterTranslator("flatcar", "1.1.0", flatcar1_1.ToIgn3_4Bytes)
		Variant: "flatcar",
		Version: "1.1.0",
	}

	// --- users → passwd.users ---
	// Field mapping is 1:1 for our subset. The real Butane PasswdUser has
	// many more optional fields (gecos, home_dir, shell, etc.) that CAPI
	// doesn't use for worker node provisioning.
	if len(input.Users) > 0 {
		var butaneUsers []ButaneUser
		for _, u := range input.Users {
			butaneUsers = append(butaneUsers, ButaneUser{
				Name:              u.Name,
				SSHAuthorizedKeys: u.SSHAuthorizedKeys,
				Groups:            u.Groups,
			})
		}
		output.Passwd = &Passwd{Users: butaneUsers}
	}

	// --- write_files → storage.files ---
	// Field mapping:
	//   cloud-config path        → Butane path           (same)
	//   cloud-config content     → Butane contents.inline (nested into Resource.Inline)
	//   cloud-config permissions → Butane mode            (string→int conversion)
	//
	// Butane's compiler (translate.go:173-187) converts Resource.Inline
	// to a data URL automatically — we just pass the plain string through.
	if len(input.WriteFiles) > 0 {
		var butaneFiles []ButaneFile
		for _, f := range input.WriteFiles {
			bf := ButaneFile{Path: f.Path}

			if f.Content != nil {
				bf.Contents = ButaneFileContents{Inline: f.Content}
			}

			// Convert permissions string (octal like "0644") to integer.
			// ParseInt with base 8 interprets the string as octal:
			// "0644" → 420 decimal. Butane's compiler then handles the
			// representation in the final Ignition JSON.
			// The coreos-cloudinit File.RawFilePermissions field validates
			// with: valid:"^0?[0-7]{3,4}$"  (file.go:22)
			if f.Permissions != nil {
				mode, err := strconv.ParseInt(*f.Permissions, 8, 32)
				if err == nil {
					modeInt := int(mode)
					bf.Mode = &modeInt
				}
				// Silently skip malformed permissions — a real implementation
				// would surface a validation error.
			}

			butaneFiles = append(butaneFiles, bf)
		}
		output.Storage = &Storage{Files: butaneFiles}
	}

	// --- runcmd → systemd.units (single oneshot unit) ---
	//
	// Design decision (Option A): all runcmd entries become multiple
	// ExecStart= lines in ONE Type=oneshot unit. This is correct systemd
	// behavior — the systemd.service(5) man page states that oneshot
	// units support multiple ExecStart= lines, executed sequentially,
	// stopping on first failure.
	//
	// The generated unit matches the pattern used by Butane's own
	// mountUnitFromFS (translate.go:480-510): construct a unit file
	// string with strings.Builder, wrap in a types.Unit with Name,
	// Enabled (via util.BoolToPtr), and Contents (via util.StrToPtr).
	if len(input.Runcmd) > 0 {
		var sb strings.Builder
		sb.WriteString("[Unit]\n")
		sb.WriteString("Description=Translated runcmd commands\n")
		sb.WriteString("\n")
		sb.WriteString("[Service]\n")
		sb.WriteString("Type=oneshot\n")
		sb.WriteString("RemainAfterExit=true\n")
		for _, cmd := range input.Runcmd {
			// Wrap each command in /bin/sh -c "..." so shell features
			// (pipes, redirects, variable expansion) work correctly.
			sb.WriteString(fmt.Sprintf("ExecStart=/bin/sh -c \"%s\"\n", cmd))
		}
		sb.WriteString("\n")
		sb.WriteString("[Install]\n")
		sb.WriteString("WantedBy=multi-user.target\n")

		enabled := true
		contents := sb.String()
		output.Systemd = &Systemd{
			Units: []SystemdUnit{
				{
					Name:     "runcmd.service",
					Enabled:  &enabled,
					Contents: &contents,
				},
			},
		}
	}

	return output
}

// =====================================================================
// MAIN: run the full parse → transform → generate pipeline
// =====================================================================

func main() {
	// Test input matching the task specification.
	// This simulates the cloud-config that CAPI generates for a
	// Flatcar worker node.
	inputJSON := `{
  "users": [
    {"name": "core", "ssh_authorized_keys": ["ssh-rsa AAAA..."], "groups": ["sudo", "docker"]}
  ],
  "write_files": [
    {"path": "/etc/motd", "content": "Welcome", "permissions": "0644"}
  ],
  "runcmd": [
    "echo first command",
    "echo second command"
  ]
}`

	// --- PARSE ---
	var input CloudConfig
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		fmt.Printf("parse error: %v\n", err)
		return
	}

	// --- TRANSFORM ---
	output := transform(input)

	// --- GENERATE ---
	// Using json.MarshalIndent as a stand-in for yaml.v3 Marshal during
	// prototyping. The struct tags and nesting are identical — swapping
	// to yaml.v3 (changing `json:` to `yaml:` tags) is a mechanical
	// change. Field names already use the correct snake_case from both
	// the coreos-cloudinit and Butane schemas.
	result, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fmt.Printf("generate error: %v\n", err)
		return
	}

	fmt.Println(string(result))
}
