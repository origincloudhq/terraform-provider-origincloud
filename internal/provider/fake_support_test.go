package provider

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	supportv1beta1 "go.origincloud.eu/sdk/support/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
)

// The fake support service's cases, as it answers them: acme's, one about
// its project acme-prod with a conversation, one about the organization as
// a whole; and another organization's.
var (
	supportCases = map[string]*supportv1beta1.Case{}
	caseMessages = map[string]*supportv1beta1.CaseMessage{}
)

func init() {
	for _, c := range []string{
		`{"name":"organizations/acme/cases/c-1","urn":"urn:origincloud:support:case/organizations/acme/cases/c-1","project":"projects/acme-prod",` +
			`"subject":"Instance unreachable","description":"i-1 doesn't answer on port 22.","contactEmail":"ops@acme.example","state":"IN_PROGRESS",` +
			`"creator":"user:test","createTime":"2026-11-01T12:00:00Z","updateTime":"2026-11-01T13:30:00Z"}`,
		`{"name":"organizations/acme/cases/c-2","subject":"Invoice address","description":"Please explain the reverse charge.","contactEmail":"finance@acme.example",` +
			`"state":"OPEN","creator":"user:test","createTime":"2026-11-02T08:00:00Z","updateTime":"2026-11-02T08:00:00Z"}`,
		`{"name":"organizations/other/cases/c-3","subject":"Someone else's","description":"Not acme's.","contactEmail":"x@other.example","state":"CLOSED"}`,
	} {
		p := new(supportv1beta1.Case)
		if err := protojson.Unmarshal([]byte(c), p); err != nil {
			panic(err)
		}
		supportCases[p.GetName()] = p
	}
	for _, m := range []string{
		`{"name":"organizations/acme/cases/c-1/messages/m-1","body":"i-1 doesn't answer on port 22.","author":"user:test","createTime":"2026-11-01T12:00:00Z"}`,
		`{"name":"organizations/acme/cases/c-1/messages/m-2","body":"Its security group allows no SSH.","author":"user:support-1","createTime":"2026-11-01T13:30:00Z"}`,
	} {
		p := new(supportv1beta1.CaseMessage)
		if err := protojson.Unmarshal([]byte(m), p); err != nil {
			panic(err)
		}
		caseMessages[p.GetName()] = p
	}
}

// newFakeSupport serves the support service's REST API's reads; its lists
// answer one item per page.
func newFakeSupport(t *testing.T) *httptest.Server {
	f := httptest.NewServer(http.HandlerFunc(serveSupport))
	t.Cleanup(f.Close)
	return f
}

func serveSupport(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	s := strings.Split(path, "/")
	switch {
	case r.Method != http.MethodGet:
	case len(s) == 3 && s[0] == "organizations" && s[2] == "cases":
		// Newest first, as the API lists them: keyed so that sorting by
		// key orders them so.
		newest := map[string]*supportv1beta1.Case{}
		for name, c := range supportCases {
			if strings.HasPrefix(name, path+"/") {
				newest[strconv.FormatInt(1e10-c.GetCreateTime().GetSeconds(), 10)] = c
			}
		}
		items, next := page(newest, "", r, func(*supportv1beta1.Case) bool { return true })
		writeMessage(w, &supportv1beta1.ListCasesResponse{Cases: items, NextPageToken: next})
		return
	case len(s) == 4 && s[2] == "cases":
		serveSynchronous(w, r, path, nil, supportCases)
		return
	case len(s) == 5 && s[2] == "cases" && s[4] == "messages":
		items, next := page(caseMessages, path+"/", r, func(*supportv1beta1.CaseMessage) bool { return true })
		writeMessage(w, &supportv1beta1.ListCaseMessagesResponse{CaseMessages: items, NextPageToken: next})
		return
	case len(s) == 6 && s[2] == "cases" && s[4] == "messages":
		serveSynchronous(w, r, path, nil, caseMessages)
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
}
