package noryxv1

import "testing"

// Every type has a slug that parses back, and belongs to the families of its configuration.
func TestServerTypes(t *testing.T) {
	for value := range ServerType_name {
		typ := ServerType(value)
		if got := ParseServerType(typ.Slug()); got != typ {
			t.Errorf("ParseServerType(%q) = %v", typ.Slug(), got)
		}
	}
	for _, tc := range []struct {
		types                []ServerType
		proxy, bungee, paper bool
		fabric, modded       bool
	}{
		{[]ServerType{ServerType_SERVER_TYPE_UNSPECIFIED, ServerType_SERVER_TYPE_VANILLA}, false, false, false, false, false},
		{[]ServerType{ServerType_SERVER_TYPE_PAPER, ServerType_SERVER_TYPE_PURPUR, ServerType_SERVER_TYPE_FOLIA, ServerType_SERVER_TYPE_LEAF}, false, false, true, false, false},
		{[]ServerType{ServerType_SERVER_TYPE_FABRIC, ServerType_SERVER_TYPE_QUILT}, false, false, false, true, true},
		{[]ServerType{ServerType_SERVER_TYPE_FORGE, ServerType_SERVER_TYPE_NEOFORGE}, false, false, false, false, true},
		{[]ServerType{ServerType_SERVER_TYPE_VELOCITY}, true, false, false, false, false},
		{[]ServerType{ServerType_SERVER_TYPE_BUNGEECORD, ServerType_SERVER_TYPE_WATERFALL}, true, true, false, false, false},
	} {
		for _, typ := range tc.types {
			got := [5]bool{typ.Proxy(), typ.Bungee(), typ.Paper(), typ.Fabric(), typ.Modded()}
			if want := [5]bool{tc.proxy, tc.bungee, tc.paper, tc.fabric, tc.modded}; got != want {
				t.Errorf("%v: proxy, bungee, paper, fabric, modded = %v, want %v", typ, got, want)
			}
		}
	}
	for slug, want := range map[string]ServerType{"paper": ServerType_SERVER_TYPE_PAPER, "NEOFORGE": ServerType_SERVER_TYPE_NEOFORGE, "spigot": 0, "": 0} {
		if got := ParseServerType(slug); got != want {
			t.Errorf("ParseServerType(%q) = %v, want %v", slug, got, want)
		}
	}
}

func TestSlugs(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{RestartPolicy_RESTART_POLICY_UNSPECIFIED.Slug(), "always"},
		{RestartPolicy_RESTART_POLICY_ON_CRASH.Slug(), "on_crash"},
		{OverlayFirewall_OVERLAY_FIREWALL_UNSPECIFIED.Slug(), ""},
		{OverlayFirewall_OVERLAY_FIREWALL_IN_PLACE.Slug(), "in_place"},
		{DatastoreEngine_DATASTORE_ENGINE_POSTGRES.Slug(), "postgres"},
		{ServerState_SERVER_STATE_RUNNING.Slug(), "running"},
		{FileSetAction_FILE_SET_ACTION_CREATED.Slug(), "created"},
	} {
		if tc.got != tc.want {
			t.Errorf("slug %q, want %q", tc.got, tc.want)
		}
	}
	for slug, want := range map[string]RestartPolicy{"never": RestartPolicy_RESTART_POLICY_NEVER, "on_crash": RestartPolicy_RESTART_POLICY_ON_CRASH, "sometimes": 0} {
		if got := ParseRestartPolicy(slug); got != want {
			t.Errorf("ParseRestartPolicy(%q) = %v, want %v", slug, got, want)
		}
	}
}
