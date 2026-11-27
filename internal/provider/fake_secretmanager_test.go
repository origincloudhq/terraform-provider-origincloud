package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	secretmanagerv1beta1 "go.origincloud.eu/sdk/secretmanager/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const secretRegion = "projects/p-1/regions/eu-central1"

var secretIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// fakeSecretManager serves the Secret Manager's REST API from memory, in the
// region eu-central1 of the project p-1. Its mutations answer synchronously
// with the secret, as the API's do; its lists answer one item per page. A
// secret is created ACTIVE. DeleteSecret leaves it DELETED, for a recovery
// window of 30 days, or 7 to 30 with pendingDays, during which its id stays
// taken: a create with it answers CONFLICT. With force it destroys the
// secret at once and frees its id, and refuses pendingDays beside it.
// RestoreSecret brings a deleted secret back. UpdateSecret changes its tags
// alone, and refuses a deleted secret.
type fakeSecretManager struct {
	*httptest.Server

	mu      sync.Mutex
	secrets map[string]*secretmanagerv1beta1.Secret
	// calls are the mutations received, such as
	// "DELETE projects/p-1/regions/eu-central1/secrets/db?force=true".
	calls []string
}

func newFakeSecretManager(t *testing.T) *fakeSecretManager {
	f := &fakeSecretManager{secrets: map[string]*secretmanagerv1beta1.Secret{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSecretManager) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// has says that the secret name exists, deleted or not.
func (f *fakeSecretManager) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.secrets[name]
	return ok
}

// state returns the state of the secret name.
func (f *fakeSecretManager) state(name string) secretmanagerv1beta1.Secret_State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.secrets[name].GetState()
}

// seedSecret creates a secret outside Terraform, and returns its name.
func (f *fakeSecretManager) seedSecret(id string, tags map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &secretmanagerv1beta1.Secret{
		Name:       secretRegion + "/secrets/" + id,
		State:      secretmanagerv1beta1.Secret_ACTIVE,
		Tags:       tags,
		CreateTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)),
	}
	s.UpdateTime = s.CreateTime
	f.secrets[s.Name] = s
	return s.Name
}

func (f *fakeSecretManager) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	s := strings.Split(path, "/")
	if r.Method != http.MethodGet {
		call := r.Method + " " + path
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			call += " " + mask
		}
		if force := r.URL.Query().Get("force"); force != "" {
			call += "?force=" + force
		}
		f.calls = append(f.calls, call)
	}
	body, _ := io.ReadAll(r.Body)

	// projects/{p}/regions/{r}/secrets...
	if len(s) < 5 || s[0] != "projects" || s[2] != "regions" || s[4] != "secrets" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
		return
	}
	switch {
	case len(s) == 5 && r.Method == http.MethodGet:
		show := r.URL.Query().Get("showDeleted") == "true"
		items, next := page(f.secrets, path+"/", r, func(p *secretmanagerv1beta1.Secret) bool {
			return show || p.GetState() != secretmanagerv1beta1.Secret_DELETED
		})
		writeMessage(w, &secretmanagerv1beta1.ListSecretsResponse{Secrets: items, NextPageToken: next})
	case len(s) == 5 && r.Method == http.MethodPost:
		f.create(w, strings.Join(s[:4], "/"), r.URL.Query().Get("secretId"), body)
	case len(s) == 6 && strings.HasSuffix(s[5], ":restore") && r.Method == http.MethodPost:
		f.restore(w, strings.TrimSuffix(path, ":restore"))
	case len(s) == 6:
		f.secret(w, r, path, body)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeSecretManager) create(w http.ResponseWriter, parent, id string, body []byte) {
	secret := new(secretmanagerv1beta1.Secret)
	if err := protojson.Unmarshal(body, secret); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if !secretIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("the secret id %q isn't 1 to 63 lowercase letters, digits and hyphens, starting with a letter", id))
		return
	}
	name := parent + "/secrets/" + id
	if existing, ok := f.secrets[name]; ok {
		message := name + " exists already"
		if existing.GetState() == secretmanagerv1beta1.Secret_DELETED {
			message = name + " is deleted, within its recovery window: restore it with RestoreSecret, or delete it with force first"
		}
		writeError(w, http.StatusConflict, "CONFLICT", message)
		return
	}
	secret.Name = name
	secret.State = secretmanagerv1beta1.Secret_ACTIVE
	secret.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	secret.UpdateTime = secret.CreateTime
	f.secrets[name] = secret
	writeMessage(w, secret)
}

func (f *fakeSecretManager) secret(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	secret, ok := f.secrets[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, secret)
	case http.MethodPatch:
		if secret.GetState() == secretmanagerv1beta1.Secret_DELETED {
			writeError(w, http.StatusConflict, "CONFLICT", name+" is deleted: restore it first")
			return
		}
		if err := updateMasked(secret, body, r.URL.Query().Get("updateMask"), "tags"); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		secret.UpdateTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
		writeMessage(w, secret)
	case http.MethodDelete:
		f.delete(w, r, name, secret)
	default:
		writeError(w, http.StatusMethodNotAllowed, "BAD_REQUEST", r.Method+" "+name+" isn't served")
	}
}

func (f *fakeSecretManager) delete(w http.ResponseWriter, r *http.Request, name string, secret *secretmanagerv1beta1.Secret) {
	query := r.URL.Query()
	force := query.Get("force") == "true"
	if days := query.Get("pendingDays"); days != "" {
		var n int
		if _, err := fmt.Sscan(days, &n); err != nil || n < 7 || n > 30 || force {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "pending_days is 7 to 30, and can't go with force")
			return
		}
	}
	if force {
		delete(f.secrets, name)
		secret.State = secretmanagerv1beta1.Secret_DELETED
		writeMessage(w, secret)
		return
	}
	// Deleting a deleted secret succeeds and changes nothing.
	if secret.GetState() != secretmanagerv1beta1.Secret_DELETED {
		secret.State = secretmanagerv1beta1.Secret_DELETED
		secret.DeleteTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
		secret.ExpireTime = timestamppb.New(time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC))
	}
	writeMessage(w, secret)
}

func (f *fakeSecretManager) restore(w http.ResponseWriter, name string) {
	secret, ok := f.secrets[name]
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
	case secret.GetState() != secretmanagerv1beta1.Secret_DELETED:
		writeError(w, http.StatusConflict, "CONFLICT", name+" isn't deleted")
	default:
		secret.State = secretmanagerv1beta1.Secret_ACTIVE
		secret.DeleteTime, secret.ExpireTime = nil, nil
		writeMessage(w, secret)
	}
}

// softDelete deletes the secret name outside Terraform, with a recovery window.
func (f *fakeSecretManager) softDelete(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	secret := f.secrets[name]
	secret.State = secretmanagerv1beta1.Secret_DELETED
	secret.DeleteTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	secret.ExpireTime = timestamppb.New(time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC))
}
