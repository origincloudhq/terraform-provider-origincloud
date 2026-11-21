package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	kmsv1beta1 "go.origincloud.eu/sdk/kms/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const kmsRegion = "projects/p-1/regions/eu-central1"

// fakeKMS serves the key management service's REST API from memory, in the
// region eu-central1 of the project p-1. Its mutations answer
// synchronously with the resource, as the API's do; its lists answer one
// item per page. A key is created ENABLED with its first version, and
// DeleteKey leaves it DELETED, with a purge time. A disabled key refuses a
// grant, as does a signing key, a ttl outside 1 to 60 minutes, an empty
// encryption context and a grantee that isn't a host's SPIFFE ID; a grant
// is read until expire says it expired.
type fakeKMS struct {
	*httptest.Server

	mu     sync.Mutex
	next   int
	keys   map[string]*kmsv1beta1.Key
	grants map[string]*kmsv1beta1.Grant
	// calls are the mutations received, such as "DELETE projects/p-1/regions/eu-central1/keys/key-1".
	calls []string
}

func newFakeKMS(t *testing.T) *fakeKMS {
	f := &fakeKMS{
		keys:   map[string]*kmsv1beta1.Key{},
		grants: map[string]*kmsv1beta1.Grant{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeKMS) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// expire ends the grant name, as its time passing does.
func (f *fakeKMS) expire(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.grants, name)
}

// rotate adds a version to the key name, as RotateKey does outside
// Terraform.
func (f *fakeKMS) rotate(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addVersion(f.keys[name])
}

// disable disables the key name outside Terraform.
func (f *fakeKMS) disable(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[name].State = kmsv1beta1.Key_DISABLED
}

// seedKey creates a key outside Terraform, in state, and returns its name.
func (f *fakeKMS) seedKey(purpose kmsv1beta1.Key_Purpose, state kmsv1beta1.Key_State) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	k := &kmsv1beta1.Key{
		Name:       fmt.Sprintf("%s/keys/key-%d", kmsRegion, f.next),
		Purpose:    purpose,
		State:      state,
		CreateTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)),
	}
	f.addVersion(k)
	f.keys[k.Name] = k
	return k.Name
}

// seedGrant makes a grant on the key outside Terraform, and returns its
// name.
func (f *fakeKMS) seedGrant(key, purpose string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	g := &kmsv1beta1.Grant{
		Name:              fmt.Sprintf("%s/grants/grant-%d", key, f.next),
		Operation:         kmsv1beta1.Grant_DECRYPT,
		Grantee:           "spiffe://origincloud.internal/zones/eu-central1-az1/hosts/h-1",
		EncryptionContext: map[string]string{"purpose": purpose},
		Zone:              "eu-central1-az1",
		Delegate:          "//iam.origincloudapis.eu/users/u-me",
		CreateTime:        timestamppb.New(now),
		RenewTime:         timestamppb.New(now),
		ExpireTime:        timestamppb.New(now.Add(10 * time.Minute)),
	}
	f.grants[g.Name] = g
	return g.Name
}

func (f *fakeKMS) addVersion(k *kmsv1beta1.Key) {
	n := len(k.Versions) + 1
	v := &kmsv1beta1.Key_Version{
		KeyVersion: fmt.Sprintf("//kms.origincloudapis.eu/%s/versions/%d", k.Name, n),
		CreateTime: timestamppb.New(time.Date(2026, 11, 1, 12, n, 0, 0, time.UTC)),
	}
	k.Versions = append(k.Versions, v)
	k.PrimaryVersion = v.KeyVersion
}

func (f *fakeKMS) serve(w http.ResponseWriter, r *http.Request) {
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

	// projects/{p}/regions/{r}/keys...
	if len(s) < 5 || s[0] != "projects" || s[2] != "regions" || s[4] != "keys" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
		return
	}
	switch {
	case len(s) == 5 && r.Method == http.MethodGet:
		show := r.URL.Query().Get("showDeleted") == "true"
		items, next := page(f.keys, path+"/", r, func(k *kmsv1beta1.Key) bool {
			return show || k.GetState() != kmsv1beta1.Key_DELETED
		})
		writeMessage(w, &kmsv1beta1.ListKeysResponse{Keys: items, NextPageToken: next})
	case len(s) == 5 && r.Method == http.MethodPost:
		f.createKey(w, strings.Join(s[:4], "/"), body)
	case len(s) == 6:
		f.key(w, r, path, body)
	case len(s) == 7 && s[6] == "grants" && r.Method == http.MethodGet:
		items, next := page(f.grants, path+"/", r, func(*kmsv1beta1.Grant) bool { return true })
		writeMessage(w, &kmsv1beta1.ListGrantsResponse{Grants: items, NextPageToken: next})
	case len(s) == 7 && s[6] == "grants" && r.Method == http.MethodPost:
		f.createGrant(w, strings.Join(s[:6], "/"), body)
	case len(s) == 8 && s[6] == "grants":
		f.grant(w, r, path)
	case len(s) == 9 && s[6] == "versions" && s[8] == "publicKey" && r.Method == http.MethodGet:
		f.publicKey(w, strings.Join(s[:6], "/"), path, s[7])
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeKMS) createKey(w http.ResponseWriter, parent string, body []byte) {
	k := new(kmsv1beta1.Key)
	if err := protojson.Unmarshal(body, k); err != nil || k.GetPurpose() == kmsv1beta1.Key_PURPOSE_UNSPECIFIED {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a key needs a purpose")
		return
	}
	f.next++
	k.Name = fmt.Sprintf("%s/keys/key-%d", parent, f.next)
	k.State = kmsv1beta1.Key_ENABLED
	k.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	k.UpdateTime = k.CreateTime
	f.addVersion(k)
	f.keys[k.Name] = k
	writeMessage(w, k)
}

func (f *fakeKMS) key(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	k, ok := f.keys[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, k)
	case http.MethodPatch:
		if k.GetState() == kmsv1beta1.Key_DELETED {
			writeError(w, http.StatusConflict, "CONFLICT", name+" is deleted: undelete it first")
			return
		}
		if err := updateMasked(k, body, r.URL.Query().Get("updateMask"), "state", "tags"); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		if k.State != kmsv1beta1.Key_ENABLED && k.State != kmsv1beta1.Key_DISABLED {
			k.State = kmsv1beta1.Key_ENABLED // a mask without a state leaves it
		}
		k.UpdateTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
		writeMessage(w, k)
	case http.MethodDelete:
		// Deleting a deleted key succeeds.
		if k.GetState() != kmsv1beta1.Key_DELETED {
			k.State = kmsv1beta1.Key_DELETED
			k.PurgeTime = timestamppb.New(time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC))
		}
		writeMessage(w, k)
	default:
		writeError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", r.Method+" "+name+" isn't served")
	}
}

func (f *fakeKMS) createGrant(w http.ResponseWriter, key string, body []byte) {
	k, ok := f.keys[key]
	g := new(kmsv1beta1.Grant)
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "NOT_FOUND", key+" doesn't exist")
		return
	case protojson.Unmarshal(body, g) != nil:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the grant isn't valid")
		return
	case k.GetPurpose() != kmsv1beta1.Key_ENCRYPT_DECRYPT:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", key+" isn't an ENCRYPT_DECRYPT key")
		return
	case k.GetState() != kmsv1beta1.Key_ENABLED:
		writeError(w, http.StatusConflict, "CONFLICT", key+" isn't enabled")
		return
	case len(g.GetEncryptionContext()) == 0:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the encryption context is empty")
		return
	case g.GetTtl().AsDuration() < time.Minute || g.GetTtl().AsDuration() > time.Hour:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the ttl must be 1 to 60 minutes")
		return
	}
	_, rest, found := strings.Cut(g.GetGrantee(), "/zones/")
	zone, host, hostFound := strings.Cut(rest, "/hosts/")
	if !found || !hostFound || zone == "" || host == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", g.GetGrantee()+" isn't a host of the region")
		return
	}
	f.next++
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	g.Name = fmt.Sprintf("%s/grants/grant-%d", key, f.next)
	g.Zone = zone
	g.Delegate = "//iam.origincloudapis.eu/users/u-me"
	g.CreateTime, g.RenewTime = timestamppb.New(now), timestamppb.New(now)
	g.ExpireTime = timestamppb.New(now.Add(g.GetTtl().AsDuration()))
	f.grants[g.Name] = g
	// The ttl is input only: no answer has it.
	writeMessage(w, withoutTTL(g))
}

func (f *fakeKMS) grant(w http.ResponseWriter, r *http.Request, name string) {
	g, ok := f.grants[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist or has expired")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, withoutTTL(g))
	case http.MethodDelete:
		delete(f.grants, name)
		writeMessage(w, &emptypb.Empty{})
	default:
		writeError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", r.Method+" "+name+" isn't served")
	}
}

func (f *fakeKMS) publicKey(w http.ResponseWriter, key, name, version string) {
	k, ok := f.keys[key]
	if !ok || !slices.ContainsFunc(k.GetVersions(), func(v *kmsv1beta1.Key_Version) bool {
		return strings.HasSuffix(v.GetKeyVersion(), "/versions/"+version)
	}) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	writeMessage(w, &kmsv1beta1.PublicKey{
		Name:      name,
		Algorithm: k.GetPurpose(),
		Pem:       "-----BEGIN PUBLIC KEY-----\n" + key + "/" + version + "\n-----END PUBLIC KEY-----\n",
	})
}

// withoutTTL returns g as every answer shows it: the ttl is input only.
func withoutTTL(g *kmsv1beta1.Grant) *kmsv1beta1.Grant {
	answer := proto.Clone(g).(*kmsv1beta1.Grant)
	answer.Ttl = nil
	return answer
}
