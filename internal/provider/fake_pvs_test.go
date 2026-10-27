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

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	longrunningv1beta1 "go.origincloud.eu/sdk/longrunning/v1beta1"
	pvsv1beta1 "go.origincloud.eu/sdk/pvs/v1beta1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakePVS serves PVS's REST API from memory, answering every mutation
// with an operation that is already done. A volume whose tags hold
// fail=create is created in ERROR, its operation failed.
type fakePVS struct {
	*httptest.Server

	mu        sync.Mutex
	next      int
	volumes   map[string]*pvsv1beta1.Volume
	snapshots map[string]*pvsv1beta1.Snapshot
	// calls are the mutations received, such as "DELETE projects/p/zones/z/volumes/vol-1".
	calls []string
}

var storageClasses = map[string]*pvsv1beta1.StorageClass{
	"storageClasses/gp1": {Name: "storageClasses/gp1", DisplayName: "General purpose SSD", Media: pvsv1beta1.StorageClass_SSD, DefaultClass: true, Available: true, BaselineIops: 3000},
	"storageClasses/st1": {Name: "storageClasses/st1", DisplayName: "Throughput HDD", Media: pvsv1beta1.StorageClass_HDD, Available: true},
}

func newFakePVS(t *testing.T) *fakePVS {
	f := &fakePVS{volumes: map[string]*pvsv1beta1.Volume{}, snapshots: map[string]*pvsv1beta1.Snapshot{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePVS) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakePVS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	segments := strings.Split(path, "/")
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+path)
	}
	body, _ := io.ReadAll(r.Body)

	switch {
	case len(segments) == 2 && segments[0] == "storageClasses":
		class, ok := storageClasses[path]
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", path+" doesn't exist")
			return
		}
		writeMessage(w, class)
	case len(segments) == 5 && segments[4] == "volumes" && r.Method == http.MethodPost:
		f.createVolume(w, strings.TrimSuffix(path, "/volumes"), body)
	case len(segments) == 6 && segments[4] == "volumes":
		f.volume(w, r, path, body)
	case len(segments) == 5 && segments[4] == "snapshots" && r.Method == http.MethodPost:
		f.createSnapshot(w, strings.TrimSuffix(path, "/snapshots"), body)
	case len(segments) == 6 && segments[4] == "snapshots":
		f.snapshot(w, r, path, body)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakePVS) createVolume(w http.ResponseWriter, parent string, body []byte) {
	volume := new(pvsv1beta1.Volume)
	if err := protojson.Unmarshal(body, volume); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if volume.GetSizeGib() < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "size_gib must be at least 1")
		return
	}
	if volume.StorageClass == "" {
		volume.StorageClass = "storageClasses/gp1"
	}
	f.next++
	volume.Name = fmt.Sprintf("%s/volumes/vol-%d", parent, f.next)
	volume.Urn = "urn:origincloud:pvs:volume/" + volume.Name
	volume.Creator = "user:test"
	volume.State = pvsv1beta1.Volume_AVAILABLE
	volume.ResizeState = pvsv1beta1.Volume_RESIZE_COMPLETED
	f.volumes[volume.Name] = volume
	if volume.GetTags()["fail"] == "create" {
		volume.State = pvsv1beta1.Volume_ERROR
		volume.StateReason = "the storage couldn't be created"
		writeOperation(w, volume.Name, nil, volume.StateReason)
		return
	}
	writeOperation(w, volume.Name, volume, "")
}

func (f *fakePVS) volume(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	volume, ok := f.volumes[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, volume)
	case http.MethodPatch:
		update := new(pvsv1beta1.Volume)
		if err := protojson.Unmarshal(body, update); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			switch field {
			case "sizeGib":
				if update.GetSizeGib() < volume.GetSizeGib() {
					writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a volume never shrinks")
					return
				}
				volume.SizeGib = update.GetSizeGib()
			case "tags":
				volume.Tags = update.GetTags()
			default:
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", field+" can't be updated")
				return
			}
		}
		writeOperation(w, name, volume, "")
	case http.MethodDelete:
		volume.State = pvsv1beta1.Volume_DELETED
		writeOperation(w, name, &emptypb.Empty{}, "")
	}
}

func (f *fakePVS) createSnapshot(w http.ResponseWriter, parent string, body []byte) {
	snapshot := new(pvsv1beta1.Snapshot)
	if err := protojson.Unmarshal(body, snapshot); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	source, ok := f.volumes[snapshot.GetSourceVolume()]
	if !ok || source.GetState() == pvsv1beta1.Volume_DELETED {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", snapshot.GetSourceVolume()+" doesn't exist")
		return
	}
	f.next++
	snapshot.Name = fmt.Sprintf("%s/snapshots/snap-%d", parent, f.next)
	snapshot.Urn = "urn:origincloud:pvs:snapshot/" + snapshot.Name
	snapshot.Creator = "user:test"
	snapshot.SourceSizeGib = source.GetSizeGib()
	snapshot.State = pvsv1beta1.Snapshot_COMPLETED
	snapshot.ProtectionState = pvsv1beta1.Snapshot_UNPROTECTED
	f.snapshots[snapshot.Name] = snapshot
	writeOperation(w, snapshot.Name, snapshot, "")
}

func (f *fakePVS) snapshot(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	snapshot, ok := f.snapshots[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, snapshot)
	case http.MethodPatch:
		update := new(pvsv1beta1.Snapshot)
		if err := protojson.Unmarshal(body, update); err != nil || r.URL.Query().Get("updateMask") != "tags" {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "only tags can be updated")
			return
		}
		snapshot.Tags = update.GetTags()
		writeMessage(w, snapshot)
	case http.MethodDelete:
		snapshot.State = pvsv1beta1.Snapshot_DELETED
		writeOperation(w, name, &emptypb.Empty{}, "")
	}
}

// writeOperation answers with a done operation on target, whose result is
// response, or which failed with failure when that isn't empty.
func writeOperation(w http.ResponseWriter, target string, response proto.Message, failure string) {
	metadata, _ := anypb.New(&longrunningv1beta1.OperationMetadata{Target: target})
	op := &longrunningpb.Operation{Name: target + "/operations/op", Done: true, Metadata: metadata}
	if failure != "" {
		op.Result = &longrunningpb.Operation_Error{Error: &statuspb.Status{Code: 13, Message: failure}}
	} else {
		result, _ := anypb.New(response)
		op.Result = &longrunningpb.Operation_Response{Response: result}
	}
	writeMessage(w, op)
}

func writeMessage(w http.ResponseWriter, m proto.Message) {
	data, _ := protojson.Marshal(m)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"status":%q,"message":%q}}`, code, status, message)
}
