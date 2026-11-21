package computev1beta1

import (
	v1beta1 "go.origincloud.eu/sdk/compute/v1beta1"
)

// instanceLaunchOverrides returns the fields of a recipe that an instance's
// arguments set, for a launch from a launch template: the fields the
// template supplies are left empty, which keeps the version's. A launch
// from a template names the arguments the configuration sets; an argument
// the template supplies is unknown in the plan, which is empty here.
func instanceLaunchOverrides(instance *v1beta1.Instance) *v1beta1.LaunchTemplateData {
	data := &v1beta1.LaunchTemplateData{
		InstanceType:   instance.GetInstanceType(),
		Image:          instance.GetImage(),
		ImageFamily:    instance.GetImageFamily(),
		KeyPair:        instance.GetKeyPair(),
		LoginUser:      instance.GetLoginUser(),
		ServiceAccount: instance.GetServiceAccount(),
		Tags:           instance.GetTags(),
	}
	if boot := instance.GetBootVolume(); boot != nil {
		data.BootVolume = &v1beta1.LaunchTemplateBootVolume{SizeGib: boot.GetSizeGib(), StorageClass: boot.GetStorageClass()}
	}
	for _, name := range instance.GetNetworkInterfaces() {
		data.NetworkInterfaces = append(data.NetworkInterfaces, &v1beta1.LaunchTemplateNetworkInterface{
			Source: &v1beta1.LaunchTemplateNetworkInterface_NetworkInterface{NetworkInterface: name},
		})
	}
	return data
}
