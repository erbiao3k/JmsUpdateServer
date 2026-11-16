package aliyun

import (
	"testing"

	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/alibabacloud-go/tea/tea"

	"jmsupdateserver/internal/inventory"
)

func TestDropBlacklisted(t *testing.T) {
	ids, values := dropBlacklisted(
		[]string{"i-keep", "i-skip"},
		map[string]string{"i-keep": "test", "i-skip": "crawler"},
		map[string]struct{}{"i-skip": {}},
	)
	if len(ids) != 1 || ids[0] != "i-keep" || values["i-skip"] != "" || values["i-keep"] != "test" {
		t.Fatalf("ids=%v values=%v", ids, values)
	}
}

func TestRetryableNetworkAndThrottle(t *testing.T) {
	if !retryable(errString("Post \"https://ecs.me-east-1.aliyuncs.com/\": EOF")) {
		t.Fatal("EOF 应重试")
	}
	if !retryable(errString("read: connection reset by peer")) {
		t.Fatal("连接重置应重试")
	}
	if !retryable(errString("code: Throttling")) {
		t.Fatal("限流应重试")
	}
	if retryable(errString("InvalidAccessKeyId.AccessPolicyDenied")) {
		t.Fatal("权限错误不应重试")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestMapInstancePrefersEIPAndClassifiesWindows(t *testing.T) {
	server, ok := mapInstance(&ecs.DescribeInstancesResponseBodyInstancesInstance{
		InstanceId:   tea.String("i-win"),
		InstanceName: tea.String("desk"),
		RegionId:     tea.String("cn-beijing"),
		ZoneId:       tea.String("cn-beijing-a"),
		Status:       tea.String("Running"),
		OSType:       tea.String("windows"),
		OSName:       tea.String("Windows Server 2022"),
		VpcAttributes: &ecs.DescribeInstancesResponseBodyInstancesInstanceVpcAttributes{
			PrivateIpAddress: &ecs.DescribeInstancesResponseBodyInstancesInstanceVpcAttributesPrivateIpAddress{
				IpAddress: []*string{tea.String("10.0.0.5")},
			},
		},
		PublicIpAddress: &ecs.DescribeInstancesResponseBodyInstancesInstancePublicIpAddress{
			IpAddress: []*string{tea.String("1.1.1.1")},
		},
		EipAddress: &ecs.DescribeInstancesResponseBodyInstancesInstanceEipAddress{
			IpAddress: tea.String("47.98.1.2"),
		},
	})
	if !ok {
		t.Fatal("expected instance")
	}
	if server.OS != inventory.OSWindows || server.PrivateIP != "10.0.0.5" || server.PublicIP != "47.98.1.2" {
		t.Fatalf("server = %+v", server)
	}
}

func TestMapInstanceLinuxFallsBackToClassicIP(t *testing.T) {
	server, ok := mapInstance(&ecs.DescribeInstancesResponseBodyInstancesInstance{
		InstanceId: tea.String("i-linux"),
		OSName:     tea.String("Ubuntu 24.04 64位"),
		InnerIpAddress: &ecs.DescribeInstancesResponseBodyInstancesInstanceInnerIpAddress{
			IpAddress: []*string{tea.String("10.1.1.8")},
		},
		PublicIpAddress: &ecs.DescribeInstancesResponseBodyInstancesInstancePublicIpAddress{
			IpAddress: []*string{tea.String("47.1.1.8")},
		},
	})
	if !ok {
		t.Fatal("expected instance")
	}
	if server.OS != inventory.OSLinux || server.PrivateIP != "10.1.1.8" || server.PublicIP != "47.1.1.8" {
		t.Fatalf("server = %+v", server)
	}
}
