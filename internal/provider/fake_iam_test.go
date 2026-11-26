package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	iamv1beta1 "go.origincloud.eu/sdk/iam/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// iamCaller is the user the fake IAM serves: users/me, whose own bindings
// organizations/- lists.
const iamCaller = "u-me"

// The fake IAM's catalog: three predefined roles and the permissions
// they grant.
var (
	iamPermissions = []*iamv1beta1.Permission{
		iamPermission("iam.member.list"), iamPermission("iam.role.assign"), iamPermission("iam.role.revoke"),
		iamPermission("pvs.volume.create"), iamPermission("pvs.volume.list"),
	}
	iamRoles = []*iamv1beta1.Role{
		{Name: "roles/owner", DisplayName: "Owner", Description: "Everything.",
			IncludedPermissions: []string{"permissions/iam.member.list", "permissions/iam.role.assign", "permissions/iam.role.revoke", "permissions/pvs.volume.create", "permissions/pvs.volume.list"}},
		{Name: "roles/editor", DisplayName: "Editor", Description: "Changes resources.",
			IncludedPermissions: []string{"permissions/pvs.volume.create", "permissions/pvs.volume.list"}},
		{Name: "roles/viewer", DisplayName: "Viewer", Description: "Reads resources.",
			IncludedPermissions: []string{"permissions/pvs.volume.list"}},
	}
)

func iamPermission(key string) *iamv1beta1.Permission {
	parts := strings.Split(key, ".")
	return &iamv1beta1.Permission{
		Name:    "permissions/" + key,
		Service: parts[0], ResourceType: parts[1], Action: parts[2],
	}
}

// An iamUser is a user account of the fake IAM.
type iamUser struct {
	email    string
	verified bool
	deleted  bool
}

// An iamBinding is a role binding of the fake IAM. It holds on to a user's
// account, or a service account, as the API's do, and is answered with
// the member as the account is now.
type iamBinding struct {
	name, role, project, creator string
	// user is the user's id, or serviceAccount the service account's.
	user, serviceAccount string
	// resources are the full names the binding is limited to, in name
	// order, as the API keeps them.
	resources []string
}

// fakeIAM serves IAM's REST API from memory. Its mutations answer
// synchronously, with the resource or nothing, as the API's do; its lists
// answer one item per page. A role binding is answered with its user's
// current verified address, as userId:{user} when it has none, and as
// deleted:user:{user} once the account is gone, which changeEmail,
// unverify and deleteUser bring about, as a user would outside Terraform.
// A service account create repeated with the same request id creates
// nothing and answers with the account the first one created;
// loseCreateAnswer drops that first answer, as a connection that breaks
// off after the server acted does. A federated
// credential is created only on an active WORKLOAD service account, with an
// https issuer and a condition on an identity that never changes, as the
// API checks; its delete leaves it DELETED. Service account keys are only
// read: Terraform never creates or deletes one, so a test adds them with
// addKey, as the CLI or the console would.
type fakeIAM struct {
	*httptest.Server

	mu              sync.Mutex
	next            int
	users           map[string]*iamUser
	bindings        map[string]*iamBinding
	serviceAccounts map[string]*iamv1beta1.ServiceAccount
	federated       map[string]*iamv1beta1.FederatedCredential
	keys            map[string]*iamv1beta1.ServiceAccountKey
	auditLog        []*iamv1beta1.AuditLogEntry
	// created are the resources each request id created.
	created map[string]string
	// loseCreateAnswer makes the next service account create close the
	// connection instead of answering, once the account exists.
	loseCreateAnswer bool
	// tokens, when set, are the access tokens the fake accepts, as a fake
	// security token service hands them out: a call without one of them,
	// or with one that has expired, is refused with 401 UNAUTHENTICATED,
	// and counted in refused. The audit log names a token's principal as
	// the actor of what it does; without tokens, it names iamCaller.
	tokens  map[string]fakeToken
	refused int
	// actor is the principal of the call being served, and actingParty
	// who acted for it, empty when no one did.
	actor, actingParty string
	// calls are the mutations received, such as
	// "DELETE organizations/acme/roleBindings/rb-1", and requestIDs their
	// request ids, empty for a mutation that sent none.
	calls, requestIDs []string
}

func newFakeIAM(t *testing.T) *fakeIAM {
	f := &fakeIAM{
		users: map[string]*iamUser{
			iamCaller: {email: "me@example.com", verified: true},
			"u-ana":   {email: "ana@example.com", verified: true},
			"u-bo":    {email: "bo@example.com", verified: true},
		},
		bindings: map[string]*iamBinding{
			// The organization's creator, its owner.
			"organizations/acme/roleBindings/rb-0": {name: "organizations/acme/roleBindings/rb-0", role: "roles/owner", creator: "platform", user: iamCaller},
		},
		serviceAccounts: map[string]*iamv1beta1.ServiceAccount{},
		federated:       map[string]*iamv1beta1.FederatedCredential{},
		keys:            map[string]*iamv1beta1.ServiceAccountKey{},
		created:         map[string]string{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIAM) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// mutationRequestIDs returns the request id of each mutation, in order.
func (f *fakeIAM) mutationRequestIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requestIDs)
}

// changeEmail gives the user user a new verified address.
func (f *fakeIAM) changeEmail(user, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[user].email, f.users[user].verified = email, true
}

// unverify leaves the user user without a verified address.
func (f *fakeIAM) unverify(user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[user].verified = false
}

// deleteUser deletes the user user's account.
func (f *fakeIAM) deleteUser(user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[user].deleted = true
}

// fakeToken is an access token a fake security token service handed out.
type fakeToken struct {
	principal string
	// actingParty acts for principal, such as the person who impersonates
	// a service account; empty when no one does.
	actingParty string
	expiry      time.Time
}

func (f *fakeIAM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actor, f.actingParty = "//iam.origincloudapis.eu/users/"+iamCaller, ""
	if f.tokens != nil {
		token, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok || !time.Now().Before(token.expiry) {
			f.refused++
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "the access token is missing, unknown or expired")
			return
		}
		f.actor, f.actingParty = token.principal, token.actingParty
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	s := strings.Split(path, "/")
	requestID := r.URL.Query().Get("requestId")
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+path)
		f.requestIDs = append(f.requestIDs, requestID)
	}
	body, _ := io.ReadAll(r.Body)

	switch {
	case path == "roles" && r.Method == http.MethodGet:
		writeMessage(w, &iamv1beta1.ListRolesResponse{Roles: iamRoles})
	case len(s) == 2 && s[0] == "roles" && r.Method == http.MethodGet:
		writeNamed(w, path, iamRoles)
	case path == "permissions" && r.Method == http.MethodGet:
		writeMessage(w, &iamv1beta1.ListPermissionsResponse{Permissions: iamPermissions})
	case len(s) == 2 && s[0] == "permissions" && r.Method == http.MethodGet:
		writeNamed(w, path, iamPermissions)
	case len(s) == 2 && s[0] == "users" && r.Method == http.MethodGet:
		f.user(w, s[1])
	case len(s) == 3 && s[0] == "organizations" && s[2] == "roleBindings" && r.Method == http.MethodGet:
		f.listBindings(w, r, s[1])
	case len(s) == 3 && s[0] == "organizations" && s[2] == "roleBindings" && r.Method == http.MethodPost:
		f.createBinding(w, s[1], requestID, body)
	case len(s) == 4 && s[0] == "organizations" && s[2] == "roleBindings":
		f.binding(w, r, path, requestID, body)
	case len(s) == 3 && s[0] == "organizations" && s[2] == "serviceAccounts" && r.Method == http.MethodGet:
		show := r.URL.Query().Get("showDeleted") == "true"
		items, next := page(f.serviceAccounts, path+"/", r, func(a *iamv1beta1.ServiceAccount) bool {
			return show || a.GetState() != iamv1beta1.ServiceAccount_DELETED
		})
		writeMessage(w, &iamv1beta1.ListServiceAccountsResponse{ServiceAccounts: items, NextPageToken: next})
	case len(s) == 3 && s[0] == "organizations" && s[2] == "serviceAccounts" && r.Method == http.MethodPost:
		f.createServiceAccount(w, s[1], requestID, body)
	case len(s) == 4 && s[0] == "organizations" && s[2] == "serviceAccounts":
		f.serviceAccount(w, r, path)
	case len(s) == 5 && s[0] == "organizations" && s[2] == "serviceAccounts" && s[4] == "federatedCredentials" && r.Method == http.MethodGet:
		show := r.URL.Query().Get("showDeleted") == "true"
		items, next := page(f.federated, path+"/", r, func(c *iamv1beta1.FederatedCredential) bool {
			return show || c.GetState() != iamv1beta1.FederatedCredential_DELETED
		})
		writeMessage(w, &iamv1beta1.ListFederatedCredentialsResponse{FederatedCredentials: items, NextPageToken: next})
	case len(s) == 5 && s[0] == "organizations" && s[2] == "serviceAccounts" && s[4] == "federatedCredentials" && r.Method == http.MethodPost:
		f.createFederatedCredential(w, strings.Join(s[:4], "/"), requestID, body)
	case len(s) == 6 && s[0] == "organizations" && s[2] == "serviceAccounts" && s[4] == "federatedCredentials":
		f.federatedCredential(w, r, path)
	case len(s) == 5 && s[0] == "organizations" && s[2] == "serviceAccounts" && s[4] == "keys" && r.Method == http.MethodGet:
		show := r.URL.Query().Get("showDeleted") == "true"
		items, next := page(f.keys, path+"/", r, func(k *iamv1beta1.ServiceAccountKey) bool {
			return show || k.GetState() != iamv1beta1.ServiceAccountKey_DELETED
		})
		writeMessage(w, &iamv1beta1.ListServiceAccountKeysResponse{ServiceAccountKeys: items, NextPageToken: next})
	case len(s) == 6 && s[0] == "organizations" && s[2] == "serviceAccounts" && s[4] == "keys":
		f.serviceAccountKey(w, r, path)
	case len(s) == 3 && s[0] == "organizations" && s[2] == "auditLogEntries" && r.Method == http.MethodGet:
		f.listAuditLog(w, r, path+"/")
	case len(s) == 4 && s[0] == "organizations" && s[2] == "auditLogEntries" && r.Method == http.MethodGet:
		writeNamed(w, path, f.auditLog)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeIAM) user(w http.ResponseWriter, id string) {
	if id == "me" {
		id = iamCaller
	}
	u, ok := f.users[id]
	if !ok || u.deleted || id != iamCaller {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "users/"+id+" doesn't exist")
		return
	}
	writeMessage(w, &iamv1beta1.User{Name: "users/" + id, Member: "//iam.origincloudapis.eu/users/" + id, Email: u.email, EmailVerified: u.verified, GivenName: "Me"})
}

// member returns how b's member is shown: by its user's verified address
// when it has one and own is false, own being a list of the caller's own
// bindings.
// member returns the full name of the member b is for, as the API names it.
func (f *fakeIAM) member(b *iamBinding, _ bool) string {
	if b.serviceAccount != "" {
		return "//iam.origincloudapis.eu/" + strings.Join(strings.Split(b.name, "/")[:2], "/") + "/serviceAccounts/" + b.serviceAccount
	}
	return "//iam.origincloudapis.eu/users/" + b.user
}

func (f *fakeIAM) bindingMessage(b *iamBinding, own bool) *iamv1beta1.RoleBinding {
	creator, role := b.creator, b.role
	if !strings.HasPrefix(role, "//") {
		role = "//iam.origincloudapis.eu/" + role
	}
	return &iamv1beta1.RoleBinding{
		Name:   b.name,
		Member: f.member(b, own), Role: role, Project: b.project, Creator: creator,
		Resources:  b.resources,
		CreateTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)),
	}
}

func (f *fakeIAM) listBindings(w http.ResponseWriter, r *http.Request, organization string) {
	own := organization == "-"
	prefix := "organizations/" + organization + "/roleBindings/"
	if own {
		prefix = "organizations/"
	}
	items, next := page(f.bindings, prefix, r, func(b *iamBinding) bool { return !own || b.user == iamCaller })
	answer := &iamv1beta1.ListRoleBindingsResponse{NextPageToken: next}
	for _, b := range items {
		answer.RoleBindings = append(answer.RoleBindings, f.bindingMessage(b, own))
	}
	writeMessage(w, answer)
}

func (f *fakeIAM) createBinding(w http.ResponseWriter, organization, requestID string, body []byte) {
	if name, ok := f.created[requestID]; ok && requestID != "" {
		writeMessage(w, f.bindingMessage(f.bindings[name], false))
		return
	}
	in := new(iamv1beta1.RoleBinding)
	if err := protojson.Unmarshal(body, in); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if !slices.ContainsFunc(iamRoles, func(r *iamv1beta1.Role) bool { return r.GetName() == relativeName(in.GetRole()) }) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no role "+in.GetRole())
		return
	}
	b := &iamBinding{role: in.GetRole(), project: in.GetProject(), creator: "//iam.origincloudapis.eu/users/" + iamCaller, name: "organizations/" + organization + "/"}
	if b.resources = sortedResources(w, in.GetResources()); b.resources == nil && len(in.GetResources()) > 0 {
		return
	}
	member := relativeName(in.GetMember())
	switch segments := strings.Split(member, "/"); {
	case len(segments) == 2 && segments[0] == "users":
		if u, ok := f.users[segments[1]]; !ok || u.deleted {
			writeError(w, http.StatusNotFound, "NOT_FOUND", in.GetMember()+" doesn't exist")
			return
		}
		b.user = segments[1]
	case len(segments) == 4 && segments[0] == "organizations" && segments[2] == "serviceAccounts":
		a, ok := f.serviceAccounts[member]
		if !ok || segments[1] != organization || a.GetState() == iamv1beta1.ServiceAccount_DELETED {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", in.GetMember()+" isn't an active service account of organizations/"+organization)
			return
		}
		b.serviceAccount = segments[3]
	default:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a member is a user or a service account by its full name, not "+in.GetMember())
		return
	}
	for _, other := range f.bindings {
		if strings.HasPrefix(other.name, "organizations/"+organization+"/") && other.user == b.user &&
			other.serviceAccount == b.serviceAccount && other.role == b.role && other.project == b.project {
			writeError(w, http.StatusConflict, "CONFLICT", in.GetMember()+" already holds "+b.role+" there")
			return
		}
	}
	f.next++
	b.name = fmt.Sprintf("organizations/%s/roleBindings/rb-%d", organization, f.next)
	f.bindings[b.name] = b
	f.created[requestID] = b.name
	f.audit(organization, b.project, "iam.role.assign", f.member(b, false), map[string]string{"role": b.role})
	writeMessage(w, f.bindingMessage(b, false))
}

// sortedResources returns a binding's resources in name order, as the API
// keeps them, or writes the API's refusal of a duplicate and returns nil.
func sortedResources(w http.ResponseWriter, resources []string) []string {
	if len(resources) == 0 {
		return nil
	}
	sorted := slices.Sorted(slices.Values(resources))
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "resources lists "+sorted[i]+" twice")
			return nil
		}
	}
	return sorted
}

// updateBinding replaces b's resources, the one field UpdateRoleBinding
// changes.
func (f *fakeIAM) updateBinding(w http.ResponseWriter, r *http.Request, b *iamBinding, body []byte) {
	if mask := r.URL.Query().Get("updateMask"); mask != "resources" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "update_mask must be resources, not "+strconv.Quote(mask))
		return
	}
	in := new(iamv1beta1.RoleBinding)
	if err := protojson.Unmarshal(body, in); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	resources := sortedResources(w, in.GetResources())
	if resources == nil && len(in.GetResources()) > 0 {
		return
	}
	b.resources = resources
	writeMessage(w, f.bindingMessage(b, false))
}

func (f *fakeIAM) binding(w http.ResponseWriter, r *http.Request, name, requestID string, body []byte) {
	b, ok := f.bindings[name]
	if !ok {
		if r.Method == http.MethodDelete && requestID != "" && f.created["delete "+requestID] == name {
			writeMessage(w, &emptypb.Empty{})
			return
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, f.bindingMessage(b, false))
	case http.MethodPatch:
		f.updateBinding(w, r, b, body)
	case http.MethodDelete:
		organization := strings.Split(name, "/")[1]
		if relativeName(b.role) == "roles/owner" && b.project == "" && f.owners(organization) == 1 {
			writeError(w, http.StatusConflict, "CONFLICT", "the organization's last owner can't be removed: bind another owner first")
			return
		}
		delete(f.bindings, name)
		f.created["delete "+requestID] = name
		f.audit(organization, b.project, "iam.role.revoke", f.member(b, false), map[string]string{"role": b.role})
		writeMessage(w, &emptypb.Empty{})
	default:
		writeError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", r.Method+" "+name+" isn't served")
	}
}

// owners returns the number of the organization's organization-wide
// owner bindings.
func (f *fakeIAM) owners(organization string) int {
	n := 0
	for _, b := range f.bindings {
		if strings.HasPrefix(b.name, "organizations/"+organization+"/") && relativeName(b.role) == "roles/owner" && b.project == "" {
			n++
		}
	}
	return n
}

func (f *fakeIAM) createServiceAccount(w http.ResponseWriter, organization, requestID string, body []byte) {
	if name, ok := f.created[requestID]; ok && requestID != "" {
		// A repeat creates nothing and answers with the account the first
		// request created.
		writeMessage(w, f.serviceAccounts[name])
		return
	}
	a := new(iamv1beta1.ServiceAccount)
	if err := protojson.Unmarshal(body, a); err != nil || a.GetDisplayName() == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a service account needs a display name")
		return
	}
	f.next++
	id := fmt.Sprintf("sa-%d", f.next)
	a.Name = "organizations/" + organization + "/serviceAccounts/" + id
	a.Member = "//iam.origincloudapis.eu/" + a.Name
	if a.GetType() == iamv1beta1.ServiceAccount_TYPE_UNSPECIFIED {
		a.Type = iamv1beta1.ServiceAccount_EXTERNAL
	}
	a.State = iamv1beta1.ServiceAccount_ACTIVE
	a.Creator = "//iam.origincloudapis.eu/users/" + iamCaller
	a.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	f.serviceAccounts[a.Name] = a
	f.created[requestID] = a.Name
	f.audit(organization, "", "iam.service_account.create", a.Member, map[string]string{"display_name": a.GetDisplayName()})
	if f.loseCreateAnswer {
		f.loseCreateAnswer = false
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
		return
	}
	writeMessage(w, a)
}

func (f *fakeIAM) serviceAccount(w http.ResponseWriter, r *http.Request, name string) {
	a, ok := f.serviceAccounts[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, a)
	case http.MethodDelete:
		// Deleting a deleted service account succeeds; its bindings go
		// with it.
		if a.GetState() != iamv1beta1.ServiceAccount_DELETED {
			a.State = iamv1beta1.ServiceAccount_DELETED
			a.DeleteTime = timestamppb.New(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
			id := a.GetMember()[strings.LastIndex(a.GetMember(), "/")+1:]
			for n, b := range f.bindings {
				if b.serviceAccount == id {
					delete(f.bindings, n)
				}
			}
			f.audit(strings.Split(name, "/")[1], "", "iam.service_account.revoke", a.GetMember(), nil)
		}
		writeMessage(w, &emptypb.Empty{})
	default:
		writeError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", r.Method+" "+name+" isn't served")
	}
}

// addKey adds an active key to account, as creating one with the CLI or
// the console does, and returns its name.
func (f *fakeIAM) addKey(account string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	k := &iamv1beta1.ServiceAccountKey{
		Name:       fmt.Sprintf("%s/keys/key-%d", account, f.next),
		Algorithm:  iamv1beta1.ServiceAccountKey_ES256,
		State:      iamv1beta1.ServiceAccountKey_ACTIVE,
		Creator:    "//iam.origincloudapis.eu/users/" + iamCaller,
		CreateTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)),
	}
	f.keys[k.Name] = k
	return k.Name
}

func (f *fakeIAM) serviceAccountKey(w http.ResponseWriter, r *http.Request, name string) {
	k, ok := f.keys[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, k)
	default:
		writeError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", r.Method+" "+name+" isn't served")
	}
}

// federatedImmutableClaims are the claims a federated credential's
// conditions must name one of, by issuer: identities that never change. Any
// other issuer's is sub.
var federatedImmutableClaims = map[string][]string{
	"https://token.actions.githubusercontent.com": {"repository_id", "repository_owner_id"},
	"https://gitlab.com":                          {"project_id", "namespace_id"},
}

func (f *fakeIAM) createFederatedCredential(w http.ResponseWriter, account, requestID string, body []byte) {
	if name, ok := f.created[requestID]; ok && requestID != "" {
		writeMessage(w, f.federated[name])
		return
	}
	a, ok := f.serviceAccounts[account]
	if !ok || a.GetState() != iamv1beta1.ServiceAccount_ACTIVE || a.GetType() != iamv1beta1.ServiceAccount_WORKLOAD {
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", account+" isn't an active WORKLOAD service account")
		return
	}
	c := new(iamv1beta1.FederatedCredential)
	if err := protojson.Unmarshal(body, c); err != nil || c.GetDisplayName() == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a federated credential needs a display name")
		return
	}
	if !strings.HasPrefix(c.GetIssuer(), "https://") {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the issuer must be an https URL, not "+c.GetIssuer())
		return
	}
	immutable, ok := federatedImmutableClaims[c.GetIssuer()]
	if !ok {
		immutable = []string{"sub"}
	}
	named := false
	for claim, value := range c.GetConditions() {
		if strings.Contains(value, "*") {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the condition "+claim+" contains *, which isn't a pattern")
			return
		}
		named = named || slices.Contains(immutable, claim)
	}
	if !named {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "the conditions must name one of "+strings.Join(immutable, ", ")+" for the issuer "+c.GetIssuer())
		return
	}
	if c.GetAudience() == "" {
		c.Audience = "https://sts.origincloudapis.eu"
	}
	f.next++
	c.Name = fmt.Sprintf("%s/federatedCredentials/fc-%d", account, f.next)
	c.State = iamv1beta1.FederatedCredential_ACTIVE
	c.Creator = "//iam.origincloudapis.eu/users/" + iamCaller
	c.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	f.federated[c.Name] = c
	f.created[requestID] = c.Name
	writeMessage(w, c)
}

func (f *fakeIAM) federatedCredential(w http.ResponseWriter, r *http.Request, name string) {
	c, ok := f.federated[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, c)
	case http.MethodDelete:
		f.retireFederatedCredential(c)
		writeMessage(w, &emptypb.Empty{})
	default:
		writeError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", r.Method+" "+name+" isn't served")
	}
}

// retireFederatedCredential leaves c DELETED; deleting a deleted one
// changes nothing.
func (f *fakeIAM) retireFederatedCredential(c *iamv1beta1.FederatedCredential) {
	if c.GetState() != iamv1beta1.FederatedCredential_DELETED {
		c.State = iamv1beta1.FederatedCredential_DELETED
		c.DeleteTime = timestamppb.New(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	}
}

// deleteFederatedCredential deletes the federated credential name outside
// Terraform.
func (f *fakeIAM) deleteFederatedCredential(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retireFederatedCredential(f.federated[name])
}

// audit records a change in the organization's audit log.
func (f *fakeIAM) audit(organization, project, action, target string, details map[string]string) {
	f.auditLog = append(f.auditLog, &iamv1beta1.AuditLogEntry{
		Name:    fmt.Sprintf("organizations/%s/auditLogEntries/e-%d", organization, len(f.auditLog)+1),
		Project: project, Actor: f.actor, ActingParty: f.actingParty, Action: action, Target: target,
		Outcome:   iamv1beta1.AuditLogEntry_SUCCEEDED,
		OccurTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, len(f.auditLog), 0, time.UTC)),
		Details:   details,
	})
}

// listAuditLog answers with the page r asks for of the entries whose names
// start with prefix, most recent first, one per page.
func (f *fakeIAM) listAuditLog(w http.ResponseWriter, r *http.Request, prefix string) {
	var entries []*iamv1beta1.AuditLogEntry
	for _, e := range slices.Backward(f.auditLog) {
		if strings.HasPrefix(e.GetName(), prefix) {
			entries = append(entries, e)
		}
	}
	i, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
	answer := &iamv1beta1.ListAuditLogEntriesResponse{}
	if i < len(entries) {
		answer.AuditLogEntries = entries[i : i+1]
		if i+1 < len(entries) {
			answer.NextPageToken = strconv.Itoa(i + 1)
		}
	}
	writeMessage(w, answer)
}

// bind grants the role role to the user user in the organization
// organization outside Terraform, and returns the binding's name.
func (f *fakeIAM) bind(organization, user, role string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	name := fmt.Sprintf("organizations/%s/roleBindings/rb-%d", organization, f.next)
	f.bindings[name] = &iamBinding{name: name, role: role, creator: "//iam.origincloudapis.eu/users/" + iamCaller, user: user}
	return name
}
