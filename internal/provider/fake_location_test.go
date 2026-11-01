package provider

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	locationv1beta1 "go.origincloud.eu/sdk/location/v1beta1"
)

// The fake location service's catalog: two regions, one of whose zones
// offers no service yet.
var (
	regions = []*locationv1beta1.Region{
		{Name: "regions/eu-central1", DisplayName: "EU Central 1", Zones: []string{"zones/eu-central1-az1", "zones/eu-central1-az2"}},
		{Name: "regions/eu-west1", DisplayName: "EU West 1", Zones: []string{"zones/eu-west1-az1"}},
	}
	zones = []*locationv1beta1.Zone{
		{Name: "zones/eu-central1-az1", DisplayName: "EU Central 1, zone 1", Region: "regions/eu-central1", Services: []string{"pvs"}},
		{Name: "zones/eu-central1-az2", DisplayName: "EU Central 1, zone 2", Region: "regions/eu-central1", Services: []string{"pvs"}},
		{Name: "zones/eu-west1-az1", DisplayName: "EU West 1, zone 1", Region: "regions/eu-west1"},
	}
)

// newFakeLocation serves the location service's REST API.
func newFakeLocation(t *testing.T) *httptest.Server {
	f := httptest.NewServer(http.HandlerFunc(serveLocation))
	t.Cleanup(f.Close)
	return f
}

func serveLocation(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	switch {
	case r.Method != http.MethodGet:
	case path == "regions":
		writeMessage(w, &locationv1beta1.ListRegionsResponse{Regions: regions})
		return
	case path == "zones":
		writeMessage(w, &locationv1beta1.ListZonesResponse{Zones: zones})
		return
	case strings.HasPrefix(path, "regions/"):
		for _, region := range regions {
			if region.GetName() == path {
				writeMessage(w, region)
				return
			}
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", path+" doesn't exist")
		return
	case strings.HasPrefix(path, "zones/"):
		for _, zone := range zones {
			if zone.GetName() == path {
				writeMessage(w, zone)
				return
			}
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", path+" doesn't exist")
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
}

// hosts is an HTTP transport that sends each call to the test server
// serving its host, and records the hosts called.
type hosts struct {
	servers map[string]*httptest.Server

	mu     sync.Mutex
	called []string
}

func (h *hosts) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.called = append(h.called, r.URL.Host+" "+r.URL.Path)
	h.mu.Unlock()
	server, ok := h.servers[r.URL.Host]
	if !ok {
		rec := httptest.NewRecorder()
		writeError(rec, http.StatusBadGateway, "UNAVAILABLE", r.URL.Host+" isn't served")
		return rec.Result(), nil
	}
	target, _ := url.Parse(server.URL)
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func (h *hosts) calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.called...)
}
