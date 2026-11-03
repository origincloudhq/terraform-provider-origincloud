package provider

import (
	"encoding/json"
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
	"google.golang.org/protobuf/proto"
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
		{Name: "roles/owner", Urn: "urn:origincloud:iam:role/owner", DisplayName: "Owner", Description: "Everything.",
			IncludedPermissions: []string{"permissions/iam.member.list", "permissions/iam.role.assign", "permissions/iam.role.revoke", "permissions/pvs.volume.create", "permissions/pvs.volume.list"}},
		{Name: "roles/editor", Urn: "urn:origincloud:iam:role/editor", DisplayName: "Editor", Description: "Changes resources.",
			IncludedPermissions: []string{"permissions/pvs.volume.create", "permissions/pvs.volume.list"}},
		{Name: "roles/viewer", Urn: "urn:origincloud:iam:role/viewer", DisplayName: "Viewer", Description: "Reads resources.",
			IncludedPermissions: []string{"permissions/pvs.volume.list"}},
	}
)

func iamPermission(key string) *iamv1beta1.Permission {
	parts := strings.Split(key, ".")
	return &iamv1beta1.Permission{
		Name: "permissions/" + key, Urn: "urn:origincloud:iam:permission/" + key,
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
}

// fakeIAM serves IAM's REST API from memory. Its mutations answer
// synchronously, with the resource or nothing, as the API's do; its lists
// answer one item per page. A role binding is answered with its user's
// current verified address, as userId:{user} when it has none, and as
// deleted:user:{user} once the account is gone, which changeEmail,
// unverify and deleteUser bring about, as a user would outside Terraform.
// A service account's client secret is in its create's response only. A
// create repeated with the same request id is refused with 409 CONFLICT,
// naming the account in its ErrorInfo metadata, as the secret went out
// with the first answer only; loseCreateAnswer drops that first answer, as
// a connection that breaks off after the server acted does.
type fakeIAM struct {
	*httptest.Server

	mu              sync.Mutex
	next            int
	users           map[string]*iamUser
	bindings        map[string]*iamBinding
	serviceAccounts map[string]*iamv1beta1.ServiceAccount
	auditLog        []*iamv1beta1.AuditLogEntry
	// secrets are the client secrets generated, by service account.
	secrets map[string]string
	// created are the resources each request id created.
	created map[string]string
	// loseCreateAnswer makes the next service account create close the
	// connection instead of answering, once the account exists.
	loseCreateAnswer bool
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
		secrets:         map[string]string{},
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

func (f *fakeIAM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
		f.binding(w, r, path, requestID)
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
	member := "userId:" + id
	if u.verified {
		member = "user:" + u.email
	}
	writeMessage(w, &iamv1beta1.User{Name: "users/" + id, Member: member, Email: u.email, EmailVerified: u.verified, GivenName: "Me"})
}

// member returns how b's member is shown: by its user's verified address
// when it has one and own is false, own being a list of the caller's own
// bindings.
func (f *fakeIAM) member(b *iamBinding, own bool) string {
	if b.serviceAccount != "" {
		return "serviceAccount:" + b.serviceAccount
	}
	u := f.users[b.user]
	switch {
	case u.deleted:
		return "deleted:user:" + b.user
	case !u.verified || own:
		return "userId:" + b.user
	default:
		return "user:" + u.email
	}
}

func (f *fakeIAM) bindingMessage(b *iamBinding, own bool) *iamv1beta1.RoleBinding {
	creator := b.creator
	if own && strings.HasPrefix(creator, "user:") {
		creator = "userId:" + iamCaller
	}
	return &iamv1beta1.RoleBinding{
		Name: b.name, Urn: "urn:origincloud:iam:role-binding/" + b.name,
		Member: f.member(b, own), Role: b.role, Project: b.project, Creator: creator,
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
	if !slices.ContainsFunc(iamRoles, func(r *iamv1beta1.Role) bool { return r.GetName() == in.GetRole() }) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no role "+in.GetRole())
		return
	}
	b := &iamBinding{role: in.GetRole(), project: in.GetProject(), creator: "user:me@example.com"}
	switch kind, value, _ := strings.Cut(in.GetMember(), ":"); kind {
	case "user":
		for id, u := range f.users {
			if !u.deleted && u.verified && strings.EqualFold(u.email, value) {
				b.user = id
			}
		}
		if b.user == "" {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no account has the verified address "+value)
			return
		}
	case "serviceAccount":
		a, ok := f.serviceAccounts["organizations/"+organization+"/serviceAccounts/"+value]
		if !ok || a.GetState() == iamv1beta1.ServiceAccount_DELETED {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", in.GetMember()+" isn't an active service account of organizations/"+organization)
			return
		}
		b.serviceAccount = value
	default:
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a member is user:{email} or serviceAccount:{service_account}, not "+in.GetMember())
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

func (f *fakeIAM) binding(w http.ResponseWriter, r *http.Request, name, requestID string) {
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
	case http.MethodDelete:
		organization := strings.Split(name, "/")[1]
		if b.role == "roles/owner" && b.project == "" && f.owners(organization) == 1 {
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
		if strings.HasPrefix(b.name, "organizations/"+organization+"/") && b.role == "roles/owner" && b.project == "" {
			n++
		}
	}
	return n
}

func (f *fakeIAM) createServiceAccount(w http.ResponseWriter, organization, requestID string, body []byte) {
	if name, ok := f.created[requestID]; ok && requestID != "" {
		writeReplayedCreate(w, name)
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
	a.Member = "serviceAccount:" + id
	a.ClientId = "client-" + id
	a.State = iamv1beta1.ServiceAccount_ACTIVE
	a.Creator = "user:me@example.com"
	a.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	f.serviceAccounts[a.Name] = a
	f.created[requestID] = a.Name
	f.audit(organization, "", "iam.service_account.create", a.Member, map[string]string{"display_name": a.GetDisplayName()})
	created := proto.Clone(a).(*iamv1beta1.ServiceAccount)
	created.ClientSecret = "secret-" + id
	f.secrets[a.Name] = created.ClientSecret
	if f.loseCreateAnswer {
		f.loseCreateAnswer = false
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
		return
	}
	writeMessage(w, created)
}

// writeReplayedCreate refuses a service account create whose request id
// already created account, as the API does.
func writeReplayedCreate(w http.ResponseWriter, account string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code":   http.StatusConflict,
		"status": "FAILED_PRECONDITION",
		"message": "the service account " + account + " was created by an earlier request with this request_id; " +
			"its client secret was returned only to that request. To get a working secret, delete that account and create a new one",
		"details": []map[string]any{
			{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "CONFLICT", "domain": "origincloudapis.eu",
				"metadata": map[string]string{"serviceAccount": account}},
			{"@type": "type.googleapis.com/google.rpc.RequestInfo", "requestId": "fake-request"},
		},
	}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write(body)
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
			id := strings.TrimPrefix(a.GetMember(), "serviceAccount:")
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

// audit records a change in the organization's audit log.
func (f *fakeIAM) audit(organization, project, action, target string, details map[string]string) {
	f.auditLog = append(f.auditLog, &iamv1beta1.AuditLogEntry{
		Name:    fmt.Sprintf("organizations/%s/auditLogEntries/e-%d", organization, len(f.auditLog)+1),
		Project: project, Actor: "user:me@example.com", Action: action, Target: target,
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
	f.bindings[name] = &iamBinding{name: name, role: role, creator: "user:me@example.com", user: user}
	return name
}
