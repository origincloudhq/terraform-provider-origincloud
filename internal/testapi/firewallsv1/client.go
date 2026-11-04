package firewallsv1

import (
	"context"
	"net/http"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"go.origincloud.eu/sdk/core"
	"google.golang.org/protobuf/proto"
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

// KeyClient calls the test API's KeyService. Its paths are /v1/{name},
// /v1/{name}:enable and :disable, and /v1/{parent}/keys for creating.
type KeyClient struct {
	service *core.Service
}

// NewKeyClient returns a client of KeyService that sends its calls
// through client.
func NewKeyClient(client *core.Client) *KeyClient {
	return &KeyClient{service: client.Service("keys", false)}
}

func (c *KeyClient) GetKey(ctx context.Context, req *KeyNameRequest) (*Key, error) {
	out := new(Key)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetName(), Idempotent: true}, out)
	return out, err
}

func (c *KeyClient) ListKeys(ctx context.Context, req *ListKeysRequest) (*ListKeysResponse, error) {
	out := new(ListKeysResponse)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetParent() + "/keys", Idempotent: true}, out)
	return out, err
}

func (c *KeyClient) CreateKey(ctx context.Context, req *CreateKeyRequest) (*Key, error) {
	out := new(Key)
	err := c.service.Call(ctx, core.Request{Method: http.MethodPost, Path: "/v1/" + req.GetParent() + "/keys", Body: req.GetKey()}, out)
	return out, err
}

func (c *KeyClient) UpdateKey(ctx context.Context, req *UpdateKeyRequest) (*Key, error) {
	query, err := core.Query(req, "key")
	if err != nil {
		return nil, err
	}
	out := new(Key)
	err = c.service.Call(ctx, core.Request{Method: http.MethodPatch, Path: "/v1/" + req.GetKey().GetName(), Query: query, Body: req.GetKey()}, out)
	return out, err
}

func (c *KeyClient) DeleteKey(ctx context.Context, req *KeyNameRequest) (*core.Operation[*emptypb.Empty], error) {
	return c.operation(ctx, http.MethodDelete, "/v1/"+req.GetName())
}

func (c *KeyClient) EnableKey(ctx context.Context, req *KeyNameRequest) (*core.Operation[*Key], error) {
	return operation[*Key](ctx, c.service, http.MethodPost, "/v1/"+req.GetName()+":enable")
}

func (c *KeyClient) DisableKey(ctx context.Context, req *KeyNameRequest) (*core.Operation[*Key], error) {
	return operation[*Key](ctx, c.service, http.MethodPost, "/v1/"+req.GetName()+":disable")
}

func (c *KeyClient) operation(ctx context.Context, method, path string) (*core.Operation[*emptypb.Empty], error) {
	return operation[*emptypb.Empty](ctx, c.service, method, path)
}

func operation[T proto.Message](ctx context.Context, service *core.Service, method, path string) (*core.Operation[T], error) {
	out := new(longrunningpb.Operation)
	if err := service.Call(ctx, core.Request{Method: method, Path: path}, out); err != nil {
		return nil, err
	}
	return core.NewOperation[T](service, out), nil
}

// VaultClient calls the test API's VaultService. Its paths are /v1/{name},
// /v1/vaults for creating (the id in the query) and listing (the owner in
// the query), and /v1/{parent}/tickets for listing tickets.
type VaultClient struct {
	service *core.Service
}

// NewVaultClient returns a client of VaultService that sends its calls
// through client.
func NewVaultClient(client *core.Client) *VaultClient {
	return &VaultClient{service: client.Service("vaults", false)}
}

func (c *VaultClient) GetVault(ctx context.Context, req *VaultNameRequest) (*Vault, error) {
	out := new(Vault)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetName(), Idempotent: true}, out)
	return out, err
}

func (c *VaultClient) ListVaults(ctx context.Context, req *ListVaultsRequest) (*ListVaultsResponse, error) {
	query, err := core.Query(req)
	if err != nil {
		return nil, err
	}
	out := new(ListVaultsResponse)
	err = c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/vaults", Query: query, Idempotent: true}, out)
	return out, err
}

func (c *VaultClient) CreateVault(ctx context.Context, req *CreateVaultRequest) (*Vault, error) {
	query, err := core.Query(req, "vault")
	if err != nil {
		return nil, err
	}
	out := new(Vault)
	err = c.service.Call(ctx, core.Request{Method: http.MethodPost, Path: "/v1/vaults", Query: query, Body: req.GetVault()}, out)
	return out, err
}

func (c *VaultClient) UpdateVault(ctx context.Context, req *UpdateVaultRequest) (*Vault, error) {
	query, err := core.Query(req, "vault")
	if err != nil {
		return nil, err
	}
	out := new(Vault)
	err = c.service.Call(ctx, core.Request{Method: http.MethodPatch, Path: "/v1/" + req.GetVault().GetName(), Query: query, Body: req.GetVault()}, out)
	return out, err
}

func (c *VaultClient) DeleteVault(ctx context.Context, req *VaultNameRequest) (*emptypb.Empty, error) {
	out := new(emptypb.Empty)
	err := c.service.Call(ctx, core.Request{Method: http.MethodDelete, Path: "/v1/" + req.GetName()}, out)
	return out, err
}

func (c *VaultClient) GetVaultQuota(ctx context.Context, req *VaultNameRequest) (*VaultQuota, error) {
	out := new(VaultQuota)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetName(), Idempotent: true}, out)
	return out, err
}

func (c *VaultClient) UpdateVaultQuota(ctx context.Context, req *UpdateVaultQuotaRequest) (*VaultQuota, error) {
	query, err := core.Query(req, "vaultQuota")
	if err != nil {
		return nil, err
	}
	out := new(VaultQuota)
	err = c.service.Call(ctx, core.Request{Method: http.MethodPatch, Path: "/v1/" + req.GetVaultQuota().GetName(), Query: query, Body: req.GetVaultQuota()}, out)
	return out, err
}

func (c *VaultClient) GetTicket(ctx context.Context, req *VaultNameRequest) (*Ticket, error) {
	out := new(Ticket)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetName(), Idempotent: true}, out)
	return out, err
}

func (c *VaultClient) ListTickets(ctx context.Context, req *ListTicketsRequest) (*ListTicketsResponse, error) {
	out := new(ListTicketsResponse)
	err := c.service.Call(ctx, core.Request{Method: http.MethodGet, Path: "/v1/" + req.GetParent() + "/tickets", Idempotent: true}, out)
	return out, err
}
