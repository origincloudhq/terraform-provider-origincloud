package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	longrunningv1beta1 "go.origincloud.eu/sdk/longrunning/v1beta1"
	networkv1beta1 "go.origincloud.eu/sdk/network/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeNetwork serves the network service's REST API from memory, for the
// resources the tests use: VPCs, subnets, internet gateways, route tables
// and their routes and associations, security groups and their rules,
// ACLs and their rules and associations, network interfaces, and floating
// IPs and their associations.
//
// Every mutation answers with an operation, named in a zone as the
// operations API declares, a regional resource's in its region's first
// zone. The operation is done, unless pending is set: then it is done by
// the first wait on it. Lists answer one resource per page, oldest first.
//
// It refuses what the API refuses that the tests rely on: a reference to
// a resource that doesn't exist, or to one in another VPC; a security group
// rule without exactly one peer; and deleting a resource while another is
// in it or refers to it, such as a VPC that isn't empty. The attributes the
// API keeps of an association, such as a subnet's ACL or a floating IP's
// network interface, are read from the associations.
type fakeNetwork struct {
	*httptest.Server

	mu         sync.Mutex
	pending    bool
	next       int
	ops        int
	resources  map[string]networkResource
	operations map[string]*longrunningpb.Operation
	// calls are the mutations received, such as "DELETE projects/p/regions/r/vpcs/vpc-1".
	calls []string
	// refused are the deletes refused because the resource was in use.
	refused []string
}

// networkResource is a network resource.
type networkResource interface {
	proto.Message
	GetName() string
}

// networkKind is a resource type the fake serves.
type networkKind struct {
	// prefix starts its generated ids, such as "vpc" in vpc-1.
	prefix  string
	new     func() networkResource
	newList func() proto.Message
	// id, when set, is a new resource's id, as an association's is the id
	// of the resource it names.
	id func(networkResource) string
}

// networkKinds are the kinds served, by their pattern's collections.
var networkKinds = map[string]networkKind{
	"projects/regions/vpcs": {
		prefix: "vpc", new: func() networkResource { return new(networkv1beta1.Vpc) },
		newList: func() proto.Message { return new(networkv1beta1.ListVpcsResponse) },
	},
	"projects/zones/subnets": {
		prefix: "subnet", new: func() networkResource { return new(networkv1beta1.Subnet) },
		newList: func() proto.Message { return new(networkv1beta1.ListSubnetsResponse) },
	},
	"projects/regions/internetGateways": {
		prefix: "igw", new: func() networkResource { return new(networkv1beta1.InternetGateway) },
		newList: func() proto.Message { return new(networkv1beta1.ListInternetGatewaysResponse) },
	},
	"projects/regions/routeTables": {
		prefix: "rtb", new: func() networkResource { return new(networkv1beta1.RouteTable) },
		newList: func() proto.Message { return new(networkv1beta1.ListRouteTablesResponse) },
	},
	"projects/regions/routeTables/routes": {
		prefix: "route", new: func() networkResource { return new(networkv1beta1.Route) },
		newList: func() proto.Message { return new(networkv1beta1.ListRoutesResponse) },
	},
	"projects/zones/subnets/routeTableAssociations": {
		new:     func() networkResource { return new(networkv1beta1.RouteTableAssociation) },
		newList: func() proto.Message { return new(networkv1beta1.ListRouteTableAssociationsResponse) },
		id: func(m networkResource) string {
			return path.Base(m.(*networkv1beta1.RouteTableAssociation).GetRouteTable())
		},
	},
	"projects/regions/securityGroups": {
		prefix: "sg", new: func() networkResource { return new(networkv1beta1.SecurityGroup) },
		newList: func() proto.Message { return new(networkv1beta1.ListSecurityGroupsResponse) },
	},
	"projects/regions/securityGroups/rules": {
		prefix: "sgr", new: func() networkResource { return new(networkv1beta1.SecurityGroupRule) },
		newList: func() proto.Message { return new(networkv1beta1.ListSecurityGroupRulesResponse) },
	},
	"projects/regions/acls": {
		prefix: "acl", new: func() networkResource { return new(networkv1beta1.Acl) },
		newList: func() proto.Message { return new(networkv1beta1.ListAclsResponse) },
	},
	"projects/regions/acls/rules": {
		prefix: "aclr", new: func() networkResource { return new(networkv1beta1.AclRule) },
		newList: func() proto.Message { return new(networkv1beta1.ListAclRulesResponse) },
	},
	"projects/zones/subnets/aclAssociations": {
		new:     func() networkResource { return new(networkv1beta1.AclAssociation) },
		newList: func() proto.Message { return new(networkv1beta1.ListAclAssociationsResponse) },
		id:      func(m networkResource) string { return path.Base(m.(*networkv1beta1.AclAssociation).GetAcl()) },
	},
	"projects/zones/networkInterfaces": {
		prefix: "eni", new: func() networkResource { return new(networkv1beta1.NetworkInterface) },
		newList: func() proto.Message { return new(networkv1beta1.ListNetworkInterfacesResponse) },
	},
	"projects/zones/floatingIps": {
		prefix: "fip", new: func() networkResource { return new(networkv1beta1.FloatingIp) },
		newList: func() proto.Message { return new(networkv1beta1.ListFloatingIpsResponse) },
	},
	"projects/zones/floatingIps/associations": {
		new:     func() networkResource { return new(networkv1beta1.FloatingIpAssociation) },
		newList: func() proto.Message { return new(networkv1beta1.ListFloatingIpAssociationsResponse) },
		id: func(m networkResource) string {
			return path.Base(m.(*networkv1beta1.FloatingIpAssociation).GetNetworkInterface())
		},
	},
}

// The time every resource is created at.
var networkCreateTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func newFakeNetwork(t *testing.T) *fakeNetwork {
	f := &fakeNetwork{resources: map[string]networkResource{}, operations: map[string]*longrunningpb.Operation{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeNetwork) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// networkRefusal is a call the fake refuses, with the API's status.
type networkRefusal struct {
	code    int
	status  string
	message string
}

func (r *networkRefusal) Error() string { return r.message }

func networkInvalid(format string, args ...any) *networkRefusal {
	return &networkRefusal{http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf(format, args...)}
}

func networkPrecondition(format string, args ...any) *networkRefusal {
	return &networkRefusal{http.StatusBadRequest, "FAILED_PRECONDITION", fmt.Sprintf(format, args...)}
}

func (f *fakeNetwork) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+name)
	}
	body, _ := io.ReadAll(r.Body)
	if op, ok := strings.CutSuffix(name, ":wait"); ok && r.Method == http.MethodPost {
		f.wait(w, op)
		return
	}
	segments := strings.Split(name, "/")
	var collections []string
	for i := 0; i < len(segments); i += 2 {
		collections = append(collections, segments[i])
	}
	kind, ok := networkKinds[strings.Join(collections, "/")]
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	case len(segments)%2 == 1 && r.Method == http.MethodGet:
		f.list(w, r, kind, name)
	case len(segments)%2 == 1 && r.Method == http.MethodPost:
		f.create(w, kind, name, body)
	case len(segments)%2 == 0:
		f.resource(w, r, kind, name, body)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeNetwork) list(w http.ResponseWriter, r *http.Request, kind networkKind, collection string) {
	prefix := collection + "/"
	items, next := page(f.resources, prefix, r, func(m networkResource) bool {
		return !strings.Contains(strings.TrimPrefix(m.GetName(), prefix), "/")
	})
	list := kind.newList().ProtoReflect()
	fields := list.Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		switch {
		case field.IsList():
			values := list.Mutable(field).List()
			for _, item := range items {
				values.Append(protoreflect.ValueOfMessage(f.view(item).ProtoReflect()))
			}
		case field.Name() == "next_page_token":
			list.Set(field, protoreflect.ValueOfString(next))
		}
	}
	writeMessage(w, list.Interface())
}

func (f *fakeNetwork) create(w http.ResponseWriter, kind networkKind, collection string, body []byte) {
	m := kind.new()
	if err := protojson.Unmarshal(body, m); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	parent := path.Dir(collection)
	if strings.Count(parent, "/") > 3 && f.resources[parent] == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", parent+" doesn't exist")
		return
	}
	for _, ref := range networkReferences(m) {
		if f.resources[ref] == nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", ref+" doesn't exist")
			return
		}
	}
	f.next++
	if err := f.check(parent, m); err != nil {
		writeError(w, err.code, err.status, err.message)
		return
	}
	id := fmt.Sprintf("%s-%d", kind.prefix, f.next)
	if kind.id != nil {
		id = kind.id(m)
	}
	name := collection + "/" + id
	if f.resources[name] != nil {
		writeError(w, http.StatusConflict, "ALREADY_EXISTS", name+" already exists")
		return
	}
	setNetworkField(m, "name", protoreflect.ValueOfString(name))
	setNetworkField(m, "urn", protoreflect.ValueOfString("urn:origincloud:network:"+name))
	setNetworkField(m, "creator", protoreflect.ValueOfString("user:test"))
	setNetworkField(m, "create_time", protoreflect.ValueOfMessage(timestamppb.New(networkCreateTime).ProtoReflect()))
	f.resources[name] = m
	f.writeOperation(w, name, f.view(m))
}

// networkMutable are the fields an update may name, by their JSON names.
var networkMutable = []string{"tags", "autoAssignPublicIp", "sourceAddressCheck"}

func (f *fakeNetwork) resource(w http.ResponseWriter, r *http.Request, kind networkKind, name string, body []byte) {
	m, ok := f.resources[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, f.view(m))
	case http.MethodPatch:
		update := kind.new()
		if err := protojson.Unmarshal(body, update); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		fields := m.ProtoReflect().Descriptor().Fields()
		mask := strings.Split(r.URL.Query().Get("updateMask"), ",")
		for _, p := range mask {
			if field := fields.ByJSONName(p); field == nil || !slices.Contains(networkMutable, p) {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", p+" can't be updated")
				return
			}
		}
		for _, p := range mask {
			field := fields.ByJSONName(p)
			if update.ProtoReflect().Has(field) {
				m.ProtoReflect().Set(field, update.ProtoReflect().Get(field))
			} else {
				m.ProtoReflect().Clear(field)
			}
		}
		f.writeOperation(w, name, f.view(m))
	case http.MethodDelete:
		if user := f.user(name); user != "" {
			f.refused = append(f.refused, name)
			if strings.Contains(name, "/vpcs/") {
				writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", name+" isn't empty: "+user+" is in it")
			} else {
				writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", name+" is in use by "+user)
			}
			return
		}
		delete(f.resources, name)
		f.writeOperation(w, name, &emptypb.Empty{})
	}
}

// user returns the first resource, by name, that is in the one named name
// or refers to it, or "" when none is.
func (f *fakeNetwork) user(name string) string {
	var users []string
	for other, m := range f.resources {
		if strings.HasPrefix(other, name+"/") || slices.Contains(networkReferences(m), name) {
			users = append(users, other)
		}
	}
	slices.Sort(users)
	if len(users) == 0 {
		return ""
	}
	return users[0]
}

// check refuses a new resource m in parent as the API would, and sets the
// fields the API chooses.
func (f *fakeNetwork) check(parent string, m networkResource) *networkRefusal {
	switch m := m.(type) {
	case *networkv1beta1.Vpc:
		if m.Ipv4CidrBlock == "" {
			m.Ipv4CidrBlock = "172.16.0.0/16"
		}
		prefix, err := netip.ParsePrefix(m.Ipv4CidrBlock)
		if err != nil || !prefix.Addr().IsPrivate() || !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 28 || prefix.Masked() != prefix {
			return networkInvalid("ipv4_cidr_block %q isn't a private IPv4 range from /16 to /28", m.Ipv4CidrBlock)
		}
		m.Ipv6CidrBlock = fmt.Sprintf("2001:db8:%x::/56", f.next)
		m.SubnetIpv4PrefixLength = int32(min(prefix.Bits()+4, 28))
	case *networkv1beta1.Subnet:
		if err := networkInRegion(parent, m.Vpc); err != nil {
			return err
		}
		vpc := f.resources[m.Vpc].(*networkv1beta1.Vpc)
		cidr, ok := f.freeSubnetRange(vpc)
		if !ok {
			return networkPrecondition("%s has no room for another subnet", m.Vpc)
		}
		m.Ipv4CidrBlock = cidr.String()
		m.Ipv4Gateway = cidr.Addr().Next().String()
		m.Ipv4DnsServers = []string{m.Ipv4Gateway}
	case *networkv1beta1.InternetGateway:
		if err := networkInRegion(parent, m.Vpc); err != nil {
			return err
		}
		if igw := f.internetGateway(m.Vpc); igw != "" {
			return networkPrecondition("%s already has an internet gateway, %s", m.Vpc, igw)
		}
	case *networkv1beta1.RouteTable:
		if err := networkInRegion(parent, m.Vpc); err != nil {
			return err
		}
		vpc := f.resources[m.Vpc].(*networkv1beta1.Vpc)
		m.LocalRoutes = []string{vpc.Ipv4CidrBlock, vpc.Ipv6CidrBlock}
	case *networkv1beta1.Route:
		if m.Destination != "0.0.0.0/0" && m.Destination != "::/0" {
			return networkInvalid("destination %q isn't a default route", m.Destination)
		}
		if (m.InternetGateway == "") == (m.NatGateway == "") {
			return networkInvalid("set exactly one of internet_gateway and nat_gateway")
		}
		table := f.resources[parent].(*networkv1beta1.RouteTable)
		if m.InternetGateway != "" && f.vpcOf(m.InternetGateway) != table.Vpc {
			return networkInvalid("%s isn't in the route table's VPC", m.InternetGateway)
		}
	case *networkv1beta1.RouteTableAssociation:
		if f.vpcOf(m.RouteTable) != f.vpcOf(parent) {
			return networkInvalid("%s isn't in the subnet's VPC", m.RouteTable)
		}
		if f.associations(parent, "routeTableAssociations") != nil {
			return networkPrecondition("%s already has a route table association", parent)
		}
	case *networkv1beta1.SecurityGroup:
		return networkInRegion(parent, m.Vpc)
	case *networkv1beta1.SecurityGroupRule:
		peers := 0
		for _, set := range []bool{m.CidrBlock != "", m.PeerSecurityGroup != "", m.SameGroup} {
			if set {
				peers++
			}
		}
		if peers != 1 {
			return networkInvalid("set exactly one of cidr_block, peer_security_group and same_group")
		}
		if m.PeerSecurityGroup == parent {
			return networkInvalid("peer_security_group is the rule's own group: set same_group")
		}
		if m.PeerSecurityGroup != "" && f.vpcOf(m.PeerSecurityGroup) != f.vpcOf(parent) {
			return networkInvalid("%s isn't in the group's VPC", m.PeerSecurityGroup)
		}
		if m.CidrBlock != "" {
			if _, err := netip.ParsePrefix(m.CidrBlock); err != nil {
				return networkInvalid("cidr_block %q isn't an address range", m.CidrBlock)
			}
		}
		return networkPorts(m.FirstPort, m.LastPort)
	case *networkv1beta1.Acl:
		return networkInRegion(parent, m.Vpc)
	case *networkv1beta1.AclRule:
		if m.Priority < 1 {
			return networkInvalid("priority must be at least 1")
		}
		for name, other := range f.resources {
			if rule, ok := other.(*networkv1beta1.AclRule); ok && path.Dir(name) == parent+"/rules" &&
				rule.Direction == m.Direction && rule.Priority == m.Priority {
				return networkInvalid("%s already has priority %d", name, m.Priority)
			}
		}
		if _, err := netip.ParsePrefix(m.CidrBlock); err != nil {
			return networkInvalid("cidr_block %q isn't an address range", m.CidrBlock)
		}
		return networkPorts(m.FirstPort, m.LastPort)
	case *networkv1beta1.AclAssociation:
		if f.vpcOf(m.Acl) != f.vpcOf(parent) {
			return networkInvalid("%s isn't in the subnet's VPC", m.Acl)
		}
		if f.associations(parent, "aclAssociations") != nil {
			return networkPrecondition("%s already has an ACL association", parent)
		}
	case *networkv1beta1.NetworkInterface:
		if path.Dir(path.Dir(m.Subnet)) != parent {
			return networkInvalid("%s isn't in %s", m.Subnet, parent)
		}
		subnet := f.resources[m.Subnet].(*networkv1beta1.Subnet)
		m.Ipv4Address = f.freeAddress(subnet)
		m.State = networkv1beta1.NetworkInterface_AVAILABLE
		if m.SourceAddressCheck == nil {
			m.SourceAddressCheck = proto.Bool(true)
		}
	case *networkv1beta1.FloatingIp:
		m.Address = fmt.Sprintf("203.0.113.%d", f.next)
	case *networkv1beta1.FloatingIpAssociation:
		if path.Dir(path.Dir(m.NetworkInterface)) != path.Dir(path.Dir(parent)) {
			return networkInvalid("%s isn't in the floating IP's zone", m.NetworkInterface)
		}
		if f.associations(parent, "associations") != nil {
			return networkPrecondition("%s is already associated", parent)
		}
		m.PrivateAddress = f.resources[m.NetworkInterface].(*networkv1beta1.NetworkInterface).GetIpv4Address()
	}
	return nil
}

// view returns m as the API answers with it: with the attributes its
// associations and its contents set.
func (f *fakeNetwork) view(m networkResource) networkResource {
	m = proto.Clone(m).(networkResource)
	switch m := m.(type) {
	case *networkv1beta1.Vpc:
		m.InternetGateway = f.internetGateway(m.Name)
		prefix := netip.MustParsePrefix(m.Ipv4CidrBlock)
		m.AvailableSubnetCount = int32(1<<(m.SubnetIpv4PrefixLength-int32(prefix.Bits()))) - int32(len(f.inVPC(m.Name, "subnets")))
	case *networkv1beta1.Subnet:
		if a, ok := f.associations(m.Name, "routeTableAssociations").(*networkv1beta1.RouteTableAssociation); ok {
			m.RouteTable = a.RouteTable
		}
		if a, ok := f.associations(m.Name, "aclAssociations").(*networkv1beta1.AclAssociation); ok {
			m.Acl = a.Acl
		}
		interfaces := 0
		for _, other := range f.resources {
			if ni, ok := other.(*networkv1beta1.NetworkInterface); ok && ni.Subnet == m.Name {
				interfaces++
			}
		}
		m.AvailableIpv4AddressCount = int64(1)<<(32-netip.MustParsePrefix(m.Ipv4CidrBlock).Bits()) - 5 - int64(interfaces)
	case *networkv1beta1.FloatingIp:
		if a, ok := f.associations(m.Name, "associations").(*networkv1beta1.FloatingIpAssociation); ok {
			m.NetworkInterface, m.PrivateAddress = a.NetworkInterface, a.PrivateAddress
		}
	}
	return m
}

// associations returns parent's association in collection, or nil.
func (f *fakeNetwork) associations(parent, collection string) networkResource {
	for name, m := range f.resources {
		if path.Dir(name) == parent+"/"+collection {
			return m
		}
	}
	return nil
}

func (f *fakeNetwork) internetGateway(vpc string) string {
	for name, m := range f.resources {
		if igw, ok := m.(*networkv1beta1.InternetGateway); ok && igw.Vpc == vpc {
			return name
		}
	}
	return ""
}

// vpcOf returns the VPC the resource named name is in: its own vpc field,
// or its parent's.
func (f *fakeNetwork) vpcOf(name string) string {
	for ; strings.Count(name, "/") > 3; name = path.Dir(path.Dir(name)) {
		if m := f.resources[name]; m != nil {
			if field := m.ProtoReflect().Descriptor().Fields().ByName("vpc"); field != nil {
				return m.ProtoReflect().Get(field).String()
			}
		}
	}
	return ""
}

// inVPC returns the resources of collection that are in vpc.
func (f *fakeNetwork) inVPC(vpc, collection string) []networkResource {
	var in []networkResource
	for name, m := range f.resources {
		if path.Base(path.Dir(name)) == collection && f.vpcOf(name) == vpc {
			in = append(in, m)
		}
	}
	return in
}

// freeSubnetRange returns the first of vpc's subnet ranges no subnet has.
func (f *fakeNetwork) freeSubnetRange(vpc *networkv1beta1.Vpc) (netip.Prefix, bool) {
	prefix := netip.MustParsePrefix(vpc.Ipv4CidrBlock)
	used := map[string]bool{}
	for _, m := range f.inVPC(vpc.Name, "subnets") {
		used[m.(*networkv1beta1.Subnet).Ipv4CidrBlock] = true
	}
	start := prefix.Addr().As4()
	base := uint32(start[0])<<24 | uint32(start[1])<<16 | uint32(start[2])<<8 | uint32(start[3])
	size := uint32(1) << (32 - vpc.SubnetIpv4PrefixLength)
	for i := range uint32(1) << (vpc.SubnetIpv4PrefixLength - int32(prefix.Bits())) {
		a := base + i*size
		cidr := netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a)}), int(vpc.SubnetIpv4PrefixLength))
		if !used[cidr.String()] {
			return cidr, true
		}
	}
	return netip.Prefix{}, false
}

// freeAddress returns the first address of subnet no interface has, after
// the four the subnet keeps.
func (f *fakeNetwork) freeAddress(subnet *networkv1beta1.Subnet) string {
	used := map[string]bool{}
	for _, m := range f.resources {
		if ni, ok := m.(*networkv1beta1.NetworkInterface); ok && ni.Subnet == subnet.Name {
			used[ni.Ipv4Address] = true
		}
	}
	a := netip.MustParsePrefix(subnet.Ipv4CidrBlock).Addr().Next().Next().Next()
	for a = a.Next(); used[a.String()]; a = a.Next() {
	}
	return a.String()
}

// networkInRegion refuses a VPC in another project or region than parent.
func networkInRegion(parent, vpc string) *networkRefusal {
	project, region := networkLocation(parent)
	vpcProject, vpcRegion := networkLocation(vpc)
	if project != vpcProject || region != vpcRegion {
		return networkPrecondition("%s isn't in %s", vpc, parent)
	}
	return nil
}

// networkLocation returns the project and the region the resource named
// name is in, a zonal one's being its zone's.
func networkLocation(name string) (string, string) {
	segments := strings.Split(name, "/")
	if segments[2] == "zones" {
		return segments[1], segments[3][:strings.LastIndex(segments[3], "-")]
	}
	return segments[1], segments[3]
}

// networkPorts refuses a set of ports spelled otherwise than its one spelling.
func networkPorts(first, last int32) *networkRefusal {
	switch {
	case last != 0 && last == first:
		return networkInvalid("first_port %d and last_port %d is first_port %d and last_port 0", first, last, first)
	case last == 65535 && first <= 1:
		return networkInvalid("first_port %d and last_port 65535 is first_port 0 and last_port 0", first)
	case last != 0 && last < first:
		return networkInvalid("last_port %d is below first_port %d", last, first)
	}
	return nil
}

// networkReferences returns the resource names m's fields hold, other than its
// own.
func networkReferences(m proto.Message) []string {
	var refs []string
	m.ProtoReflect().Range(func(field protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if field.Kind() == protoreflect.StringKind && !field.IsList() && !field.IsMap() &&
			field.Name() != "name" && strings.HasPrefix(v.String(), "projects/") {
			refs = append(refs, v.String())
		}
		return true
	})
	return refs
}

// setNetworkField sets m's field named field, when m has one.
func setNetworkField(m proto.Message, field string, v protoreflect.Value) {
	if fd := m.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(field)); fd != nil {
		m.ProtoReflect().Set(fd, v)
	}
}

// writeOperation answers with an operation on target whose result is
// response, done unless the fake is pending.
func (f *fakeNetwork) writeOperation(w http.ResponseWriter, target string, response proto.Message) {
	segments := strings.Split(target, "/")
	zone := segments[3]
	if segments[2] == "regions" {
		zone += "-az1"
	}
	f.ops++
	metadata, _ := anypb.New(&longrunningv1beta1.OperationMetadata{Target: target})
	result, _ := anypb.New(response)
	op := &longrunningpb.Operation{
		Name:     "projects/" + segments[1] + "/zones/" + zone + "/operations/op-" + strconv.Itoa(f.ops),
		Metadata: metadata,
		Result:   &longrunningpb.Operation_Response{Response: result},
	}
	f.operations[op.Name] = op
	if !f.pending {
		op.Done = true
		writeMessage(w, op)
		return
	}
	writeMessage(w, &longrunningpb.Operation{Name: op.Name, Metadata: metadata})
	op.Done = true
}

// wait answers with the operation named name, done.
func (f *fakeNetwork) wait(w http.ResponseWriter, name string) {
	op, ok := f.operations[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	writeMessage(w, op)
}
