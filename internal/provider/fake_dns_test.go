package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	dnsv1beta1 "go.origincloud.eu/sdk/dns/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The name servers the fake gives every public zone.
var dnsNameServers = []string{"ns1.origincloud.zone", "ns2.origincloud.zone"}

// fakeDNS serves the DNS service's REST API from memory: hosted zones,
// their record sets and private zones' VPC attachments. Its mutations
// answer synchronously, as the API's do; its lists answer one item per
// page, oldest first.
//
// A public zone starts PENDING_VERIFICATION with its challenge and the
// name servers, a private zone ACTIVE with neither; verify makes a pending
// zone ACTIVE, as the platform's own check would. A record set left
// without a TTL gets the API's default, 300 seconds.
//
// It refuses what the API refuses that the tests rely on: a zone of one
// of the platform's own domains; a record set outside its zone, a second
// one of the same name and type, or an alias to another project's
// floating IP or of a type other than A; attaching a public zone, or a
// VPC of another project; and deleting a zone that holds record sets or
// attachments.
type fakeDNS struct {
	*httptest.Server

	mu          sync.Mutex
	next        int
	zones       map[string]*dnsv1beta1.HostedZone
	recordSets  map[string]*dnsv1beta1.RecordSet
	attachments map[string]*dnsv1beta1.VpcAttachment
	// calls are the mutations received, such as
	// "DELETE projects/p-1/hostedZones/hz-1", with their update masks, such
	// as "PATCH projects/p-1/hostedZones/hz-1/recordSets/rrset-2 ttl".
	calls []string
}

func newFakeDNS(t *testing.T) *fakeDNS {
	f := &fakeDNS{
		next:        1,
		zones:       map[string]*dnsv1beta1.HostedZone{},
		recordSets:  map[string]*dnsv1beta1.RecordSet{},
		attachments: map[string]*dnsv1beta1.VpcAttachment{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDNS) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// verify makes a pending public zone ACTIVE, as the platform's check does
// once the zone's token answers.
func (f *fakeDNS) verify(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	zone := f.zones[name]
	zone.State = dnsv1beta1.HostedZone_ACTIVE
	zone.Challenge.ExpireTime = nil
	zone.Challenge.VerifyTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
}

func (f *fakeDNS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	s := strings.Split(path, "/")
	if r.Method != http.MethodGet {
		call := r.Method + " " + path
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			call += " " + mask
		}
		f.calls = append(f.calls, call)
	}
	body, _ := io.ReadAll(r.Body)

	switch {
	case len(s) == 3 && s[0] == "projects" && s[2] == "hostedZones" && r.Method == http.MethodGet:
		items, next := page(f.zones, path+"/", r, func(*dnsv1beta1.HostedZone) bool { return true })
		writeMessage(w, &dnsv1beta1.ListHostedZonesResponse{HostedZones: items, NextPageToken: next})
	case len(s) == 3 && s[0] == "projects" && s[2] == "hostedZones" && r.Method == http.MethodPost:
		f.createZone(w, path, body)
	case len(s) == 4 && s[2] == "hostedZones" && r.Method == http.MethodDelete:
		f.deleteZone(w, path)
	case len(s) == 4 && s[2] == "hostedZones":
		serveSynchronous(w, r, path, body, f.zones, "tags")
	case len(s) == 5 && s[4] == "recordSets" && r.Method == http.MethodGet:
		items, next := page(f.recordSets, path+"/", r, func(*dnsv1beta1.RecordSet) bool { return true })
		writeMessage(w, &dnsv1beta1.ListRecordSetsResponse{RecordSets: items, NextPageToken: next})
	case len(s) == 5 && s[4] == "recordSets" && r.Method == http.MethodPost:
		f.createRecordSet(w, path, body)
	case len(s) == 6 && s[4] == "recordSets" && r.Method == http.MethodPatch:
		f.updateRecordSet(w, r, path, body)
	case len(s) == 6 && s[4] == "recordSets":
		serveSynchronous(w, r, path, body, f.recordSets)
	case len(s) == 5 && s[4] == "vpcAttachments" && r.Method == http.MethodGet:
		items, next := page(f.attachments, path+"/", r, func(*dnsv1beta1.VpcAttachment) bool { return true })
		writeMessage(w, &dnsv1beta1.ListVpcAttachmentsResponse{VpcAttachments: items, NextPageToken: next})
	case len(s) == 5 && s[4] == "vpcAttachments" && r.Method == http.MethodPost:
		f.createAttachment(w, path, body)
	case len(s) == 6 && s[4] == "vpcAttachments" && (r.Method == http.MethodGet || r.Method == http.MethodDelete):
		serveSynchronous(w, r, path, body, f.attachments)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeDNS) createZone(w http.ResponseWriter, collection string, body []byte) {
	zone := new(dnsv1beta1.HostedZone)
	if err := protojson.Unmarshal(body, zone); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	zone.DnsName = strings.ToLower(zone.GetDnsName())
	if zone.GetDnsName() == "origincloud.eu" || strings.HasSuffix(zone.GetDnsName(), ".origincloud.eu") {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", zone.GetDnsName()+" is one of the platform's own domains")
		return
	}
	f.next++
	zone.Name = fmt.Sprintf("%s/hz-%d", collection, f.next)
	zone.Urn = "urn:origincloud:dns:hosted-zone/" + zone.Name
	zone.Creator = "user:test"
	zone.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	switch zone.GetVisibility() {
	case dnsv1beta1.HostedZone_PUBLIC:
		zone.State = dnsv1beta1.HostedZone_PENDING_VERIFICATION
		zone.NameServers = dnsNameServers
		zone.Challenge = &dnsv1beta1.OwnershipChallenge{
			DnsName:    "_origin-challenge." + zone.GetDnsName(),
			Token:      fmt.Sprintf("origin-verify=token-%d", f.next),
			ExpireTime: timestamppb.New(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)),
		}
	case dnsv1beta1.HostedZone_PRIVATE:
		zone.State = dnsv1beta1.HostedZone_ACTIVE
	default:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a zone needs a visibility")
		return
	}
	f.zones[zone.Name] = zone
	writeMessage(w, zone)
}

func (f *fakeDNS) deleteZone(w http.ResponseWriter, name string) {
	if _, ok := f.zones[name]; !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	for child := range f.recordSets {
		if strings.HasPrefix(child, name+"/") {
			writeError(w, http.StatusConflict, "CONFLICT", name+" holds record sets: delete them first")
			return
		}
	}
	for child := range f.attachments {
		if strings.HasPrefix(child, name+"/") {
			writeError(w, http.StatusConflict, "CONFLICT", name+" is attached to VPCs: detach it first")
			return
		}
	}
	delete(f.zones, name)
	writeMessage(w, &emptypb.Empty{})
}

// checkRecordSet refuses what the API refuses of a record set of zone.
func checkRecordSet(zone *dnsv1beta1.HostedZone, rs *dnsv1beta1.RecordSet) string {
	if alias := rs.GetAlias(); alias != nil {
		project := strings.Join(strings.Split(zone.GetName(), "/")[:2], "/")
		switch {
		case rs.GetType() != dnsv1beta1.RecordSet_A:
			return "an alias is of type A"
		case !strings.HasPrefix(alias.GetFloatingIp(), project+"/"):
			return alias.GetFloatingIp() + " isn't a floating IP of the zone's project"
		}
	}
	if rs.GetData() == nil {
		return "a record set needs basic or alias"
	}
	return ""
}

func (f *fakeDNS) createRecordSet(w http.ResponseWriter, collection string, body []byte) {
	parent := path.Dir(collection)
	zone, ok := f.zones[parent]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", parent+" doesn't exist")
		return
	}
	rs := new(dnsv1beta1.RecordSet)
	if err := protojson.Unmarshal(body, rs); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	rs.DnsName = strings.ToLower(rs.GetDnsName())
	if rs.GetDnsName() != zone.GetDnsName() && !strings.HasSuffix(rs.GetDnsName(), "."+zone.GetDnsName()) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", rs.GetDnsName()+" is outside the zone "+zone.GetDnsName())
		return
	}
	if err := checkRecordSet(zone, rs); err != "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return
	}
	for _, other := range f.recordSets {
		if strings.HasPrefix(other.GetName(), parent+"/") && other.GetDnsName() == rs.GetDnsName() && other.GetType() == rs.GetType() {
			writeError(w, http.StatusConflict, "CONFLICT", "the zone holds a record set "+rs.GetDnsName()+" "+rs.GetType().String())
			return
		}
	}
	if rs.Ttl == nil {
		rs.Ttl = durationpb.New(300 * time.Second)
	}
	f.next++
	rs.Name = fmt.Sprintf("%s/recordSets/rrset-%d", parent, f.next)
	rs.Urn = "urn:origincloud:dns:record-set/" + rs.Name
	rs.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	rs.UpdateTime = rs.CreateTime
	f.recordSets[rs.Name] = rs
	writeMessage(w, rs)
}

// updateRecordSet replaces the record set's TTL or data as the mask names
// them: naming a kind replaces the kind it had, and naming ttl without one
// sets the default.
func (f *fakeDNS) updateRecordSet(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	rs, ok := f.recordSets[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	updated := proto.Clone(rs).(*dnsv1beta1.RecordSet)
	if err := updateMasked(updated, body, r.URL.Query().Get("updateMask"), "ttl", "basic", "alias"); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err := checkRecordSet(f.zones[path.Dir(path.Dir(name))], updated); err != "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return
	}
	if updated.Ttl == nil {
		updated.Ttl = durationpb.New(300 * time.Second)
	}
	updated.UpdateTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	f.recordSets[name] = updated
	writeMessage(w, updated)
}

func (f *fakeDNS) createAttachment(w http.ResponseWriter, collection string, body []byte) {
	parent := path.Dir(collection)
	zone, ok := f.zones[parent]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", parent+" doesn't exist")
		return
	}
	attachment := new(dnsv1beta1.VpcAttachment)
	if err := protojson.Unmarshal(body, attachment); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	project := strings.Join(strings.Split(parent, "/")[:2], "/")
	switch {
	case zone.GetVisibility() == dnsv1beta1.HostedZone_PUBLIC:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a public zone can't be attached to a VPC")
		return
	case !strings.HasPrefix(attachment.GetVpc(), project+"/"):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", attachment.GetVpc()+" isn't a VPC of the zone's project")
		return
	}
	attachment.Name = parent + "/vpcAttachments/" + path.Base(attachment.GetVpc())
	if _, ok := f.attachments[attachment.Name]; ok {
		writeError(w, http.StatusConflict, "CONFLICT", "the zone is attached to "+attachment.GetVpc())
		return
	}
	attachment.Urn = "urn:origincloud:dns:vpc-attachment/" + attachment.Name
	attachment.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	f.attachments[attachment.Name] = attachment
	writeMessage(w, attachment)
}
