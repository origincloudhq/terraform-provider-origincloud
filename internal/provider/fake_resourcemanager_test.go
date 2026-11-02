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

	resourcemanagerv1beta1 "go.origincloud.eu/sdk/resourcemanager/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeResourceManager serves the resource manager's REST API from
// memory. Its mutations answer synchronously, with the resource or
// nothing, as the API's do; its lists answer one item per page. A project
// id is never used again, not even after the project is deleted. A project
// refuses to be deleted while it holds resources, which hold adds, and an
// organization while it has a project.
type fakeResourceManager struct {
	*httptest.Server

	mu            sync.Mutex
	next          int
	organizations map[string]*resourcemanagerv1beta1.Organization
	projects      map[string]*resourcemanagerv1beta1.Project
	quotas        map[string]*resourcemanagerv1beta1.ProjectQuota
	// used are the project ids ever created.
	used map[string]bool
	// holdings are the kinds of resource each project holds.
	holdings map[string][]string
	// calls are the mutations received, such as "DELETE projects/acme-prod",
	// with their update masks, such as "PATCH projects/acme-prod/quota maxVcpu".
	calls []string
}

func newFakeResourceManager(t *testing.T) *fakeResourceManager {
	f := &fakeResourceManager{
		organizations: map[string]*resourcemanagerv1beta1.Organization{},
		projects:      map[string]*resourcemanagerv1beta1.Project{},
		quotas:        map[string]*resourcemanagerv1beta1.ProjectQuota{},
		used:          map[string]bool{},
		holdings:      map[string][]string{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeResourceManager) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// hold makes the project named project hold resources of the kinds given,
// or none.
func (f *fakeResourceManager) hold(project string, kinds ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holdings[project] = kinds
}

func (f *fakeResourceManager) serve(w http.ResponseWriter, r *http.Request) {
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
	case path == "organizations" && r.Method == http.MethodGet:
		items, next := page(f.organizations, "organizations/", r, func(*resourcemanagerv1beta1.Organization) bool { return true })
		writeMessage(w, &resourcemanagerv1beta1.ListOrganizationsResponse{Organizations: items, NextPageToken: next})
	case path == "organizations" && r.Method == http.MethodPost:
		f.createOrganization(w, body)
	case len(s) == 2 && s[0] == "organizations" && r.Method == http.MethodDelete:
		for _, p := range f.projects {
			if p.GetOrganization() == path {
				writeError(w, http.StatusConflict, "CONFLICT", path+" still has a project: delete its projects first")
				return
			}
		}
		serveSynchronous(w, r, path, body, f.organizations)
	case len(s) == 2 && s[0] == "organizations":
		serveSynchronous(w, r, path, body, f.organizations, "displayName")
	case len(s) == 3 && s[0] == "organizations" && s[2] == "quota" && r.Method == http.MethodGet:
		f.organizationQuota(w, strings.TrimSuffix(path, "/quota"))
	case path == "projects" && r.Method == http.MethodGet:
		organization := r.URL.Query().Get("organization")
		items, next := page(f.projects, "projects/", r, func(p *resourcemanagerv1beta1.Project) bool { return p.GetOrganization() == organization })
		writeMessage(w, &resourcemanagerv1beta1.ListProjectsResponse{Projects: items, NextPageToken: next})
	case path == "projects" && r.Method == http.MethodPost:
		f.createProject(w, r.URL.Query().Get("projectId"), body)
	case len(s) == 2 && s[0] == "projects" && r.Method == http.MethodDelete:
		f.deleteProject(w, path)
	case len(s) == 2 && s[0] == "projects":
		serveSynchronous(w, r, path, body, f.projects, "displayName")
	case len(s) == 3 && s[0] == "projects" && s[2] == "quota" && r.Method != http.MethodDelete && r.Method != http.MethodPost:
		serveSynchronous(w, r, path, body, f.quotas, "maxInstances", "maxVcpu", "maxVolumeGib", "maxVpcs", "maxPublicIps")
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeResourceManager) createOrganization(w http.ResponseWriter, body []byte) {
	organization := new(resourcemanagerv1beta1.Organization)
	if err := protojson.Unmarshal(body, organization); err != nil || organization.GetDisplayName() == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "an organization needs a display name")
		return
	}
	f.next++
	organization.Name = fmt.Sprintf("organizations/org-%d", f.next)
	organization.Urn = "urn:origincloud:resourcemanager:organization/" + organization.Name
	organization.Creator = "user:test"
	organization.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	organization.State = resourcemanagerv1beta1.Organization_ACTIVE
	f.organizations[organization.Name] = organization
	writeMessage(w, organization)
}

func (f *fakeResourceManager) createProject(w http.ResponseWriter, id string, body []byte) {
	project := new(resourcemanagerv1beta1.Project)
	if err := protojson.Unmarshal(body, project); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if organization, ok := f.organizations[project.GetOrganization()]; !ok || organization.GetState() != resourcemanagerv1beta1.Organization_ACTIVE {
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", project.GetOrganization()+" isn't an ACTIVE organization")
		return
	}
	if f.used[id] {
		writeError(w, http.StatusConflict, "CONFLICT", "the project id "+id+" is used already")
		return
	}
	f.used[id] = true
	project.Name = "projects/" + id
	project.Urn = "urn:origincloud:resourcemanager:project/" + project.Name
	project.Creator = "user:test"
	project.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	f.projects[project.Name] = project
	// A project's quota exists with it, its instances counted.
	f.quotas[project.Name+"/quota"] = &resourcemanagerv1beta1.ProjectQuota{
		Name:  project.Name + "/quota",
		Usage: &resourcemanagerv1beta1.QuotaUsage{Instances: 1, Vcpu: 2, VolumeGib: 10},
	}
	writeMessage(w, project)
}

func (f *fakeResourceManager) deleteProject(w http.ResponseWriter, name string) {
	if _, ok := f.projects[name]; !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	if kinds := f.holdings[name]; len(kinds) > 0 {
		writeError(w, http.StatusConflict, "CONFLICT", name+" still has resources; delete them first, in this order: "+strings.Join(kinds, ", "))
		return
	}
	delete(f.projects, name)
	delete(f.quotas, name+"/quota")
	writeMessage(w, &emptypb.Empty{})
}

// organizationQuota answers with the quota of the organization named
// organization, its usage summed over its projects'.
func (f *fakeResourceManager) organizationQuota(w http.ResponseWriter, organization string) {
	if _, ok := f.organizations[organization]; !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", organization+" doesn't exist")
		return
	}
	quota := &resourcemanagerv1beta1.OrganizationQuota{
		Name: organization + "/quota", MaxInstances: 20, MaxVcpu: 64, MaxVolumeGib: 2048, MaxVpcs: 5, MaxPublicIps: 4,
		Usage: &resourcemanagerv1beta1.QuotaUsage{},
	}
	for name, project := range f.projects {
		if project.GetOrganization() == organization {
			usage := f.quotas[name+"/quota"].GetUsage()
			quota.Usage.Instances += usage.GetInstances()
			quota.Usage.Vcpu += usage.GetVcpu()
			quota.Usage.VolumeGib += usage.GetVolumeGib()
		}
	}
	writeMessage(w, quota)
}
