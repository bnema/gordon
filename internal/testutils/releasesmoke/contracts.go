package releasesmoke

import "github.com/bnema/gordon/internal/domain"

// ManagedPassLockMessage is printed when secrets lock is acquired.
const ManagedPassLockMessage = "Managed pass backend lock acquired"

// LeaseConflictMessage is printed when a second doctor run hits an active lease.
const LeaseConflictMessage = "managed pass store is already in use"

// ReadinessPollAttempts bounds managed-pass lock acquisition waits.
const ReadinessPollAttempts = 30

// ImageArchitectures verified by release-image-smoke.
var ImageArchitectures = []string{"amd64", "arm64"}

// RoleIdentities exercised by podman managed-pass smoke.
var RoleIdentities = []struct {
	Role     domain.ComponentRole
	Identity string
}{
	{domain.ComponentRoleRuntime, "21001"},
	{domain.ComponentRoleControl, "21002"},
	{domain.ComponentRoleEdge, "21003"},
	{domain.ComponentRoleRegistry, "21004"},
}

// HarnessContract documents release smoke invariants previously encoded inline in the Makefile.
type HarnessContract struct {
	Name        string
	Engine      string
	Contains    []string
	NotContains []string
}

// HarnessContracts is the table of lease, identity, cleanup, and inspection contracts.
var HarnessContracts = []HarnessContract{
	{
		Name:   "docker-managed-pass-lease",
		Engine: "docker",
		Contains: []string{
			`"secrets", "lock"`,
			"ManagedPassLockMessage",
			"LeaseConflictMessage",
			"waitManagedPassReadiness",
			"StdoutPipe",
			"ReadinessPollAttempts",
			"managedPassOwnerCleanup",
			"Process.Kill",
		},
		NotContains: []string{
			"mkfifo",
			"O_WRONLY",
		},
	},
	{
		Name:   "docker-image-permissions",
		Engine: "docker",
		Contains: []string{
			"21002:21002:700",
			".gordon-managed-pass-fingerprint",
			"password-store/.gpg-id",
		},
	},
	{
		Name:   "podman-role-identity",
		Engine: "podman",
		Contains: []string{
			"keep-id:uid=",
			`["ALL"]`,
			"no-new-privileges",
			"identity-write-check",
			"/var/lib/gordon:U",
			"assertExclusiveGenerationVolumeU",
			"assertResourcesUninspectable",
		},
		NotContains: []string{
			"secrets:U",
			"--group-add",
			"21900",
		},
	},
	{
		Name:   "podman-runtime-socket",
		Engine: "podman",
		Contains: []string{
			"/run/gordon/runtime.sock:ro",
			`"system", "service"`,
		},
	},
	{
		Name:   "podman-managed-pass-lease",
		Engine: "podman",
		Contains: []string{
			"ManagedPassLockMessage",
			"LeaseConflictMessage",
			`"secrets", "doctor"`,
			`"--write-check"`,
			"StdoutPipe",
			"managedPassOwnerCleanup",
		},
		NotContains: []string{
			"mkfifo",
		},
	},
	{
		Name:   "cleanup-reap",
		Engine: "podman",
		Contains: []string{
			`"rm", "-f"`,
			`"volume", "rm"`,
			"Process.Kill",
			"Process.Wait",
			"cleanupOnce",
			`"volume", "inspect"`,
			`"container", "inspect"`,
		},
	},
}
