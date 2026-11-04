package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A method not managed is one of the API's that no resource or data source
// calls, such as a query over a moving window of time series, which a data
// source would answer differently at every plan.
//
// Every method of a service the provider generates for is called by a
// generated resource or data source, or declared here, by the API's name,
// with the reason: one that is neither fails generation, so that a method
// the API adds isn't left out unnoticed. Each declaration is checked
// against the API when generating: one whose method is missing, or that a
// resource or data source calls, fails generation. A declaration of a
// package the API doesn't have yet is left until it does, as a secret is.
type notManaged struct {
	// method is the API's, such as
	// origincloud.observability.v1beta1.MetricService.QueryTimeSeries.
	method protoreflect.FullName
	// reason says why no resource or data source calls it.
	reason string
}

var methodsNotManaged = []notManaged{
	{"origincloud.billing.v1beta1.BillingAccountService.StartPaymentMethodSetup", "a redirect to the payment provider, completed in a browser"},
	{"origincloud.billing.v1beta1.BillingAccountService.ConfirmPaymentMethodSetup", "completes a setup started in a browser"},
	{"origincloud.billing.v1beta1.BillingAccountService.ListProjectBillingInfo", "lists by account what each project's origincloud_billing_project_billing_info holds"},
	{"origincloud.billing.v1beta1.CostService.QueryBillingAccountCost", "a report over a window of time, not state: its answer changes at every plan"},
	{"origincloud.billing.v1beta1.CostService.QueryOrganizationCost", "a report over a window of time, not state: its answer changes at every plan"},
	{"origincloud.billing.v1beta1.CostService.QueryProjectCost", "a report over a window of time, not state: its answer changes at every plan"},
	{"origincloud.billing.v1beta1.CostService.QueryProjectUsage", "a report over a window of time, not state: its answer changes at every plan"},
	{"origincloud.billing.v1beta1.PriceListService.GetPriceList", "the prices in effect now, which no configuration sets or refers to"},
	{"origincloud.compute.v1beta1.InstanceService.StartInstance", "a change of an instance's power state, an action rather than configuration"},
	{"origincloud.compute.v1beta1.InstanceService.StopInstance", "a change of an instance's power state, an action rather than configuration"},
	{"origincloud.iam.v1beta1.PermissionService.TestIamPermissions", "a check of the caller's own permissions, not state"},
	{"origincloud.network.v1beta1.DnsPolicyService.SetDefaultDnsPolicy", "moves the project's default to another policy, which has no clear to pair it with"},
	{"origincloud.observability.v1beta1.MetricService.QueryTimeSeries", "a query over a moving window of time series, not state: its answer changes at every plan"},
	{"origincloud.pvs.v1beta1.VolumeService.ImportVolume", "a second create, from a downloaded disk image; origincloud_pvs_volume creates one empty or from a snapshot"},
	{"origincloud.support.v1beta1.CaseService.CloseCase", "support cases are created outside Terraform, so read only"},
	{"origincloud.support.v1beta1.CaseService.ReopenCase", "support cases are created outside Terraform, so read only"},
}

// methodsCalled are the methods the generated resources and data sources
// call, and the creates of types created outside Terraform, whose reason
// outside.go declares.
var methodsCalled = map[protoreflect.FullName]bool{}

// callMethods records the methods t's generated code calls.
func callMethods(t resourceType) {
	methods := []*protogen.Method{t.get, t.list}
	if t.managed() {
		methods = append(methods, t.create, t.update, t.del)
	}
	for _, m := range methods {
		if m != nil {
			methodsCalled[m.Desc.FullName()] = true
		}
	}
}

// checkMethodsCalled fails on every method of files' services that nothing
// calls and isn't declared not managed, and on every declaration of a
// method that is called or that the API doesn't have, in one of the API's
// packages, those of files.
func checkMethodsCalled(files []*protogen.File) error {
	var failures []error
	served := map[protoreflect.FullName]bool{}
	for _, f := range files {
		if !f.Generate {
			continue
		}
		for _, s := range f.Services {
			if host, _ := proto.GetExtension(s.Desc.Options(), annotations.E_DefaultHost).(string); host == "" {
				continue // served by every service, such as operations
			}
			for _, m := range s.Methods {
				name := m.Desc.FullName()
				served[name] = true
				declared := slices.ContainsFunc(methodsNotManaged, func(n notManaged) bool { return n.method == name })
				switch {
				case methodsCalled[name] && declared:
					failures = append(failures, fmt.Errorf("%s is declared not managed by Terraform, but a resource or data source calls it", name))
				case !methodsCalled[name] && !declared:
					failures = append(failures, fmt.Errorf("%s: no resource or data source calls it, and it isn't declared not managed by Terraform", name))
				}
			}
		}
	}
	for _, n := range methodsNotManaged {
		published := slices.ContainsFunc(files, func(f *protogen.File) bool {
			return strings.HasPrefix(string(n.method), string(f.Desc.Package())+".")
		})
		if published && !served[n.method] {
			failures = append(failures, fmt.Errorf("not managed by Terraform: no method %s", n.method))
		}
	}
	return errors.Join(failures...)
}
