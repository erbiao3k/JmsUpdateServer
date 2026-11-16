package syncer

import (
	"testing"

	"jmsupdateserver/internal/inventory"
	"jmsupdateserver/internal/jumpserver"
)

func TestHostIndexPrefersInstanceLabel(t *testing.T) {
	index := newHostIndex([]jumpserver.Host{
		{
			ID:      "asset-1",
			Name:    "old",
			Address: "10.0.0.8",
			Labels:  []jumpserver.Label{{Name: inventory.LabelInstanceID, Value: "i-1"}},
		},
		{ID: "asset-2", Name: "other", Address: "10.0.0.8"},
	})
	host, ok := index.find(inventory.Server{InstanceID: "i-1", PrivateIP: "10.0.0.9"})
	if !ok || host.ID != "asset-1" {
		t.Fatalf("host = %+v ok=%v", host, ok)
	}
}

func TestHostIndexSkipsAddressOwnedByAnotherInstance(t *testing.T) {
	index := newHostIndex([]jumpserver.Host{{
		ID:      "asset-2",
		Address: "10.0.0.8",
		Labels:  []jumpserver.Label{{Name: inventory.LabelInstanceID, Value: "i-other"}},
	}})
	if _, ok := index.find(inventory.Server{InstanceID: "i-1", PrivateIP: "10.0.0.8"}); ok {
		t.Fatal("matched another instance's asset")
	}
}

func TestConnectAddressFollowsOpenPort(t *testing.T) {
	server := inventory.Server{
		OS:        inventory.OSLinux,
		PrivateIP: "10.0.0.8",
		PublicIP:  "47.0.0.8",
		Probe:     inventory.Probe{OK: true, Scope: "public", Port: 2221},
	}
	if server.ConnectAddress() != "47.0.0.8" || server.ProtocolPort() != 2221 {
		t.Fatalf("address=%s port=%d", server.ConnectAddress(), server.ProtocolPort())
	}
	server.Probe = inventory.Probe{}
	if server.ConnectAddress() != "10.0.0.8" || server.ProtocolPort() != 22 {
		t.Fatalf("fallback address=%s port=%d", server.ConnectAddress(), server.ProtocolPort())
	}
}

func TestPermissionNamesFollowTagValue(t *testing.T) {
	permission, group, ok := namesForTagValue("test")
	if !ok || permission != "asset_permissions_test" || group != "user_group_test" {
		t.Fatalf("permission=%s group=%s ok=%v", permission, group, ok)
	}
	if _, _, ok := namesForTagValue(""); ok {
		t.Fatal("空标签值不应单独建组")
	}
	if _, _, ok := namesForTagValue("admin"); ok {
		t.Fatal("admin 只进入管理员组")
	}
	if _, _, ok := namesForTagValue("Admin"); ok {
		t.Fatal("Admin 只进入管理员组")
	}
	if _, _, ok := namesForTagValue("a/b"); ok {
		t.Fatal("含特殊字符的标签值不应单独建组")
	}
}
