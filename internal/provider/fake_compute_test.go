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

	computev1beta1 "go.origincloud.eu/sdk/compute/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeCompute serves the compute service's REST API from memory,
// answering every mutation that takes an operation with one that is
// already done. Its lists answer one item per page, and list terminated
// instances too, as the API does for a while. An instance's type changes
// only while it is STOPPED, which stop brings it to, as a user would
// outside Terraform. A key pair created without a public key is
// generated, its private key in the create's response only. A launch
// template keeps numbered versions, the highest number it has ever given
// one past the highest; a version is read as `{name}@{n}`, `@default` or
// `@latest`, and the template read at one is named with its number.
type fakeCompute struct {
	*httptest.Server

	mu          sync.Mutex
	next        int
	keyPairs    map[string]*computev1beta1.KeyPair
	images      map[string]*computev1beta1.Image
	instances   map[string]*computev1beta1.Instance
	attachments map[string]*computev1beta1.VolumeAttachment
	// templates are the launch templates, by name.
	templates map[string]*fakeTemplate
	// privateKeys are the private keys generated, by key pair.
	privateKeys map[string]string
	// calls are the mutations received, such as "DELETE projects/p/keyPairs/kp-1".
	calls []string
}

// The fake compute service's catalog: one instance type no longer
// offered, and two public images of a family, the second its current.
var (
	computeInstanceTypes = []*computev1beta1.InstanceType{
		{Name: "instanceTypes/gp1.core.2", DisplayName: "General purpose, 2 cores", Kind: "gp1", CpuAllocationMode: "core", Vcpu: 2, MemoryMib: 8192, StorageBandwidthMbit: 1000, Available: true},
		{Name: "instanceTypes/gp1.core.4", DisplayName: "General purpose, 4 cores", Kind: "gp1", CpuAllocationMode: "core", Vcpu: 4, MemoryMib: 16384, StorageBandwidthMbit: 2000, StorageBurstBandwidthMbit: 4000, Available: true},
		{Name: "instanceTypes/gp0.shared.1", DisplayName: "Shared, 1 vCPU", Kind: "gp0", CpuAllocationMode: "shared", Vcpu: 1, MemoryMib: 2048},
	}
	computeImageFamilies = []*computev1beta1.ImageFamily{
		{Name: "imageFamilies/ubuntu-24-04-minimal", DisplayName: "Ubuntu 24.04 minimal", Vendor: "Canonical", Os: "ubuntu", OsVersion: "24.04", SupportEndTime: timestamppb.New(time.Date(2029, 5, 31, 0, 0, 0, 0, time.UTC)), Available: true},
		{Name: "imageFamilies/ubuntu-22-04-minimal", DisplayName: "Ubuntu 22.04 minimal", Vendor: "Canonical", Os: "ubuntu", OsVersion: "22.04"},
	}
	computePublicImages = []*computev1beta1.PublicImage{
		computePublicImage("ubuntu-24-04-v1", false),
		computePublicImage("ubuntu-24-04-v2", true),
	}
)

func computePublicImage(id string, current bool) *computev1beta1.PublicImage {
	name := "zones/eu-central1-az1/images/" + id
	return &computev1beta1.PublicImage{
		Name: name, Description: "Ubuntu 24.04 minimal",
		DefaultUser: "ubuntu", RootDevice: "vda", BlockDevices: []*computev1beta1.PublicImageBlockDevice{{Device: "vda", SizeGib: 10}},
		Architecture: computev1beta1.Image_X86_64, Platform: computev1beta1.Image_LINUX_UNIX, State: computev1beta1.Image_AVAILABLE,
		ImageFamily: "imageFamilies/ubuntu-24-04-minimal", FamilyCurrent: current,
	}
}

func newFakeCompute(t *testing.T) *fakeCompute {
	f := &fakeCompute{
		keyPairs:    map[string]*computev1beta1.KeyPair{},
		images:      map[string]*computev1beta1.Image{},
		instances:   map[string]*computev1beta1.Instance{},
		attachments: map[string]*computev1beta1.VolumeAttachment{},
		templates:   map[string]*fakeTemplate{},
		privateKeys: map[string]string{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCompute) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// stop stops the instance named name.
func (f *fakeCompute) stop(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[name].State = computev1beta1.Instance_STOPPED
}

func (f *fakeCompute) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1beta1/")
	s := strings.Split(path, "/")
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, r.Method+" "+path)
	}
	body, _ := io.ReadAll(r.Body)
	list := r.Method == http.MethodGet
	create := r.Method == http.MethodPost
	all := func(*computev1beta1.Instance) bool { return true }

	switch {
	case path == "instanceTypes" && list:
		writeMessage(w, &computev1beta1.ListInstanceTypesResponse{InstanceTypes: computeInstanceTypes})
	case len(s) == 2 && s[0] == "instanceTypes" && list:
		writeNamed(w, path, computeInstanceTypes)
	case path == "imageFamilies" && list:
		writeMessage(w, &computev1beta1.ListImageFamiliesResponse{ImageFamilies: computeImageFamilies})
	case len(s) == 2 && s[0] == "imageFamilies" && list:
		writeNamed(w, path, computeImageFamilies)
	case len(s) == 3 && s[0] == "zones" && s[2] == "images" && list:
		images := map[string]*computev1beta1.PublicImage{}
		for _, image := range computePublicImages {
			images[image.GetName()] = image
		}
		items, next := page(images, path+"/", r, func(*computev1beta1.PublicImage) bool { return true })
		writeMessage(w, &computev1beta1.ListPublicImagesResponse{PublicImages: items, NextPageToken: next})
	case len(s) == 4 && s[0] == "zones" && s[2] == "images" && list:
		writeNamed(w, path, computePublicImages)
	case len(s) == 3 && s[2] == "keyPairs" && list:
		items, next := page(f.keyPairs, path+"/", r, func(*computev1beta1.KeyPair) bool { return true })
		writeMessage(w, &computev1beta1.ListKeyPairsResponse{KeyPairs: items, NextPageToken: next})
	case len(s) == 3 && s[2] == "keyPairs" && create:
		f.createKeyPair(w, path, body)
	case len(s) == 4 && s[2] == "keyPairs":
		serveSynchronous(w, r, path, body, f.keyPairs, "tags")
	case len(s) == 5 && s[4] == "images" && list:
		items, next := page(f.images, path+"/", r, func(*computev1beta1.Image) bool { return true })
		writeMessage(w, &computev1beta1.ListImagesResponse{Images: items, NextPageToken: next})
	case len(s) == 5 && s[4] == "images" && create:
		f.createImage(w, path, body)
	case len(s) == 6 && s[4] == "images":
		serveSynchronous(w, r, path, body, f.images, "description", "tags")
	case len(s) == 5 && s[4] == "instances" && list:
		items, next := page(f.instances, path+"/", r, all)
		writeMessage(w, &computev1beta1.ListInstancesResponse{Instances: items, NextPageToken: next})
	case len(s) == 5 && s[4] == "instances" && create:
		f.createInstance(w, path, body)
	case len(s) == 5 && s[4] == "instances:launch" && create:
		f.launchInstance(w, strings.TrimSuffix(path, ":launch"), body)
	case len(s) == 3 && s[2] == "launchTemplates" && list:
		items, next := f.listTemplates(path+"/", r)
		writeMessage(w, &computev1beta1.ListLaunchTemplatesResponse{LaunchTemplates: items, NextPageToken: next})
	case len(s) == 3 && s[2] == "launchTemplates" && create:
		f.createTemplate(w, path, body)
	case len(s) == 4 && s[2] == "launchTemplates":
		f.template(w, r, path, body)
	case len(s) == 6 && s[4] == "instances":
		f.instance(w, r, path, body)
	case len(s) == 7 && s[6] == "volumeAttachments" && list:
		// "-" for the instance lists every instance's in the zone.
		prefix := strings.TrimSuffix(strings.TrimSuffix(path, "volumeAttachments"), "-/")
		items, next := page(f.attachments, prefix, r, func(*computev1beta1.VolumeAttachment) bool { return true })
		writeMessage(w, &computev1beta1.ListVolumeAttachmentsResponse{VolumeAttachments: items, NextPageToken: next})
	case len(s) == 7 && s[6] == "volumeAttachments" && create:
		f.attach(w, strings.TrimSuffix(path, "/volumeAttachments"), body)
	case len(s) == 8 && s[6] == "volumeAttachments":
		f.attachment(w, r, path)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeCompute) createKeyPair(w http.ResponseWriter, parent string, body []byte) {
	keyPair := new(computev1beta1.KeyPair)
	if err := protojson.Unmarshal(body, keyPair); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	f.next++
	keyPair.Name = fmt.Sprintf("%s/kp-%d", parent, f.next)
	keyPair.Creator = "user:test"
	f.keyPairs[keyPair.Name] = keyPair
	if keyPair.GetPublicKey() != "" {
		writeMessage(w, keyPair)
		return
	}
	keyPair.PublicKey = strings.TrimSpace(fmt.Sprintf("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA%d %s", f.next, keyPair.GetTags()["origin:name"]))
	generated := proto.Clone(keyPair).(*computev1beta1.KeyPair)
	generated.PrivateKey = fmt.Sprintf("-----BEGIN OPENSSH PRIVATE KEY-----\nkp-%d\n-----END OPENSSH PRIVATE KEY-----\n", f.next)
	f.privateKeys[keyPair.Name] = generated.PrivateKey
	writeMessage(w, generated)
}

func (f *fakeCompute) createImage(w http.ResponseWriter, parent string, body []byte) {
	image := new(computev1beta1.Image)
	if err := protojson.Unmarshal(body, image); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if !slices.ContainsFunc(image.GetBlockDevices(), func(d *computev1beta1.ImageBlockDevice) bool { return d.GetDevice() == image.GetRootDevice() }) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "root_device isn't one of the block devices")
		return
	}
	if image.DefaultUser == "" {
		image.DefaultUser = "cloud"
	}
	f.next++
	image.Name = fmt.Sprintf("%s/img-%d", parent, f.next)
	image.Creator = "user:test"
	image.Architecture = computev1beta1.Image_X86_64
	image.Platform = computev1beta1.Image_LINUX_UNIX
	image.State = computev1beta1.Image_AVAILABLE
	f.images[image.Name] = image
	writeMessage(w, image)
}

func (f *fakeCompute) createInstance(w http.ResponseWriter, parent string, body []byte) {
	instance := new(computev1beta1.Instance)
	if err := protojson.Unmarshal(body, instance); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	f.startInstance(w, parent, instance)
}

// startInstance checks and creates an instance, answering with the
// operation.
func (f *fakeCompute) startInstance(w http.ResponseWriter, parent string, instance *computev1beta1.Instance) {
	if err := f.check(instance); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	// The boot volume's size and the login user default to the image's.
	var defaultUser string
	var size int32
	zone := strings.TrimSuffix(parent[strings.Index(parent, "zones/"):], "/instances")
	for _, image := range computePublicImages {
		family := relativeName(image.GetImageFamily()) == relativeName(instance.GetImageFamily()) && image.GetFamilyCurrent()
		if strings.HasPrefix(image.GetName(), zone+"/") && (full("compute", image.GetName()) == instance.GetImage() || family) {
			instance.Image, defaultUser, size = full("compute", image.GetName()), image.GetDefaultUser(), image.GetBlockDevices()[0].GetSizeGib()
		}
	}
	if image, ok := f.images[relativeName(instance.GetImage())]; ok {
		defaultUser, size = image.GetDefaultUser(), image.GetBlockDevices()[0].GetSizeGib()
	}
	if defaultUser == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "no image "+instance.GetImage()+instance.GetImageFamily()+" in "+zone)
		return
	}
	if instance.LoginUser == "" {
		instance.LoginUser = defaultUser
	}
	if instance.BootVolume == nil {
		instance.BootVolume = &computev1beta1.BootVolume{}
	}
	if instance.BootVolume.SizeGib == 0 {
		instance.BootVolume.SizeGib = size
	}
	if instance.BootVolume.StorageClass == "" {
		instance.BootVolume.StorageClass = "//pvs.origincloudapis.eu/storageClasses/gp1"
	}
	f.next++
	instance.Name = fmt.Sprintf("%s/i-%d", parent, f.next)
	instance.Creator = "user:test"
	instance.State = computev1beta1.Instance_RUNNING
	instance.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	instance.BootVolume.Volume = fmt.Sprintf("%s/volumes/vol-boot-%d", strings.TrimSuffix(parent, "/instances"), f.next)
	instance.Health = &computev1beta1.InstanceHealth{
		Availability: computev1beta1.InstanceHealth_AVAILABLE,
		Checks: []*computev1beta1.HealthCheck{
			{Kind: computev1beta1.HealthCheck_HOST_REACHABLE, Category: computev1beta1.HealthCheck_SYSTEM, Outcome: computev1beta1.HealthCheck_OK, ChangeTime: instance.CreateTime},
			{Kind: computev1beta1.HealthCheck_GUEST_AGENT, Category: computev1beta1.HealthCheck_INSTANCE, Outcome: computev1beta1.HealthCheck_INSUFFICIENT_DATA},
		},
	}
	instance.InterfaceAddresses = []*computev1beta1.InterfaceAddresses{
		{NetworkInterface: instance.GetNetworkInterfaces()[0], Ipv4Address: fmt.Sprintf("10.0.0.%d", f.next), Ipv6Address: fmt.Sprintf("fd00::%d", f.next)},
	}
	f.instances[instance.Name] = instance
	boot := &computev1beta1.VolumeAttachment{Name: instance.Name + "/volumeAttachments/boot", Volume: instance.BootVolume.Volume, Device: "vda", Boot: true, State: computev1beta1.VolumeAttachment_ATTACHED}
	f.attachments[boot.Name] = boot
	writeOperation(w, instance.Name, instance, "")
}

// check checks what an instance refers to.
func (f *fakeCompute) check(instance *computev1beta1.Instance) error {
	if !slices.ContainsFunc(computeInstanceTypes, func(t *computev1beta1.InstanceType) bool {
		return full("compute", t.GetName()) == instance.GetInstanceType() && t.GetAvailable()
	}) {
		return fmt.Errorf("%s isn't an available instance type", instance.GetInstanceType())
	}
	if _, ok := f.keyPairs[relativeName(instance.GetKeyPair())]; !ok {
		return fmt.Errorf("%s doesn't exist", instance.GetKeyPair())
	}
	if (instance.GetImage() == "") == (instance.GetImageFamily() == "") {
		return fmt.Errorf("set exactly one of image and image_family")
	}
	if len(instance.GetNetworkInterfaces()) != 1 {
		return fmt.Errorf("an instance has exactly one network interface")
	}
	return nil
}

func (f *fakeCompute) instance(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	instance, ok := f.instances[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, instance)
	case http.MethodPatch:
		update := new(computev1beta1.Instance)
		if err := protojson.Unmarshal(body, update); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			switch field {
			case "instanceType":
				if instance.GetState() != computev1beta1.Instance_STOPPED {
					writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "a new instance_type needs the instance STOPPED")
					return
				}
				instance.InstanceType = update.GetInstanceType()
			case "tags":
				instance.Tags = update.GetTags()
			default:
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", field+" can't be updated")
				return
			}
		}
		writeOperation(w, name, instance, "")
	case http.MethodDelete:
		// Terminated, it stays listed; its volumes are detached.
		instance.State = computev1beta1.Instance_TERMINATED
		for attachment := range f.attachments {
			if strings.HasPrefix(attachment, name+"/") {
				delete(f.attachments, attachment)
			}
		}
		writeOperation(w, name, &emptypb.Empty{}, "")
	}
}

func (f *fakeCompute) attach(w http.ResponseWriter, instance string, body []byte) {
	attachment := new(computev1beta1.VolumeAttachment)
	if err := protojson.Unmarshal(body, attachment); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	holder, ok := f.instances[instance]
	if !ok || holder.GetState() != computev1beta1.Instance_RUNNING && holder.GetState() != computev1beta1.Instance_STOPPED {
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", instance+" isn't RUNNING or STOPPED")
		return
	}
	// The platform refuses a reference to another project's resource.
	if project := strings.Join(strings.Split(instance, "/")[:2], "/"); !strings.HasPrefix(relativeName(attachment.GetVolume()), project+"/") {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", attachment.GetVolume()+" isn't a volume of the instance's project")
		return
	}
	devices := map[string]bool{}
	for name, a := range f.attachments {
		if a.GetVolume() == attachment.GetVolume() {
			writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", a.GetVolume()+" is attached already")
			return
		}
		if strings.HasPrefix(name, instance+"/") {
			devices[a.GetDevice()] = true
		}
	}
	for d := 'b'; attachment.Device == ""; d++ {
		if !devices["vd"+string(d)] {
			attachment.Device = "vd" + string(d)
		}
	}
	f.next++
	attachment.Name = fmt.Sprintf("%s/volumeAttachments/att-%d", instance, f.next)
	attachment.State = computev1beta1.VolumeAttachment_ATTACHED
	f.attachments[attachment.Name] = attachment
	writeOperation(w, attachment.Name, attachment, "")
}

func (f *fakeCompute) attachment(w http.ResponseWriter, r *http.Request, name string) {
	attachment, ok := f.attachments[name]
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
	case r.Method == http.MethodGet:
		writeMessage(w, attachment)
	case r.Method == http.MethodDelete && attachment.GetBoot():
		writeError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "the boot volume can't be detached")
	case r.Method == http.MethodDelete:
		delete(f.attachments, name)
		writeOperation(w, name, &emptypb.Empty{}, "")
	}
}

// serveSynchronous gets, updates or deletes the resource named name in
// all, answering with it, not an operation. An update may change the
// fields named mutable, by their JSON names; a delete removes it.
func serveSynchronous[P proto.Message](w http.ResponseWriter, r *http.Request, name string, body []byte, all map[string]P, mutable ...string) {
	p, ok := all[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeMessage(w, p)
	case http.MethodPatch:
		if err := updateMasked(p, body, r.URL.Query().Get("updateMask"), mutable...); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		writeMessage(w, p)
	case http.MethodDelete:
		delete(all, name)
		writeMessage(w, &emptypb.Empty{})
	}
}

// updateMasked sets the fields of p that mask names, by their JSON names,
// to body's, or clears them where body has none. Each must be one of
// mutable.
func updateMasked(p proto.Message, body []byte, mask string, mutable ...string) error {
	from := p.ProtoReflect().New().Interface()
	if err := protojson.Unmarshal(body, from); err != nil {
		return err
	}
	for _, field := range strings.Split(mask, ",") {
		fd := p.ProtoReflect().Descriptor().Fields().ByJSONName(field)
		if fd == nil || !slices.Contains(mutable, field) {
			return fmt.Errorf("%s can't be updated", field)
		}
		if from.ProtoReflect().Has(fd) {
			p.ProtoReflect().Set(fd, from.ProtoReflect().Get(fd))
		} else {
			p.ProtoReflect().Clear(fd)
		}
	}
	return nil
}

// writeNamed answers with the one of all named name.
func writeNamed[P interface {
	proto.Message
	GetName() string
}](w http.ResponseWriter, name string, all []P) {
	for _, p := range all {
		if p.GetName() == name {
			writeMessage(w, p)
			return
		}
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
}

// A fakeTemplate is a launch template: its versions' recipes by number, the
// default and the highest number given.
type fakeTemplate struct {
	name        string
	tags        map[string]string
	versions    map[int32]*fakeVersion
	defaultNum  int32
	latestNum   int32
	creator     string
	createTime  *timestamppb.Timestamp
	versionTime int
}

type fakeVersion struct {
	data       *computev1beta1.LaunchTemplateData
	createTime *timestamppb.Timestamp
}

// view returns the template read at version, named with its number when
// named is set, as a read by `@` is.
func (t *fakeTemplate) view(version int32, named bool) *computev1beta1.LaunchTemplate {
	v := t.versions[version]
	name := t.name
	if named {
		name = fmt.Sprintf("%s@%d", t.name, version)
	}
	return &computev1beta1.LaunchTemplate{
		Name: name, LaunchTemplateData: proto.Clone(v.data).(*computev1beta1.LaunchTemplateData),
		DefaultVersion: t.defaultNum, LatestVersion: t.latestNum, CreateTime: t.createTime, Creator: t.creator,
		Tags: t.tags, Version: version, VersionCreateTime: v.createTime,
	}
}

// resolve returns the version a selector names: none or `default`, `latest`
// or a number the template has.
func (t *fakeTemplate) resolve(selector string) (int32, error) {
	switch selector {
	case "", "default":
		return t.defaultNum, nil
	case "latest":
		return t.latestNum, nil
	}
	n, err := strconv.Atoi(selector)
	if err != nil || t.versions[int32(n)] == nil {
		return 0, fmt.Errorf("%s has no version %s", t.name, selector)
	}
	return int32(n), nil
}

// checkRecipe checks a recipe for what its names say.
func (f *fakeCompute) checkRecipe(data *computev1beta1.LaunchTemplateData) error {
	switch {
	case data.GetInstanceType() == "" || data.GetKeyPair() == "":
		return fmt.Errorf("instance_type and key_pair are required")
	case (data.GetImage() == "") == (data.GetImageFamily() == ""):
		return fmt.Errorf("set exactly one of image and image_family")
	case len(data.GetNetworkInterfaces()) > 1:
		return fmt.Errorf("at most one network interface")
	}
	return nil
}

func (f *fakeCompute) listTemplates(prefix string, r *http.Request) ([]*computev1beta1.LaunchTemplate, string) {
	views := map[string]*computev1beta1.LaunchTemplate{}
	for name, t := range f.templates {
		views[name] = t.view(t.defaultNum, false)
	}
	return page(views, prefix, r, func(*computev1beta1.LaunchTemplate) bool { return true })
}

func (f *fakeCompute) createTemplate(w http.ResponseWriter, parent string, body []byte) {
	template := new(computev1beta1.LaunchTemplate)
	if err := protojson.Unmarshal(body, template); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err := f.checkRecipe(template.GetLaunchTemplateData()); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	f.next++
	t := &fakeTemplate{
		name: fmt.Sprintf("%s/lt-%d", parent, f.next), tags: template.GetTags(), creator: "user:test",
		versions: map[int32]*fakeVersion{}, createTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)),
	}
	t.addVersion(template.GetLaunchTemplateData())
	t.defaultNum = 1
	f.templates[t.name] = t
	writeMessage(w, t.view(1, false))
}

// addVersion gives data the number past the highest the template has given.
func (t *fakeTemplate) addVersion(data *computev1beta1.LaunchTemplateData) int32 {
	t.latestNum++
	t.versionTime++
	t.versions[t.latestNum] = &fakeVersion{data: data, createTime: timestamppb.New(time.Date(2026, 11, 1, 12, t.versionTime, 0, 0, time.UTC))}
	return t.latestNum
}

// template serves one launch template: its name may end in `@{version}`, or
// in an action, `:createVersion`, `:listVersions` or `:deleteVersion`.
func (f *fakeCompute) template(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	rest, action, _ := strings.Cut(path, ":")
	name, selector, selected := strings.Cut(rest, "@")
	t, ok := f.templates[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	version, err := t.resolve(selector)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	switch {
	case action == "createVersion" && r.Method == http.MethodPost:
		request := new(computev1beta1.CreateLaunchTemplateVersionRequest)
		if err := protojson.Unmarshal(body, request); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		if err := f.checkRecipe(request.GetLaunchTemplateData()); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		writeMessage(w, t.view(t.addVersion(request.GetLaunchTemplateData()), true))
	case action == "listVersions" && r.Method == http.MethodGet:
		var views []*computev1beta1.LaunchTemplate
		for n := t.latestNum; n >= 1; n-- {
			if t.versions[n] != nil {
				views = append(views, t.view(n, true))
			}
		}
		i, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		next := ""
		if i+1 < len(views) {
			next = strconv.Itoa(i + 1)
		}
		writeMessage(w, &computev1beta1.ListLaunchTemplateVersionsResponse{LaunchTemplates: views[i : i+1], NextPageToken: next})
	case action != "":
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+path+" isn't served")
	case r.Method == http.MethodGet:
		writeMessage(w, t.view(version, selected))
	case r.Method == http.MethodPatch:
		update := new(computev1beta1.LaunchTemplate)
		if err := protojson.Unmarshal(body, update); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
			switch field {
			case "tags":
				t.tags = update.GetTags()
			case "defaultVersion":
				if t.versions[update.GetDefaultVersion()] == nil {
					writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("%s has no version %d", t.name, update.GetDefaultVersion()))
					return
				}
				t.defaultNum = update.GetDefaultVersion()
			default:
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", field+" can't be updated")
				return
			}
		}
		writeMessage(w, t.view(t.defaultNum, false))
	case r.Method == http.MethodDelete:
		delete(f.templates, name)
		writeMessage(w, &emptypb.Empty{})
	}
}

// launchInstance launches an instance from a template's version, which a
// bare name, `@default` or `@latest` resolve to, with the recipe's fields
// the request sets over the version's: an image or family replaces both,
// and tags and network interfaces replace the version's whole.
func (f *fakeCompute) launchInstance(w http.ResponseWriter, parent string, body []byte) {
	request := new(computev1beta1.LaunchInstanceRequest)
	if err := protojson.Unmarshal(body, request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	data := new(computev1beta1.LaunchTemplateData)
	var resolved string
	if request.GetLaunchTemplate() != "" {
		name, selector, _ := strings.Cut(relativeName(request.GetLaunchTemplate()), "@")
		t, ok := f.templates[name]
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
			return
		}
		version, err := t.resolve(selector)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		data = proto.Clone(t.versions[version].data).(*computev1beta1.LaunchTemplateData)
		resolved = full("compute", fmt.Sprintf("%s@%d", name, version))
	}
	over := request.GetLaunchTemplateData()
	if over.GetImage() != "" || over.GetImageFamily() != "" {
		data.Image, data.ImageFamily = over.GetImage(), over.GetImageFamily()
	}
	if over.GetBootVolume() != nil {
		if data.BootVolume == nil {
			data.BootVolume = &computev1beta1.LaunchTemplateBootVolume{}
		}
		if over.GetBootVolume().GetSizeGib() != 0 {
			data.BootVolume.SizeGib = over.GetBootVolume().GetSizeGib()
		}
		if over.GetBootVolume().GetStorageClass() != "" {
			data.BootVolume.StorageClass = over.GetBootVolume().GetStorageClass()
		}
	}
	for field, set := range map[*string]string{
		&data.InstanceType: over.GetInstanceType(), &data.KeyPair: over.GetKeyPair(),
		&data.LoginUser: over.GetLoginUser(), &data.ServiceAccount: over.GetServiceAccount(),
	} {
		if set != "" {
			*field = set
		}
	}
	if len(over.GetTags()) > 0 {
		data.Tags = over.GetTags()
	}
	if len(over.GetNetworkInterfaces()) > 0 {
		data.NetworkInterfaces = over.GetNetworkInterfaces()
	}
	if err := f.checkRecipe(data); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	instance := &computev1beta1.Instance{
		InstanceType: data.GetInstanceType(), Image: data.GetImage(), ImageFamily: data.GetImageFamily(),
		KeyPair: data.GetKeyPair(), LoginUser: data.GetLoginUser(), ServiceAccount: data.GetServiceAccount(),
		Tags: data.GetTags(), LaunchTemplate: resolved,
	}
	if boot := data.GetBootVolume(); boot != nil {
		instance.BootVolume = &computev1beta1.BootVolume{SizeGib: boot.GetSizeGib(), StorageClass: boot.GetStorageClass()}
	}
	// An interface the launch creates, from a spec or from none, is the
	// instance's own.
	for _, source := range data.GetNetworkInterfaces() {
		if name := source.GetNetworkInterface(); name != "" {
			instance.NetworkInterfaces = append(instance.NetworkInterfaces, name)
		}
	}
	if len(instance.NetworkInterfaces) == 0 {
		f.next++
		zone := strings.TrimSuffix(parent[strings.Index(parent, "projects/"):], "/instances")
		instance.NetworkInterfaces = []string{full("network", fmt.Sprintf("%s/networkInterfaces/eni-launch-%d", zone, f.next))}
	}
	f.startInstance(w, parent, instance)
}
