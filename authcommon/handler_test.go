package authcommon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

func TestParseGroupPageRequest(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantLimit  int
		wantFilter string
	}{
		{
			name:      "defaults when absent",
			query:     "",
			wantLimit: DefaultGroupPageSize,
		},
		{
			name:      "explicit limit",
			query:     "limit=25",
			wantLimit: 25,
		},
		{
			name:      "limit capped",
			query:     "limit=100000",
			wantLimit: MaxGroupPageSize,
		},
		{
			name:      "zero limit falls back to default",
			query:     "limit=0",
			wantLimit: DefaultGroupPageSize,
		},
		{
			name:      "negative limit falls back to default",
			query:     "limit=-1",
			wantLimit: DefaultGroupPageSize,
		},
		{
			name:      "unparseable limit falls back to default",
			query:     "limit=many",
			wantLimit: DefaultGroupPageSize,
		},
		{
			name:       "name filter is carried through",
			query:      "limit=10&name=eng",
			wantLimit:  10,
			wantFilter: "eng",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("failed to parse query: %v", err)
			}

			req, err := ParseGroupPageRequest("auth0", query)
			if err != nil {
				t.Fatalf("ParseGroupPageRequest() error = %v", err)
			}
			if req.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", req.Limit, tt.wantLimit)
			}
			if req.NameFilter != tt.wantFilter {
				t.Errorf("NameFilter = %q, want %q", req.NameFilter, tt.wantFilter)
			}
			if req.Cursor != "" {
				t.Errorf("Cursor = %q, want empty", req.Cursor)
			}
		})
	}
}

// pagedFetch serves the given groups in pages of the requested size, using the offset as its
// native continuation token. It stands in for an identity provider that pages natively.
func pagedFetch(all state.GroupInfoList, calls *[]PageRequest) FetchGroupPageFunc {
	return func(_ context.Context, req PageRequest) (PageResult, error) {
		*calls = append(*calls, req)

		start := 0
		if req.Cursor != "" {
			start, _ = strconv.Atoi(req.Cursor)
		}

		end := min(start+req.Limit, len(all))

		var next string
		if end < len(all) {
			next = strconv.Itoa(end)
		}

		return PageResult{Items: all[start:end], NextCursor: next}, nil
	}
}

func testGroups(n int) state.GroupInfoList {
	groups := make(state.GroupInfoList, 0, n)
	for i := range n {
		groups = append(groups, state.GroupInfo{ID: "g" + strconv.Itoa(i), Name: "group-" + strconv.Itoa(i)})
	}

	return groups
}

func TestListGroupsHandlerWalksEveryPage(t *testing.T) {
	var calls []PageRequest
	srv := httptest.NewServer(ListGroupsHandler("auth0", pagedFetch(testGroups(25), &calls)))
	defer srv.Close()

	var (
		seen   []string
		cursor string
		pages  int
	)
	for {
		u := srv.URL + "/?limit=10"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}

		resp, err := http.Get(u)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var page GroupPage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatalf("failed to decode page: %v", err)
		}
		resp.Body.Close()

		for _, group := range page.Items {
			seen = append(seen, group.ID)
		}

		pages++
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}

		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if pages != 3 {
		t.Errorf("walked %d pages, want 3", pages)
	}
	if len(seen) != 25 {
		t.Fatalf("collected %d groups, want 25", len(seen))
	}

	unique := make(map[string]struct{}, len(seen))
	for _, id := range seen {
		if _, ok := unique[id]; ok {
			t.Fatalf("group %s was returned more than once", id)
		}
		unique[id] = struct{}{}
	}

	// Every page after the first must have carried the previous page's position upstream.
	for i, call := range calls {
		if i == 0 && call.Cursor != "" {
			t.Error("the first upstream call should not carry a cursor")
		}
		if i > 0 && call.Cursor == "" {
			t.Errorf("upstream call %d should have carried a cursor", i)
		}
	}
}

func TestListGroupsHandlerLastPageOmitsCursor(t *testing.T) {
	var calls []PageRequest
	srv := httptest.NewServer(ListGroupsHandler("auth0", pagedFetch(testGroups(3), &calls)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}

	if _, ok := body["nextCursor"]; ok {
		t.Error("nextCursor should be omitted entirely when the listing is exhausted")
	}
}

// A provider that drops entries after fetching them returns a short page that still has a
// successor. The handler must take hasMore from the cursor rather than from the page size.
func TestListGroupsHandlerShortPageStillHasNextCursor(t *testing.T) {
	fetch := func(_ context.Context, req PageRequest) (PageResult, error) {
		if req.Cursor == "" {
			// One item filtered out of a full page of ten.
			return PageResult{Items: testGroups(9), NextCursor: "10"}, nil
		}

		return PageResult{Items: testGroups(2)}, nil
	}

	srv := httptest.NewServer(ListGroupsHandler("okta", fetch))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var page GroupPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("failed to decode page: %v", err)
	}

	if len(page.Items) != 9 {
		t.Errorf("got %d items, want 9", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Error("a short page must still advertise its successor")
	}
}

func TestListGroupsHandlerRejectsForeignCursor(t *testing.T) {
	var calls []PageRequest
	srv := httptest.NewServer(ListGroupsHandler("auth0", pagedFetch(testGroups(25), &calls)))
	defer srv.Close()

	// Minted for a different provider.
	foreign, err := EncodeCursor("okta", "", 10, "10")
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}

	resp, err := http.Get(srv.URL + "/?limit=10&cursor=" + url.QueryEscape(foreign))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if len(calls) != 0 {
		t.Error("a rejected cursor should not reach the identity provider")
	}
}

func TestListGroupsHandlerRejectsCursorAfterFilterChange(t *testing.T) {
	var calls []PageRequest
	srv := httptest.NewServer(ListGroupsHandler("auth0", pagedFetch(testGroups(25), &calls)))
	defer srv.Close()

	cursor, err := EncodeCursor("auth0", "eng", 10, "10")
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}

	resp, err := http.Get(srv.URL + "/?limit=10&name=sales&cursor=" + url.QueryEscape(cursor))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// A cursor means "the next page at the size it was minted for". Auth0's native token is a page
// number, so replaying a cursor at a different page size would skip or repeat rows.
func TestListGroupsHandlerRejectsCursorAfterLimitChange(t *testing.T) {
	var calls []PageRequest
	srv := httptest.NewServer(ListGroupsHandler("auth0", pagedFetch(testGroups(25), &calls)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	var page GroupPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("failed to decode page: %v", err)
	}
	resp.Body.Close()

	if page.NextCursor == "" {
		t.Fatal("the first page of 25 groups should advertise a successor")
	}

	calls = nil

	resp, err = http.Get(srv.URL + "/?limit=100&cursor=" + url.QueryEscape(page.NextCursor))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if len(calls) != 0 {
		t.Error("a rejected cursor should not reach the identity provider")
	}
}

// The limit is clamped and defaulted before the cursor is minted, so two requests that ask for
// different limits but resolve to the same effective one still page together.
func TestListGroupsHandlerCursorSurvivesEquivalentLimits(t *testing.T) {
	var calls []PageRequest
	srv := httptest.NewServer(ListGroupsHandler("auth0", pagedFetch(testGroups(250), &calls)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?limit=100")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	var page GroupPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("failed to decode page: %v", err)
	}
	resp.Body.Close()

	if page.NextCursor == "" {
		t.Fatal("the first page of 250 groups should advertise a successor")
	}

	// Over the cap, so it clamps back to the same effective page size.
	resp, err = http.Get(srv.URL + "/?limit=100000&cursor=" + url.QueryEscape(page.NextCursor))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestListGroupsHandlerEncodesEmptyItemsAsArray(t *testing.T) {
	fetch := func(context.Context, PageRequest) (PageResult, error) {
		return PageResult{}, nil
	}

	srv := httptest.NewServer(ListGroupsHandler("auth0", fetch))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?limit=10")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Items *[]state.GroupInfo `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body.Items == nil {
		t.Error("items should encode as an empty array, never as null")
	}
}
