package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/cmd/protoc-gen-go/internal_gengo"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

var update = flag.Bool("update", false, "write the test API's generated code")

// The repository's root, from this package's directory.
const root = "../../.."

// testAPIFiles generates the test API (internal/testapi/firewalls.prototext):
// its protobuf messages, as protoc-gen-go writes them, and the provider's
// code for it, as this plugin does. It returns the files by their path in
// the repository.
func testAPIFiles(t *testing.T) map[string][]byte {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, "internal/testapi/firewalls.prototext"))
	if err != nil {
		t.Fatal(err)
	}
	file := new(descriptorpb.FileDescriptorProto)
	if err := prototext.Unmarshal(source, file); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	add := func(resp *pluginpb.CodeGeneratorResponse, prefix string) {
		if resp.Error != nil {
			t.Fatal(resp.GetError())
		}
		for _, f := range resp.GetFile() {
			files[strings.TrimPrefix(f.GetName(), prefix)] = []byte(f.GetContent())
		}
	}

	messages, err := protogen.Options{}.New(request(t, file))
	if err != nil {
		t.Fatal(err)
	}
	messages.SupportedFeatures = internal_gengo.SupportedFeatures
	internal_gengo.GenerateFile(messages, messages.FilesByPath[file.GetName()])
	add(messages.Response(), module+"/")

	savedServices, savedProvider, savedToggles := servicesDir, providerDir, toggles
	servicesDir, providerDir, toggles = "internal/testapi/services", "internal/testapi", nil
	t.Cleanup(func() { servicesDir, providerDir, toggles = savedServices, savedProvider, savedToggles })
	plugin, err := protogen.Options{}.New(request(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if err := generate(plugin); err != nil {
		t.Fatal(err)
	}
	add(plugin.Response(), "")
	return files
}

// TestTestAPIIsCurrent checks that the test API's generated code is what
// the plugin generates; -update writes it.
func TestTestAPIIsCurrent(t *testing.T) {
	files := testAPIFiles(t)
	var stale []string
	for name, content := range files {
		path := filepath.Join(root, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, content) {
			stale = append(stale, name)
		}
	}
	// A file the plugin no longer writes.
	err := filepath.WalkDir(filepath.Join(root, "internal/testapi"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".gen.go") && !strings.HasSuffix(path, ".pb.go") {
			return err
		}
		name, _ := filepath.Rel(root, path)
		if _, ok := files[filepath.ToSlash(name)]; !ok {
			if *update {
				return os.Remove(path)
			}
			stale = append(stale, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Errorf("not what the plugin generates: %s; run go test ./internal/cmd/protoc-gen-terraform-origincloud -run TestTestAPI -update", strings.Join(stale, ", "))
	}
}
