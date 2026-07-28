package rbac

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestViewerRole(t *testing.T) {
	perms := Resolve(RoleViewer, "")
	for _, p := range []Permission{
		PermChannelsRead, PermTokensRead, PermLogsRead, PermAnalyticsRead,
	} {
		if !Allow(perms, p) {
			t.Errorf("viewer should have %s", p)
		}
	}
	for _, p := range []Permission{
		PermChannelsWrite, PermChannelsDelete, PermUsersDelete,
		PermSecretsRotate, PermConfigWrite,
	} {
		if Allow(perms, p) {
			t.Errorf("viewer should NOT have %s", p)
		}
	}
}

func TestOperatorRole(t *testing.T) {
	perms := Resolve(RoleOperator, "")
	if !Allow(perms, PermChannelsWrite) {
		t.Error("operator should have channels:write")
	}
	if !Allow(perms, PermConfigWrite) {
		t.Error("operator should have config:write")
	}
	if Allow(perms, PermUsersDelete) {
		t.Error("operator should NOT have users:delete")
	}
	if Allow(perms, PermSecretsRotate) {
		t.Error("operator should NOT have secrets:rotate")
	}
}

func TestAdminRole(t *testing.T) {
	perms := Resolve(RoleAdmin, "")
	if !Allow(perms, PermChannelsWrite) {
		t.Error("admin should have channels:write")
	}
	if !Allow(perms, PermUsersDelete) {
		t.Error("admin should have users:delete")
	}
	if Allow(perms, PermSecretsRotate) {
		t.Error("admin should NOT have secrets:rotate")
	}
}

func TestRootRole(t *testing.T) {
	perms := Resolve(RoleRoot, "")
	for _, p := range AllPermissions() {
		if !Allow(perms, p) {
			t.Errorf("root should have %s", p)
		}
	}
}

func TestExplicitGrants(t *testing.T) {
	// Viewer explicitly granted channels:write.
	perms := Resolve(RoleViewer, `["+channels:write"]`)
	if !Allow(perms, PermChannelsWrite) {
		t.Error("explicit grant should work")
	}
	// Other viewer permissions intact.
	if !Allow(perms, PermChannelsRead) {
		t.Error("inherited perms should remain")
	}
}

func TestExplicitRevokes(t *testing.T) {
	// Admin explicitly revoked channels:delete.
	perms := Resolve(RoleAdmin, `["-channels:delete"]`)
	if Allow(perms, PermChannelsDelete) {
		t.Error("explicit revoke should work")
	}
	if !Allow(perms, PermChannelsWrite) {
		t.Error("other admin perms should remain")
	}
}

func TestInvalidJSONFallsBack(t *testing.T) {
	perms := Resolve(RoleViewer, "not json")
	if !Allow(perms, PermChannelsRead) {
		t.Error("fallback should grant role perms")
	}
	if Allow(perms, PermChannelsWrite) {
		t.Error("fallback should NOT grant non-role perms")
	}
}

func TestEncode(t *testing.T) {
	got := Encode([]string{"a:b"}, []string{"c:d"})
	if got == "" {
		t.Fatal("empty")
	}
	var entries []string
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	sort.Strings(entries)
	want := []string{"+a:b", "-c:d"}
	if len(entries) != 2 || entries[0] != want[0] || entries[1] != want[1] {
		t.Errorf("got %v, want %v", entries, want)
	}
}

func TestUnknownRoleHasNoPerms(t *testing.T) {
	perms := Resolve(Role("alien"), "")
	if len(perms) != 0 {
		t.Errorf("unknown role should have no perms, got %v", perms)
	}
}

func TestAllRolesKnown(t *testing.T) {
	for _, r := range AllRoles() {
		perms := Resolve(r, "")
		if r == RoleRoot && len(perms) == 0 {
			t.Errorf("root has no perms: %v", r)
		}
	}
}