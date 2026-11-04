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

	billingv1beta1 "go.origincloud.eu/sdk/billing/v1beta1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultAccount = "organizations/acme/billingAccounts/ba-1"
	billingProject = "projects/acme-prod/billingInfo"
)

// The billing countries: one invoiced with VAT, one under the reverse-charge
// rule, which needs a VAT id.
var billingCountries = []*billingv1beta1.BillingCountry{
	{Name: "billingCountries/DE", RegionCode: "DE", DisplayName: "Germany"},
	{Name: "billingCountries/AT", RegionCode: "AT", DisplayName: "Austria", VatIdRequired: true},
}

// fakeBilling serves the billing service's REST API from memory, for the
// organization acme, which has its default billing account, paying for its
// project acme-prod. Its mutations answer synchronously, as the API's do;
// its lists answer one item per page. Every billing account has its spend
// limit, which exists with it. An account is refused to be created or
// updated without a whole profile, and to be deleted while it is the
// default, pays for a project or has a spend limit.
type fakeBilling struct {
	*httptest.Server

	mu          sync.Mutex
	next        int
	accounts    map[string]*billingv1beta1.BillingAccount
	infos       map[string]*billingv1beta1.ProjectBillingInfo
	spendLimits map[string]*billingv1beta1.SpendLimit
	// calls are the mutations received, such as
	// "DELETE organizations/acme/billingAccounts/ba-2", with their update
	// masks, such as "PATCH projects/acme-prod/billingInfo billingAccount".
	calls []string
}

func newFakeBilling(t *testing.T) *fakeBilling {
	f := &fakeBilling{
		next: 1,
		accounts: map[string]*billingv1beta1.BillingAccount{defaultAccount: {
			Name: defaultAccount, Urn: "urn:origincloud:billing:billing-account/" + defaultAccount,
			DisplayName: "default", DefaultAccount: true,
			PaymentMethod: &billingv1beta1.PaymentMethod{State: billingv1beta1.PaymentMethod_NOT_REQUIRED},
			Creator:       "user:test", CreateTime: timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)),
		}},
		infos:       map[string]*billingv1beta1.ProjectBillingInfo{billingProject: {Name: billingProject, BillingAccount: defaultAccount}},
		spendLimits: map[string]*billingv1beta1.SpendLimit{defaultAccount + "/spendLimit": {Name: defaultAccount + "/spendLimit"}},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBilling) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeBilling) serve(w http.ResponseWriter, r *http.Request) {
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
	case path == "billingCountries" && r.Method == http.MethodGet:
		countries := map[string]*billingv1beta1.BillingCountry{}
		for i, c := range billingCountries {
			countries[fmt.Sprintf("billingCountries/%d", i)] = c
		}
		items, next := page(countries, "billingCountries/", r, func(*billingv1beta1.BillingCountry) bool { return true })
		writeMessage(w, &billingv1beta1.ListBillingCountriesResponse{BillingCountries: items, NextPageToken: next})
	case len(s) == 2 && s[0] == "billingCountries" && r.Method == http.MethodGet:
		writeNamed(w, path, billingCountries)
	case len(s) == 3 && s[0] == "organizations" && s[2] == "billingAccounts" && r.Method == http.MethodGet:
		items, next := page(f.accounts, path+"/", r, func(*billingv1beta1.BillingAccount) bool { return true })
		writeMessage(w, &billingv1beta1.ListBillingAccountsResponse{BillingAccounts: items, NextPageToken: next})
	case len(s) == 3 && s[0] == "organizations" && s[2] == "billingAccounts" && r.Method == http.MethodPost:
		f.createAccount(w, r, path, body)
	case len(s) == 4 && s[2] == "billingAccounts" && r.Method == http.MethodDelete:
		f.deleteAccount(w, path)
	case len(s) == 4 && s[2] == "billingAccounts" && r.Method == http.MethodPatch:
		f.updateAccount(w, r, path, body)
	case len(s) == 4 && s[2] == "billingAccounts":
		serveSynchronous(w, r, path, body, f.accounts)
	case len(s) == 5 && s[2] == "billingAccounts" && s[4] == "spendLimit" && (r.Method == http.MethodGet || r.Method == http.MethodPatch):
		if limit, ok := f.spendLimits[path]; ok && r.Method == http.MethodPatch {
			limit.UpdateTime = timestamppb.New(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
		}
		serveSynchronous(w, r, path, body, f.spendLimits, "limit", "alertThresholdsPercent", "notificationEmails")
	case len(s) == 3 && s[0] == "projects" && s[2] == "billingInfo" && r.Method == http.MethodPatch:
		info := new(billingv1beta1.ProjectBillingInfo)
		if err := protojson.Unmarshal(body, info); err == nil && f.accounts[info.GetBillingAccount()] == nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", info.GetBillingAccount()+" isn't a billing account of the project's organization")
			return
		}
		serveSynchronous(w, r, path, body, f.infos, "billingAccount")
	case len(s) == 3 && s[0] == "projects" && s[2] == "billingInfo" && r.Method == http.MethodGet:
		serveSynchronous(w, r, path, body, f.infos)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" isn't served")
	}
}

func (f *fakeBilling) createAccount(w http.ResponseWriter, r *http.Request, parent string, body []byte) {
	account := new(billingv1beta1.BillingAccount)
	if err := protojson.Unmarshal(body, account); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if r.URL.Query().Get("requestId") == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a create needs a request id")
		return
	}
	if err := wholeProfile(account); err != "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return
	}
	f.next++
	account.Name = fmt.Sprintf("%s/ba-%d", parent, f.next)
	account.Urn = "urn:origincloud:billing:billing-account/" + account.Name
	account.PaymentMethod = &billingv1beta1.PaymentMethod{State: billingv1beta1.PaymentMethod_MISSING}
	account.Creator = "user:test"
	account.CreateTime = timestamppb.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	f.accounts[account.Name] = account
	f.spendLimits[account.Name+"/spendLimit"] = &billingv1beta1.SpendLimit{Name: account.Name + "/spendLimit"}
	writeMessage(w, account)
}

// updateAccount applies the update, which must leave the account with a
// whole profile.
func (f *fakeBilling) updateAccount(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	account, ok := f.accounts[name]
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	}
	updated := proto.Clone(account).(*billingv1beta1.BillingAccount)
	if err := updateMasked(updated, body, r.URL.Query().Get("updateMask"), "displayName", "billingAddress", "invoiceEmail", "vatId", "contactPerson"); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if err := wholeProfile(updated); err != "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err)
		return
	}
	f.accounts[name] = updated
	writeMessage(w, updated)
}

func (f *fakeBilling) deleteAccount(w http.ResponseWriter, name string) {
	account, ok := f.accounts[name]
	switch {
	case !ok:
		writeError(w, http.StatusNotFound, "NOT_FOUND", name+" doesn't exist")
		return
	case account.GetDefaultAccount():
		writeError(w, http.StatusConflict, "CONFLICT", name+" is the organization's default billing account")
		return
	case f.spendLimits[name+"/spendLimit"].Limit != nil:
		writeError(w, http.StatusConflict, "CONFLICT", name+" has a spend limit: remove it first")
		return
	}
	for _, info := range f.infos {
		if info.GetBillingAccount() == name {
			writeError(w, http.StatusConflict, "CONFLICT", name+" pays for "+strings.TrimSuffix(info.GetName(), "/billingInfo")+": link it to another account first")
			return
		}
	}
	delete(f.accounts, name)
	delete(f.spendLimits, name+"/spendLimit")
	writeMessage(w, &emptypb.Empty{})
}

// wholeProfile returns why account's profile isn't whole, or nothing.
func wholeProfile(account *billingv1beta1.BillingAccount) string {
	address := account.GetBillingAddress()
	switch {
	case account.GetDisplayName() == "":
		return "a billing account needs a display name"
	case address == nil || account.GetInvoiceEmail() == "" || account.GetContactPerson() == "":
		return "a billing account needs a billing address, an invoice email and a contact person"
	case address.GetRegionCode() != "DE" && account.GetVatId() == "":
		return "a billing address outside Germany needs a vat_id"
	}
	return ""
}
