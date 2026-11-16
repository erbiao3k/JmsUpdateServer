package aliyun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/alibabacloud-go/tea/tea"

	"jmsupdateserver/internal/inventory"
)

type Region struct {
	ID       string
	Name     string
	Endpoint string
}

type Client struct {
	accessKeyID     string
	accessKeySecret string
	bootstrap       string
}

func New(accessKeyID, accessKeySecret, bootstrap string) *Client {
	return &Client{
		accessKeyID:     accessKeyID,
		accessKeySecret: accessKeySecret,
		bootstrap:       strings.TrimSpace(bootstrap),
	}
}

// ListTagged 拉取全部地域中标签匹配的 ECS。单个地域失败不会中断其他地域。
func (c *Client) ListTagged(ctx context.Context, tagKey string, regionConcurrency int, blacklist []string) ([]inventory.Server, []error) {
	regions, err := c.Regions(ctx)
	if err != nil {
		return nil, []error{err}
	}
	if regionConcurrency <= 0 {
		regionConcurrency = 5
	}
	skipped := idSet(blacklist)

	var (
		mu      sync.Mutex
		servers []inventory.Server
		errs    []error
		wg      sync.WaitGroup
		sem     = make(chan struct{}, regionConcurrency)
	)
	for _, region := range regions {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		wg.Add(1)
		go func(region Region) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			found, err := c.instancesInRegion(ctx, region, tagKey, skipped)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("地域 %s: %w", region.ID, err))
				return
			}
			servers = append(servers, found...)
		}(region)
	}
	wg.Wait()
	return servers, errs
}

func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	client, err := c.newClient(c.bootstrap)
	if err != nil {
		return nil, err
	}
	resp, err := call(ctx, func() (*ecs.DescribeRegionsResponse, error) {
		return client.DescribeRegions(&ecs.DescribeRegionsRequest{
			ResourceType:   tea.String("instance"),
			AcceptLanguage: tea.String("zh-CN"),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeRegions: %w", err)
	}
	if resp == nil || resp.Body == nil || resp.Body.Regions == nil {
		return nil, fmt.Errorf("DescribeRegions 返回空地域列表")
	}
	regions := make([]Region, 0, len(resp.Body.Regions.Region))
	for _, item := range resp.Body.Regions.Region {
		if item == nil {
			continue
		}
		id := deref(item.RegionId)
		if id == "" {
			continue
		}
		endpoint := deref(item.RegionEndpoint)
		if !strings.Contains(endpoint, ".") {
			endpoint = "ecs." + id + ".aliyuncs.com"
		}
		regions = append(regions, Region{
			ID:       id,
			Name:     deref(item.LocalName),
			Endpoint: endpoint,
		})
	}
	return regions, nil
}

func (c *Client) instancesInRegion(ctx context.Context, region Region, tagKey string, blacklist map[string]struct{}) ([]inventory.Server, error) {
	client, err := c.newClient(region.Endpoint)
	if err != nil {
		return nil, err
	}
	ids, values, err := listInstanceIDs(ctx, client, region.ID, tagKey)
	if err != nil {
		return nil, err
	}
	ids, values = dropBlacklisted(ids, values, blacklist)
	servers := make([]inventory.Server, 0, len(ids))
	for start := 0; start < len(ids); start += 100 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		raw, err := json.Marshal(ids[start:end])
		if err != nil {
			return nil, err
		}
		var nextToken string
		for page := 0; page < 20; page++ {
			req := &ecs.DescribeInstancesRequest{
				RegionId:    tea.String(region.ID),
				InstanceIds: tea.String(string(raw)),
				MaxResults:  tea.Int32(100),
			}
			if nextToken != "" {
				req.NextToken = tea.String(nextToken)
			}
			resp, err := call(ctx, func() (*ecs.DescribeInstancesResponse, error) {
				return client.DescribeInstances(req)
			})
			if err != nil {
				return nil, fmt.Errorf("DescribeInstances: %w", err)
			}
			if resp != nil && resp.Body != nil && resp.Body.Instances != nil {
				for _, inst := range resp.Body.Instances.Instance {
					server, ok := mapInstance(inst)
					if ok {
						server.Endpoint = region.Endpoint
						server.TagKey = tagKey
						server.TagValue = values[server.InstanceID]
						servers = append(servers, server)
					}
				}
			}
			if resp == nil || resp.Body == nil {
				break
			}
			nextToken = deref(resp.Body.NextToken)
			if nextToken == "" {
				break
			}
		}
	}
	return servers, nil
}

func listInstanceIDs(ctx context.Context, client *ecs.Client, regionID, tagKey string) ([]string, map[string]string, error) {
	seen := map[string]struct{}{}
	values := map[string]string{}
	ids := make([]string, 0)
	var nextToken string
	for page := 0; page < 10000; page++ {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		req := &ecs.ListTagResourcesRequest{
			RegionId:     tea.String(regionID),
			ResourceType: tea.String("instance"),
			Tag: []*ecs.ListTagResourcesRequestTag{{
				Key: tea.String(tagKey),
			}},
		}
		if nextToken != "" {
			req.NextToken = tea.String(nextToken)
		}
		resp, err := call(ctx, func() (*ecs.ListTagResourcesResponse, error) {
			return client.ListTagResources(req)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("ListTagResources: %w", err)
		}
		if resp == nil || resp.Body == nil || resp.Body.TagResources == nil {
			break
		}
		for _, item := range resp.Body.TagResources.TagResource {
			if item == nil {
				continue
			}
			id := deref(item.ResourceId)
			if id == "" || deref(item.TagKey) != tagKey {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			values[id] = deref(item.TagValue)
			ids = append(ids, id)
		}
		nextToken = deref(resp.Body.NextToken)
		if nextToken == "" {
			break
		}
	}
	return ids, values, nil
}

func mapInstance(inst *ecs.DescribeInstancesResponseBodyInstancesInstance) (inventory.Server, bool) {
	if inst == nil {
		return inventory.Server{}, false
	}
	id := deref(inst.InstanceId)
	if id == "" {
		return inventory.Server{}, false
	}
	osType := deref(inst.OSType)
	osName := deref(inst.OSName)
	return inventory.Server{
		InstanceID: id,
		Name:       deref(inst.InstanceName),
		RegionID:   deref(inst.RegionId),
		ZoneID:     deref(inst.ZoneId),
		Status:     deref(inst.Status),
		OSType:     osType,
		OSName:     osName,
		OS:         inventory.ClassifyOS(osType, osName),
		PrivateIP:  privateIP(inst),
		PublicIP:   publicIP(inst),
	}, true
}

func privateIP(inst *ecs.DescribeInstancesResponseBodyInstancesInstance) string {
	if inst.VpcAttributes != nil && inst.VpcAttributes.PrivateIpAddress != nil {
		if ip := firstIP(inst.VpcAttributes.PrivateIpAddress.IpAddress); ip != "" {
			return ip
		}
	}
	if inst.InnerIpAddress != nil {
		return firstIP(inst.InnerIpAddress.IpAddress)
	}
	return ""
}

func publicIP(inst *ecs.DescribeInstancesResponseBodyInstancesInstance) string {
	if inst.EipAddress != nil {
		if ip := deref(inst.EipAddress.IpAddress); ip != "" {
			return ip
		}
	}
	if inst.PublicIpAddress != nil {
		return firstIP(inst.PublicIpAddress.IpAddress)
	}
	return ""
}

func firstIP(values []*string) string {
	for _, value := range values {
		if ip := deref(value); ip != "" {
			return ip
		}
	}
	return ""
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (c *Client) newClient(endpoint string) (*ecs.Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	client, err := ecs.NewClient(&openapi.Config{
		AccessKeyId:     tea.String(c.accessKeyID),
		AccessKeySecret: tea.String(c.accessKeySecret),
		Endpoint:        tea.String(endpoint),
		ConnectTimeout:  tea.Int(connectTimeoutMS),
		ReadTimeout:     tea.Int(readTimeoutMS),
	})
	if err != nil {
		return nil, fmt.Errorf("创建 ECS 客户端 %s: %w", endpoint, err)
	}
	return client, nil
}

const (
	connectTimeoutMS = 20000
	readTimeoutMS    = 40000
	maxAttempts      = 6
)

func call[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		var result T
		result, err = fn()
		if err == nil {
			return result, nil
		}
		if !retryable(err) || attempt == maxAttempts-1 {
			return zero, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, err
}

func idSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func dropBlacklisted(ids []string, values map[string]string, blacklist map[string]struct{}) ([]string, map[string]string) {
	if len(blacklist) == 0 {
		return ids, values
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, skip := blacklist[id]; skip {
			delete(values, id)
			continue
		}
		kept = append(kept, id)
	}
	return kept, values
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, part := range []string{
		"throttling",
		"serviceunavailable",
		"eof",
		"connection reset",
		"timeout",
		"broken pipe",
		"connection refused",
		"tls handshake",
	} {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}
