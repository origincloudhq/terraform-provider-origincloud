package firewallsv1

import (
	"context"
	"net/http"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/types/known/emptypb"
)

// FirewallClient calls the test API's FirewallService, as the Go SDK's
// generated clients call theirs, for the provider's tests. Its paths are
// /v1/{name}, and /v1/{parent}/firewalls for creating and listing.
type FirewallClient struct {
	service *core.Service
}

// NewFirewallClient returns a client of FirewallService that sends its
// calls through client.
func NewFirewallClient(client *core.Client) *FirewallClient {
	return &FirewallClient{service: client.Service("firewalls", false)}
}

func (c *FirewallClient) GetFirewall(ctx context.Context, req *GetFirewallRequest) (*Firewall, error) {
	out := new(Firewall)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetName(), Idempotent: true}, out)
	return out, err
}

func (c *FirewallClient) ListFirewalls(ctx context.Context, req *ListFirewallsRequest) (*ListFirewallsResponse, error) {
	out := new(ListFirewallsResponse)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetParent() + "/firewalls", Idempotent: true}, out)
	return out, err
}

func (c *FirewallClient) CreateFirewall(ctx context.Context, req *CreateFirewallRequest) (*core.Operation[*Firewall], error) {
	out := new(longrunningpb.Operation)
	if err := c.service.Call(ctx, core.Request{Method: http.MethodPost, Path: "/v1/" + req.GetParent() + "/firewalls", Body: req.GetFirewall()}, out); err != nil {
		return nil, err
	}
	return core.NewOperation[*Firewall](c.service, out), nil
}

func (c *FirewallClient) UpdateFirewall(ctx context.Context, req *UpdateFirewallRequest) (*Firewall, error) {
	query, err := core.Query(req, "firewall")
	if err != nil {
		return nil, err
	}
	out := new(Firewall)
	err = c.service.Call(ctx, core.Request{Method: http.MethodPatch, Path: "/v1/" + req.GetFirewall().GetName(), Query: query, Body: req.GetFirewall()}, out)
	return out, err
}

func (c *FirewallClient) DeleteFirewall(ctx context.Context, req *DeleteFirewallRequest) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.service.Call(ctx, core.Request{Method: http.MethodDelete, Path: "/v1/" + req.GetName()}, out)
	return out, err
}
