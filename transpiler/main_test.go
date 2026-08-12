package main

import (
	"strings"
	"testing"
)

func TestTransform_Variant(t *testing.T) {
	out := transform(CloudConfig{})
	if out.Variant != "flatcar" {
		t.Errorf("Variant = %q, want %q", out.Variant, "flatcar")
	}
	if out.Version != "1.1.0" {
		t.Errorf("Version = %q, want %q", out.Version, "1.1.0")
	}
}

func TestTransform_Users(t *testing.T) {
	input := CloudConfig{
		Users: []CloudConfigUser{
			{
				Name:              "core",
				SSHAuthorizedKeys: []string{"ssh-rsa AAAA..."},
				Groups:            []string{"sudo", "docker"},
			},
		},
	}
	out := transform(input)

	if out.Passwd == nil {
		t.Fatal("Passwd is nil, want a populated Passwd section")
	}
	if len(out.Passwd.Users) != 1 {
		t.Fatalf("got %d users, want 1", len(out.Passwd.Users))
	}
	got := out.Passwd.Users[0]
	if got.Name != "core" {
		t.Errorf("Name = %q, want %q", got.Name, "core")
	}
	if len(got.SSHAuthorizedKeys) != 1 || got.SSHAuthorizedKeys[0] != "ssh-rsa AAAA..." {
		t.Errorf("SSHAuthorizedKeys = %v, want [ssh-rsa AAAA...]", got.SSHAuthorizedKeys)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "sudo" || got.Groups[1] != "docker" {
		t.Errorf("Groups = %v, want [sudo docker]", got.Groups)
	}
}

func TestTransform_WriteFiles_ContentAndOctalMode(t *testing.T) {
	content := "Welcome"
	permissions := "0644"
	input := CloudConfig{
		WriteFiles: []CloudConfigFile{
			{Path: "/etc/motd", Content: &content, Permissions: &permissions},
		},
	}
	out := transform(input)

	if out.Storage == nil {
		t.Fatal("Storage is nil, want a populated Storage section")
	}
	if len(out.Storage.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(out.Storage.Files))
	}
	got := out.Storage.Files[0]
	if got.Path != "/etc/motd" {
		t.Errorf("Path = %q, want %q", got.Path, "/etc/motd")
	}
	if got.Contents.Inline == nil || *got.Contents.Inline != "Welcome" {
		t.Errorf("Contents.Inline = %v, want \"Welcome\"", got.Contents.Inline)
	}
	// "0644" octal must become 420 decimal — this is the specific
	// conversion that's easy to get subtly wrong.
	if got.Mode == nil || *got.Mode != 420 {
		t.Errorf("Mode = %v, want 420 (decimal for octal 0644)", got.Mode)
	}
}

func TestTransform_WriteFiles_MalformedPermissionsSkipped(t *testing.T) {
	content := "test"
	badPermissions := "not-octal"
	input := CloudConfig{
		WriteFiles: []CloudConfigFile{
			{Path: "/etc/test", Content: &content, Permissions: &badPermissions},
		},
	}
	out := transform(input)
	got := out.Storage.Files[0]
	if got.Mode != nil {
		t.Errorf("Mode = %v, want nil for malformed permissions input", *got.Mode)
	}
}

func TestTransform_Runcmd_SingleOneshotUnit(t *testing.T) {
	input := CloudConfig{
		Runcmd: []string{"echo first command", "echo second command"},
	}
	out := transform(input)

	if out.Systemd == nil {
		t.Fatal("Systemd is nil, want a populated Systemd section")
	}
	if len(out.Systemd.Units) != 1 {
		t.Fatalf("got %d units, want exactly 1 (Option A: single unit, multiple ExecStart=)", len(out.Systemd.Units))
	}
	unit := out.Systemd.Units[0]
	if unit.Name != "runcmd.service" {
		t.Errorf("Name = %q, want %q", unit.Name, "runcmd.service")
	}
	if unit.Enabled == nil || !*unit.Enabled {
		t.Error("Enabled should be true")
	}
	if unit.Contents == nil {
		t.Fatal("Contents is nil, want the generated unit file text")
	}
	contents := *unit.Contents

	if !strings.Contains(contents, "Type=oneshot") {
		t.Error("unit contents missing Type=oneshot")
	}
	if !strings.Contains(contents, "RemainAfterExit=true") {
		t.Error("unit contents missing RemainAfterExit=true")
	}

	// Ordering matters: cloud-config's runcmd runs in order, so the
	// first command's ExecStart= line must appear before the second's.
	firstIdx := strings.Index(contents, `ExecStart=/bin/sh -c "echo first command"`)
	secondIdx := strings.Index(contents, `ExecStart=/bin/sh -c "echo second command"`)
	if firstIdx == -1 || secondIdx == -1 {
		t.Fatalf("expected both ExecStart= lines present, got:\n%s", contents)
	}
	if firstIdx > secondIdx {
		t.Error("ExecStart= lines are out of order — runcmd must preserve sequence")
	}
}

func TestTransform_EmptyInput_ProducesNoOptionalSections(t *testing.T) {
	out := transform(CloudConfig{})
	if out.Passwd != nil {
		t.Error("Passwd should be nil for empty input")
	}
	if out.Storage != nil {
		t.Error("Storage should be nil for empty input")
	}
	if out.Systemd != nil {
		t.Error("Systemd should be nil for empty input")
	}
}
