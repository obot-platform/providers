package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

func listAuthGroups(t *testing.T, s *server, query string) (int, state.GroupInfoList, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/obot-list-auth-groups?"+query, nil)
	rec := httptest.NewRecorder()
	s.listAuthGroups(rec, req)
	var page struct {
		Items      state.GroupInfoList `json:"items"`
		NextCursor string              `json:"nextCursor"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode page: %v", err)
		}
	}
	return rec.Code, page.Items, page.NextCursor
}

func TestDirectoryListsKnownAndSeenGroups(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	s.directory = newGroupDirectory([]string{"LDAP-Obot-Admins", " LDAP-Obot-Auditors ", ""})

	status, items, next := listAuthGroups(t, s, "limit=50")
	if status != http.StatusOK || next != "" {
		t.Fatalf("status = %d next = %q", status, next)
	}
	if got := groupNames(items); got != "pingfederate/LDAP-Obot-Admins=LDAP-Obot-Admins,pingfederate/LDAP-Obot-Auditors=LDAP-Obot-Auditors" {
		t.Errorf("declared groups = %q", got)
	}

	s.rememberGroups([]string{"devs", "LDAP-Obot-Admins"}, "sub-1")
	_, items, _ = listAuthGroups(t, s, "")
	if got := groupNames(items); got != "pingfederate/devs=devs,pingfederate/LDAP-Obot-Admins=LDAP-Obot-Admins,pingfederate/LDAP-Obot-Auditors=LDAP-Obot-Auditors" {
		t.Errorf("groups after sign-in = %q", got)
	}

	_, items, _ = listAuthGroups(t, s, "name=audit")
	if got := groupNames(items); got != "pingfederate/LDAP-Obot-Auditors=LDAP-Obot-Auditors" {
		t.Errorf("filtered groups = %q", got)
	}
}

func TestDirectoryPaginates(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	s.directory = newGroupDirectory([]string{"b", "a", "D", "c", "e"})

	var all []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		status, items, next := listAuthGroups(t, s, "limit=2&cursor="+cursor)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		for _, item := range items {
			all = append(all, item.Name)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(all, ",") != "a,b,c,D,e" {
		t.Errorf("paged names = %v", all)
	}

	if status, _, _ := listAuthGroups(t, s, "limit=x"); status != http.StatusBadRequest {
		t.Errorf("bad limit: status = %d, want 400", status)
	}
	if _, items, _ := listAuthGroups(t, s, "cursor=zzz"); len(items) != 0 {
		t.Errorf("cursor past the end should give an empty page, got %v", items)
	}
	if _, items, next := listAuthGroups(t, s, "limit=0"); len(items) != 5 || next != "" {
		t.Errorf("limit=0 should use the default page size, got %d items next=%q", len(items), next)
	}
}

func TestDirectoryDropsOldestSeenGroupsButKeepsDeclared(t *testing.T) {
	d := newGroupDirectory([]string{"declared"})
	for i := 0; i < maxSeenGroups+5; i++ {
		d.remember([]string{"g" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)})
	}
	items, _ := d.list("", maxListLimit, "")
	if len(d.names) > maxSeenGroups+1 {
		t.Errorf("directory holds %d names, want at most %d", len(d.names), maxSeenGroups+1)
	}
	found := false
	for _, item := range items {
		if item.Name == "declared" {
			found = true
		}
	}
	if !found {
		t.Error("declared group was dropped")
	}
}

func TestGetAuthGroupsResolvesIDsInNamespace(t *testing.T) {
	s := newTestServer(t, "", time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/obot-get-auth-groups?ids=pingfederate%2FLDAP-Obot-Admins,other%2Fx,pingfederate%2F,%20pingfederate%2Fdevs%20", nil)
	rec := httptest.NewRecorder()
	s.getAuthGroups(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var page struct {
		Items state.GroupInfoList `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if got := groupNames(page.Items); got != "pingfederate/LDAP-Obot-Admins=LDAP-Obot-Admins,pingfederate/devs=devs" {
		t.Errorf("resolved = %q", got)
	}
}

func TestSplitLines(t *testing.T) {
	got := splitLines("LDAP-Admins, LDAP-Auditors\n\n Domain Users \r\nLDAP-Admins")
	if strings.Join(got, "|") != "LDAP-Admins|LDAP-Auditors|Domain Users" {
		t.Errorf("splitLines = %v", got)
	}
	if splitLines("") != nil {
		t.Error("empty input should give nil")
	}
}
