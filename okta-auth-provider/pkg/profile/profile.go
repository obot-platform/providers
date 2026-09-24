package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/authcommon"
	"github.com/okta/okta-sdk-golang/v5/okta"
)

const (
	oktaPageSize = 200
	maxGroups    = 100000
)

// UserInfo represents basic user profile information.
type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

// FetchUserGroupInfos retrieves all groups the specified user belongs to via Management API.
// Uses the /api/v1/users/{userId}/groups endpoint with service account permissions.
// Requires okta.users.read or okta.groups.read scope in the service account token.
func FetchUserGroupInfos(ctx context.Context, client *okta.APIClient, userID string) (state.GroupInfoList, error) {
	// Query user-specific groups endpoint (accepts user ID or login/email).
	groups, resp, err := client.UserAPI.ListUserGroups(ctx, userID).Limit(oktaPageSize).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch group memberships for user %s: %w", userID, err)
	}

	allGroups, err := collectRemainingPages(groups, resp)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch group memberships for user %s: %w", userID, err)
	}

	return convertOktaGroupsToGroupInfos(allGroups), nil
}

// FetchGroupPage fetches one page of groups from the Okta organization.
// Uses the Okta Management API endpoint: GET /api/v1/groups
//
// The continuation token is Okta's "after" cursor, lifted out of the Link header of the previous
// response.
//
// Ordering is Okta's own, not alphabetical. Okta only honors sortBy alongside the "search"
// parameter, and search is served from an eventually consistent index whose cursor paging can skip
// or repeat rows, so it is not safe to page over. Sorting each page locally is worse than not
// sorting at all: it would make every page individually alphabetical while the sequence as a whole
// stayed arbitrary, which reads as a bug. The name filter is what users navigate with instead.
func FetchGroupPage(ctx context.Context, client *okta.APIClient, req authcommon.PageRequest) (authcommon.PageResult, error) {
	r := client.GroupAPI.ListGroups(ctx).Limit(int32(req.Limit))
	if req.NameFilter != "" {
		// Okta matches q as a case-insensitive starts-with on the group name. It is the
		// pagination-safe filter; see the note on search above.
		r = r.Q(req.NameFilter)
	}
	if req.Cursor != "" {
		r = r.After(req.Cursor)
	}

	groups, resp, err := r.Execute()
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to fetch groups: %w", err)
	}

	nextCursor, err := nextGroupCursor(resp)
	if err != nil {
		return authcommon.PageResult{}, err
	}

	return authcommon.PageResult{
		Items:      convertOktaGroupsToGroupInfos(groups),
		NextCursor: nextCursor,
	}, nil
}

// nextGroupCursor extracts Okta's "after" cursor from the next-page link of a list response.
// Okta hands back a full URL; only the after value is worth carrying, and it is short.
func nextGroupCursor(resp *okta.APIResponse) (string, error) {
	if resp == nil || !resp.HasNextPage() {
		return "", nil
	}

	next, err := url.Parse(resp.NextPage())
	if err != nil {
		return "", fmt.Errorf("failed to parse Okta next page link: %w", err)
	}

	return next.Query().Get("after"), nil
}

// convertOktaGroupsToGroupInfos converts a slice of Okta groups to GroupInfo structs.
func convertOktaGroupsToGroupInfos(groups []okta.Group) state.GroupInfoList {
	if len(groups) == 0 {
		return state.GroupInfoList{}
	}

	groupInfos := make(state.GroupInfoList, 0, len(groups))
	for _, group := range groups {
		name := group.Profile.GetName()
		if name == "Okta Administrators" {
			// Skip the Okta Administrators group because it does not show in the OIDC groups claim
			continue
		}

		groupInfo := convertToGroupInfo(group)
		if groupInfo != nil {
			groupInfos = append(groupInfos, *groupInfo)
		}
	}

	return groupInfos
}

// convertToGroupInfo converts a single Okta Group to a GroupInfo struct.
// Follows the same pattern as Entra's convertToGroupInfo.
func convertToGroupInfo(group okta.Group) *state.GroupInfo {
	name := group.Profile.GetName()
	id := group.GetId()
	if id == "" {
		return nil
	}
	return &state.GroupInfo{
		ID:      "okta/" + id,
		Name:    name,
		IconURL: nil, // Okta doesn't support group photos
	}
}

// GetUserInfo returns the authenticated user's ID and name.
// Okta does not support user profile photos, so IconURL is always empty.
func GetUserInfo(ctx context.Context, accessToken, issuerURL string) (*UserInfo, error) {
	// Call /userinfo endpoint
	baseURL := strings.TrimSuffix(issuerURL, "/")
	userInfoURL := baseURL + "/oauth2/v1/userinfo"

	req, err := http.NewRequestWithContext(ctx, "GET", userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call userinfo endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse userinfo response
	var userInfo struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}
	return &UserInfo{
		ID:      userInfo.Sub,
		Name:    userInfo.Name,
		IconURL: "", // Okta doesn't support profile photos
	}, nil
}

// GroupMigrationMapping maps an old-format group ID to a new-format group ID.
type GroupMigrationMapping struct {
	OldID string `json:"oldID"` // "okta/{name}"
	NewID string `json:"newID"` // "okta/{id}"
}

// BuildGroupMigrationMapping fetches all groups from Okta and builds a mapping from
// old-format IDs ("okta/{name}") to new-format IDs ("okta/{id}").
// When multiple groups share a name, the preferred group is selected: OKTA_GROUP type
// is preferred over others, and among equal types, the earliest created group wins.
func BuildGroupMigrationMapping(ctx context.Context, client *okta.APIClient) ([]GroupMigrationMapping, error) {
	allGroups, err := fetchAllGroups(ctx, client)
	if err != nil {
		return nil, err
	}

	// Group by name, skipping groups without an ID
	byName := make(map[string][]okta.Group)
	for _, group := range allGroups {
		name := group.Profile.GetName()
		if name == "" || name == "Okta Administrators" || group.GetId() == "" {
			continue
		}
		byName[name] = append(byName[name], group)
	}

	mappings := make([]GroupMigrationMapping, 0, len(byName))
	for name, nameGroups := range byName {
		preferred := selectPreferredGroup(nameGroups)
		mappings = append(mappings, GroupMigrationMapping{
			OldID: "okta/" + name,
			NewID: "okta/" + preferred.GetId(),
		})
	}

	return mappings, nil
}

// fetchAllGroups fetches all groups from Okta, paginating through all pages.
func fetchAllGroups(ctx context.Context, client *okta.APIClient) ([]okta.Group, error) {
	groups, resp, err := client.GroupAPI.ListGroups(ctx).Limit(oktaPageSize).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch groups: %w", err)
	}

	return collectRemainingPages(groups, resp)
}

// collectRemainingPages walks the Link-header cursor from an Okta list call and appends every
// remaining page to the first one. Okta returns a single page per request.
func collectRemainingPages(first []okta.Group, resp *okta.APIResponse) ([]okta.Group, error) {
	allGroups := first
	for resp != nil && resp.HasNextPage() {
		if len(allGroups) >= maxGroups {
			slog.Warn("Reached the maximum number of Okta groups that can be listed; some groups were not loaded", "groupLimit", maxGroups)
			break
		}

		var (
			nextGroups []okta.Group
			err        error
		)
		if resp, err = resp.Next(&nextGroups); err != nil {
			return nil, fmt.Errorf("failed to fetch next page of groups: %w", err)
		}
		if len(nextGroups) == 0 {
			break
		}
		allGroups = append(allGroups, nextGroups...)
	}

	return allGroups, nil
}

// selectPreferredGroup picks the best group for migration when multiple groups share a name.
// Preference: OKTA_GROUP type first, then earliest creation time.
func selectPreferredGroup(groups []okta.Group) okta.Group {
	if len(groups) == 1 {
		return groups[0]
	}

	sort.Slice(groups, func(i, j int) bool {
		ti := groups[i].GetType()
		tj := groups[j].GetType()
		if ti != tj {
			// Prefer OKTA_GROUP
			if ti == "OKTA_GROUP" {
				return true
			}
			if tj == "OKTA_GROUP" {
				return false
			}
		}
		return groups[i].GetCreated().Before(groups[j].GetCreated())
	})

	return groups[0]
}

// FetchGroupsByIDs resolves group IDs to their current names.
//
// Okta has no batch group read, so this is one request per ID; authcommon overlaps them and caps
// how many IDs a single request may carry.
func FetchGroupsByIDs(ctx context.Context, client *okta.APIClient, ids []string) (state.GroupInfoList, error) {
	return authcommon.ResolveGroupsByLookup(ctx, ids, func(ctx context.Context, id string) (*state.GroupInfo, error) {
		group, resp, err := client.GroupAPI.GetGroup(ctx, id).Execute()
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				// The group was deleted in Okta while a policy still references it.
				return nil, nil
			}
			return nil, fmt.Errorf("failed to fetch group %s: %w", id, err)
		}
		if group == nil {
			return nil, nil
		}

		return convertToGroupInfo(*group), nil
	})
}
