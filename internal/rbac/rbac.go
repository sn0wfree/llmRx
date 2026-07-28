// Package rbac implements fine-grained role-based access control.
//
// A Permission is a string of the form "resource:action", e.g.
//   "channels:read", "channels:write", "tokens:rotate", "users:delete",
//   "secrets:rotate", "config:write", "guardrails:write", "combos:write",
//   "alerts:write", "logs:read", "analytics:read".
//
// Roles group permissions into named bundles. Built-in roles:
//   - "viewer": read-only across all resources
//   - "operator": viewer + write on channels/tokens/combos/plans/
//     guardrails/alerts; no user management or secret rotation
//   - "admin": operator + user management (no secret rotation)
//   - "root":   everything (default for the bootstrap user)
//
// Custom roles can be defined by adding rows to the User.Permissions
// column (a JSON array of permission strings). Permission resolution
// is: explicit per-user permissions > role permissions > deny by
// default.
package rbac

import (
	"encoding/json"
	"strings"
)

// Permission is a single capability, formatted as "resource:action".
type Permission string

// Built-in permissions.
const (
	// Read permissions.
	PermChannelsRead    Permission = "channels:read"
	PermChannelsWrite   Permission = "channels:write"
	PermChannelsDelete  Permission = "channels:delete"
	PermTokensRead      Permission = "tokens:read"
	PermTokensWrite     Permission = "tokens:write"
	PermTokensDelete    Permission = "tokens:delete"
	PermTokensRotate    Permission = "tokens:rotate"
	PermPlansRead       Permission = "plans:read"
	PermPlansWrite      Permission = "plans:write"
	PermPlansDelete     Permission = "plans:delete"
	PermCombosRead      Permission = "combos:read"
	PermCombosWrite     Permission = "combos:write"
	PermCombosDelete    Permission = "combos:delete"
	PermGuardrailsRead  Permission = "guardrails:read"
	PermGuardrailsWrite Permission = "guardrails:write"
	PermGuardrailsDel   Permission = "guardrails:delete"
	PermAlertsRead      Permission = "alerts:read"
	PermAlertsWrite     Permission = "alerts:write"
	PermAlertsDelete    Permission = "alerts:delete"
	PermLogsRead        Permission = "logs:read"
	PermAnalyticsRead   Permission = "analytics:read"
	PermUsersRead       Permission = "users:read"
	PermUsersWrite      Permission = "users:write"
	PermUsersDelete     Permission = "users:delete"
	PermConfigRead      Permission = "config:read"
	PermConfigWrite     Permission = "config:write"
	PermSecretsRotate   Permission = "secrets:rotate"
	PermProvidersRead   Permission = "providers:read"
	PermProvidersWrite  Permission = "providers:write"
)

// Role is a named bundle of permissions.
type Role string

// Built-in roles.
const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
	RoleRoot     Role = "root"
)

// rolePermissions returns the permissions granted by a built-in
// role. Unknown roles return an empty set.
func rolePermissions(role Role) map[Permission]bool {
	switch role {
	case RoleViewer:
		return map[Permission]bool{
			PermChannelsRead: true, PermTokensRead: true,
			PermPlansRead: true, PermCombosRead: true,
			PermGuardrailsRead: true, PermAlertsRead: true,
			PermLogsRead: true, PermAnalyticsRead: true,
			PermUsersRead: true, PermConfigRead: true,
			PermProvidersRead: true,
		}
	case RoleOperator:
		p := rolePermissions(RoleViewer)
		p[PermChannelsWrite] = true
		p[PermChannelsDelete] = true
		p[PermTokensWrite] = true
		p[PermTokensDelete] = true
		p[PermTokensRotate] = true
		p[PermPlansWrite] = true
		p[PermPlansDelete] = true
		p[PermCombosWrite] = true
		p[PermCombosDelete] = true
		p[PermGuardrailsWrite] = true
		p[PermGuardrailsDel] = true
		p[PermAlertsWrite] = true
		p[PermAlertsDelete] = true
		p[PermConfigWrite] = true
		p[PermProvidersWrite] = true
		return p
	case RoleAdmin:
		p := rolePermissions(RoleOperator)
		p[PermUsersRead] = true
		p[PermUsersWrite] = true
		p[PermUsersDelete] = true
		return p
	case RoleRoot:
		// root has every built-in permission.
		all := map[Permission]bool{}
		for _, p := range AllPermissions() {
			all[p] = true
		}
		return all
	}
	return map[Permission]bool{}
}

// AllPermissions returns every built-in permission constant.
func AllPermissions() []Permission {
	return []Permission{
		PermChannelsRead, PermChannelsWrite, PermChannelsDelete,
		PermTokensRead, PermTokensWrite, PermTokensDelete, PermTokensRotate,
		PermPlansRead, PermPlansWrite, PermPlansDelete,
		PermCombosRead, PermCombosWrite, PermCombosDelete,
		PermGuardrailsRead, PermGuardrailsWrite, PermGuardrailsDel,
		PermAlertsRead, PermAlertsWrite, PermAlertsDelete,
		PermLogsRead, PermAnalyticsRead,
		PermUsersRead, PermUsersWrite, PermUsersDelete,
		PermConfigRead, PermConfigWrite,
		PermSecretsRotate,
		PermProvidersRead, PermProvidersWrite,
	}
}

// AllRoles returns every built-in role.
func AllRoles() []Role {
	return []Role{RoleViewer, RoleOperator, RoleAdmin, RoleRoot}
}

// Resolve computes the effective permission set for a user with the
// given role + explicit per-user overrides. explicitJSON is the
// contents of the User.Permissions column (or "" if none). The
// returned set is the union of role permissions and explicit
// add/remove entries encoded as "+perm" / "-perm" strings.
func Resolve(role Role, explicitJSON string) map[Permission]bool {
	perms := rolePermissions(role)
	if explicitJSON == "" {
		return perms
	}
	var entries []string
	if err := json.Unmarshal([]byte(explicitJSON), &entries); err != nil {
		return perms
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		switch {
		case strings.HasPrefix(entry, "+"):
			perms[Permission(strings.TrimPrefix(entry, "+"))] = true
		case strings.HasPrefix(entry, "-"):
			delete(perms, Permission(strings.TrimPrefix(entry, "-")))
		default:
			perms[Permission(entry)] = true
		}
	}
	return perms
}

// Allow returns true if the permission set includes the given perm.
func Allow(perms map[Permission]bool, p Permission) bool {
	if perms == nil {
		return false
	}
	return perms[p]
}

// Encode serializes a set of explicit permission overrides into JSON
// suitable for the User.Permissions column. Used by admin tooling
// to grant/revoke individual capabilities.
func Encode(grants []string, revokes []string) string {
	var out []string
	for _, g := range grants {
		if !strings.HasPrefix(g, "+") {
			g = "+" + g
		}
		out = append(out, g)
	}
	for _, r := range revokes {
		if !strings.HasPrefix(r, "-") {
			r = "-" + r
		}
		out = append(out, r)
	}
	b, _ := json.Marshal(out)
	return string(b)
}