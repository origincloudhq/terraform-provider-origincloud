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
	{"origincloud.billing.v1beta1.LegalDocumentService.DownloadLegalDocumentVersion", "a download of the document's text, not state"},
	{"origincloud.billing.v1beta1.LegalAcceptanceService.AcceptLegalDocuments", "a person's acceptance of the terms, which a configuration can't give on their behalf"},
	{"origincloud.billing.v1beta1.LegalAcceptanceService.QueryLegalRequirements", "what an organization still has to accept at the moment it asks, not state"},
	{"origincloud.billing.v1beta1.PriceListService.GetPriceList", "the prices in effect now, which no configuration sets or refers to"},
	{"origincloud.compute.v1beta1.LaunchTemplateService.DeleteLaunchTemplateVersion", "a version is a revision of the template's recipe and stays with the template's history; deleting origincloud_compute_launch_template deletes them all"},
	{"origincloud.compute.v1beta1.InstanceService.StartInstance", "a change of an instance's power state, an action rather than configuration"},
	{"origincloud.compute.v1beta1.InstanceService.StopInstance", "a change of an instance's power state, an action rather than configuration"},
	{"origincloud.dns.v1beta1.HostedZoneService.VerifyHostedZone", "checks a public zone's challenge now, which the platform also checks on its own; the TXT record it checks is created after the zone, often with another provider"},
	{"origincloud.dns.v1beta1.RecordSetService.BatchChangeRecordSets", "several record sets' changes at once, which each origincloud_dns_record_set makes on its own"},
	{"origincloud.iam.v1beta1.PermissionService.TestIamPermissions", "a check of the caller's own permissions, not state"},
	{"origincloud.network.v1beta1.ResolverPolicyService.SetDefaultResolverPolicy", "moves the project's default to another policy, which has no clear to pair it with"},
	{"origincloud.iam.v1beta1.UserService.ActivatePendingRoleBindings", "activates the roles granted to the caller's own verified email address, an action of the person signing in rather than configuration"},
	{"origincloud.iam.v1beta1.UserService.LookupUser", "finds a user by an email address, which origincloud_iam_user doesn't take"},
	{"origincloud.kms.v1beta1.GrantService.RenewGrant", "extends a grant's lifetime, an action rather than configuration"},
	{"origincloud.kms.v1beta1.KeyService.Decrypt", "a cryptographic operation on data, not state"},
	{"origincloud.kms.v1beta1.KeyService.Encrypt", "a cryptographic operation on data, not state"},
	{"origincloud.kms.v1beta1.KeyService.GenerateDataKey", "a cryptographic operation that answers with new key material at every call"},
	{"origincloud.kms.v1beta1.KeyService.RotateKey", "an action that adds a key version, rather than configuration"},
	{"origincloud.kms.v1beta1.KeyService.Sign", "a cryptographic operation on data, not state"},
	{"origincloud.kms.v1beta1.KeyService.UndeleteKey", "cancels a scheduled deletion, an action rather than configuration"},
	{"origincloud.kms.v1beta1.KeyService.RetireEncryptionContext", "retires a context of a key the platform manages, which only a platform service agent may call and no customer role holds"},
	{"origincloud.secretmanager.v1beta1.SecretManagerService.RestoreSecret", "brings a deleted secret back within its recovery window with every version as it was, which creating a secret does not do: the API refuses a create with the id of a deleted secret still in its window with CONFLICT, and so does the provider, rather than silently reviving old payloads; restore it with the CLI or the console, or destroy it with force_delete to free the id"},
	{"origincloud.secretmanager.v1beta1.SecretManagerService.AddSecretVersion", "adds a version with a payload, which a secret version resource creates with the next change"},
	{"origincloud.secretmanager.v1beta1.SecretManagerService.AccessSecretVersion", "answers with a version's payload, which a secret version data source reads with the next change"},
	{"origincloud.secretmanager.v1beta1.SecretManagerService.DisableSecretVersion", "disables a version, which a secret version resource does with the next change"},
	{"origincloud.secretmanager.v1beta1.SecretManagerService.EnableSecretVersion", "enables a version, which a secret version resource does with the next change"},
	{"origincloud.secretmanager.v1beta1.SecretManagerService.DestroySecretVersion", "destroys a version, which a secret version resource does when it is destroyed, with the next change"},
	{"origincloud.observability.v1beta1.MetricService.QueryTimeSeries", "a query over a moving window of time series, not state: its answer changes at every plan"},
	{"origincloud.pvs.v1beta1.VolumeService.ImportVolume", "a second create, from a downloaded disk image; origincloud_pvs_volume creates one empty or from a snapshot"},
	{"origincloud.sts.v1beta1.SecurityTokenService.AssumeServiceAccount", "exchanges a credential for a short-lived token, an action whose answer is a secret that changes at every call"},
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
