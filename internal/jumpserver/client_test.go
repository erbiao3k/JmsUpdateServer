package jumpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"jmsupdateserver/internal/inventory"
)

func TestHostUnmarshalIgnoresNullLabelAndComment(t *testing.T) {
	var host Host
	raw := []byte(`{"id":"h1","name":"n","address":"10.0.0.1","comment":null,"platform":{"id":1},"labels":[null,{"name":"ecs_instance_id","value":"i-1","color":"#fff"}]}`)
	if err := json.Unmarshal(raw, &host); err != nil {
		t.Fatal(err)
	}
	if host.Comment != "" || host.Platform.ID != 1 || len(host.Labels) != 1 || host.Labels[0].Value != "i-1" {
		t.Fatalf("%+v", host)
	}
}

func TestMergeLabelsKeepsForeignLabelsAndSkipsIPv6(t *testing.T) {
	merged := MergeLabels([]Label{
		{Name: "env", Value: "prod"},
		{Name: inventory.LabelPrivateIP, Value: "10.0.0.1"},
	}, map[string]string{
		inventory.LabelInstanceID: "i-1",
		inventory.LabelPrivateIP:  "2001:db8::1",
		inventory.LabelPublicIP:   "47.0.0.1",
		inventory.LabelRegion:     "cn-hangzhou",
	})
	got := map[string]string{}
	for _, label := range merged {
		got[label.Name] = label.Value
	}
	if got["env"] != "prod" || got[inventory.LabelPublicIP] != "47.0.0.1" || got[inventory.LabelInstanceID] != "i-1" {
		t.Fatalf("labels = %+v", merged)
	}
	if _, ok := got[inventory.LabelPrivateIP]; ok {
		t.Fatalf("ipv6 should not be stored as a label: %+v", merged)
	}
}

func TestManagedCommentPreservesHumanNote(t *testing.T) {
	server := inventory.Server{InstanceID: "i-1", RegionID: "cn-hangzhou", PrivateIP: "10.0.0.1"}
	comment, changed := ManagedComment("值班备注", server)
	if changed || comment != "值班备注" {
		t.Fatalf("comment = %q changed=%v", comment, changed)
	}
	comment, changed = ManagedComment("", server)
	if !changed || !strings.Contains(comment, "private_ip=10.0.0.1") {
		t.Fatalf("comment = %q changed=%v", comment, changed)
	}
}

func TestBuildProtocolsOverridesSSHPort(t *testing.T) {
	protocols := BuildProtocols([]ProtocolDef{
		{Name: "ssh", Port: 22, Primary: true},
		{Name: "sftp", Port: 22, Default: true},
		{Name: "telnet", Port: 23},
	}, inventory.OSLinux, 2221)
	if len(protocols) != 2 || protocols[0].Port != 2221 || protocols[1].Name != "sftp" || protocols[1].Port != 2221 {
		t.Fatalf("protocols = %+v", protocols)
	}
}
