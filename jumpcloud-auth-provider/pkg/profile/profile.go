package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/authcommon"
	"github.com/obot-platform/providers/jumpcloud-auth-provider/pkg/client"
)

const (
	bulkGroupLookupThreshold = 50
	maxGroups                = 100000
)

// UserInfo represents basic user profile information.
type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

// UserUnavailableError indicates the JumpCloud account is not currently available for login.
type UserUnavailableError struct {
	Reason string
}

func (e *UserUnavailableError) Error() string {
	return e.Reason
}

type oidcUserInfo struct {
	Sub               string `json:"sub"`
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
	Username          string `json:"username"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	Name              string `json:"name"`
}

type systemUser struct {
	ID          string `json:"_id"`
	Email       string `json:"email"`
	Username    string `json:"username"`
	FirstName   string `json:"firstname"`
	LastName    string `json:"lastname"`
	DisplayName string `json:"displayname"`
	Activated   bool   `json:"activated"`
	State       string `json:"state"`
	Suspended   bool   `json:"suspended"`
}

type systemUserList struct {
	Results []systemUser `json:"results"`
}

type userGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type graphObjectWithPaths struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// GetUserInfo resolves the authenticated OIDC user to a JumpCloud system user and returns its canonical ID.
func GetUserInfo(ctx context.Context, accessToken, issuerURL string, apiClient *client.APIClient) (*UserInfo, error) {
	oidcUser, err := fetchOIDCUserInfo(ctx, accessToken, issuerURL)
	if err != nil {
		return nil, err
	}

	user, err := findSystemUserForOIDCUser(ctx, apiClient, oidcUser)
	if err != nil {
		return &UserInfo{
			Name:    preferredOIDCUserName(oidcUser),
			IconURL: "",
		}, nil
	}

	if err := ensureUserAvailable(user); err != nil {
		return nil, err
	}

	return &UserInfo{
		ID:      user.ID,
		Name:    preferredUserName(user, oidcUser),
		IconURL: "",
	}, nil
}

// FetchGroupPage fetches one page of JumpCloud user groups.
//
// The continuation token is the skip offset: the v2 API pages with limit/skip and has no opaque
// cursor of its own. Results are sorted by name server-side, which makes the offset stable enough
// to page over.
func FetchGroupPage(ctx context.Context, apiClient *client.APIClient, req authcommon.PageRequest) (authcommon.PageResult, error) {
	skip := 0
	if req.Cursor != "" {
		var err error
		if skip, err = strconv.Atoi(req.Cursor); err != nil || skip < 0 {
			return authcommon.PageResult{}, fmt.Errorf("%w: %q is not a skip offset", authcommon.ErrInvalidCursor, req.Cursor)
		}
	}

	query := url.Values{}
	query.Set("limit", strconv.Itoa(req.Limit))
	query.Set("skip", strconv.Itoa(skip))
	query.Set("sort", "name")
	if req.NameFilter != "" {
		// This `name:search` filter is a case-insensitive substring match.
		query.Set("filter", "name:search:"+req.NameFilter)
	}

	resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/v2/usergroups", query, nil)
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to fetch JumpCloud groups: %w", err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to read JumpCloud groups response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return authcommon.PageResult{}, fmt.Errorf("usergroups endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var groups []userGroup
	if err := json.Unmarshal(body, &groups); err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to decode JumpCloud groups response: %w", err)
	}

	return authcommon.PageResult{
		Items:      convertGroupsToGroupInfos(groups),
		NextCursor: nextGroupCursor(skip, len(groups), req.Limit, resp.Header.Get("X-Total-Count")),
	}, nil
}

// nextGroupCursor decides whether another page exists. JumpCloud reports the unpaged size in
// X-Total-Count, which gives an exact answer; without it, a full page is taken to mean there may
// be more, which costs at most one extra empty request at the end.
func nextGroupCursor(skip, fetched, limit int, totalHeader string) string {
	if fetched == 0 {
		return ""
	}

	if total, err := strconv.Atoi(totalHeader); err == nil {
		if skip+fetched >= total {
			return ""
		}
		return strconv.Itoa(skip + fetched)
	}

	if fetched < limit {
		return ""
	}

	return strconv.Itoa(skip + fetched)
}

// FetchUserGroupInfos fetches all JumpCloud user groups for a specific user.
func FetchUserGroupInfos(ctx context.Context, apiClient *client.APIClient, userID string) (state.GroupInfoList, error) {
	user, err := getSystemUserByID(ctx, apiClient, userID)
	if err != nil {
		return nil, err
	}
	if err := ensureUserAvailable(user); err != nil {
		return nil, err
	}

	memberOfIDs, err := fetchUserMemberOfIDs(ctx, apiClient, userID)
	if err != nil {
		return nil, err
	}
	if len(memberOfIDs) == 0 {
		return state.GroupInfoList{}, nil
	}

	groups, err := fetchGroupsByIDs(ctx, apiClient, memberOfIDs)
	if err != nil {
		return nil, err
	}
	return convertGroupsToGroupInfos(groups), nil
}

func fetchOIDCUserInfo(ctx context.Context, accessToken, issuerURL string) (*oidcUserInfo, error) {
	userInfoURL := strings.TrimSuffix(issuerURL, "/") + "/userinfo"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
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
		return nil, fmt.Errorf("failed to read userinfo response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var userInfo oidcUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}

	return &userInfo, nil
}

func findSystemUserForOIDCUser(ctx context.Context, apiClient *client.APIClient, oidcUser *oidcUserInfo) (*systemUser, error) {
	if sub := strings.TrimSpace(oidcUser.Sub); sub != "" {
		user, err := getSystemUserByID(ctx, apiClient, sub)
		if err == nil {
			return user, nil
		}
	}

	candidates := []struct {
		field string
		value string
	}{
		{field: "email", value: strings.TrimSpace(oidcUser.Email)},
		{field: "username", value: strings.TrimSpace(oidcUser.PreferredUsername)},
		{field: "username", value: strings.TrimSpace(oidcUser.Username)},
	}

	seen := map[string]struct{}{}
	var lastErr error
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		key := candidate.field + ":" + candidate.value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		user, err := findSystemUserByField(ctx, apiClient, candidate.field, candidate.value)
		if err == nil {
			return user, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to resolve JumpCloud user for authenticated identity %q: %w", firstNonEmpty(oidcUser.Email, oidcUser.PreferredUsername, oidcUser.Username, oidcUser.Sub), lastErr)
	}

	return nil, fmt.Errorf("failed to resolve JumpCloud user for authenticated identity %q", firstNonEmpty(oidcUser.Email, oidcUser.PreferredUsername, oidcUser.Username, oidcUser.Sub))
}

func findSystemUserByField(ctx context.Context, apiClient *client.APIClient, field, value string) (*systemUser, error) {
	query := url.Values{}
	query.Set("limit", "1")
	query.Set("skip", "0")
	query.Set("fields", "_id,email,username,firstname,lastname,displayname,activated,state,suspended")
	query.Set("filter", fmt.Sprintf("%s:$eq:%s", field, value))

	resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/systemusers", query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query JumpCloud system users: %w", err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read JumpCloud system users response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("system users endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var list systemUserList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to decode JumpCloud system users response: %w", err)
	}

	if len(list.Results) == 0 {
		return nil, fmt.Errorf("no JumpCloud user found for %s %q", field, value)
	}

	return &list.Results[0], nil
}

func getSystemUserByID(ctx context.Context, apiClient *client.APIClient, userID string) (*systemUser, error) {
	resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/systemusers/"+url.PathEscape(userID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JumpCloud user %s: %w", userID, err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read JumpCloud user response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("system user endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var user systemUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("failed to decode JumpCloud user response: %w", err)
	}

	return &user, nil
}

func fetchAllGroups(ctx context.Context, apiClient *client.APIClient) ([]userGroup, error) {
	var allGroups []userGroup
	limit := 100

	for skip := 0; ; skip += limit {
		query := url.Values{}
		query.Set("limit", fmt.Sprintf("%d", limit))
		query.Set("skip", fmt.Sprintf("%d", skip))
		query.Set("sort", "name")

		resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/v2/usergroups", query, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch JumpCloud groups: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read JumpCloud groups response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("usergroups endpoint returned status %d: %s", resp.StatusCode, string(body))
		}

		var groups []userGroup
		if err := json.Unmarshal(body, &groups); err != nil {
			return nil, fmt.Errorf("failed to decode JumpCloud groups response: %w", err)
		}

		allGroups = append(allGroups, groups...)
		if len(groups) < limit {
			break
		}
		if len(allGroups) >= maxGroups {
			slog.Warn("Reached the maximum number of JumpCloud groups that can be listed; some groups were not loaded", "groupLimit", maxGroups)
			break
		}
	}

	return allGroups, nil
}

func fetchGroupsByIDs(ctx context.Context, apiClient *client.APIClient, groupIDs []string) ([]userGroup, error) {
	if len(groupIDs) > bulkGroupLookupThreshold {
		return fetchGroupsByIDsInBulk(ctx, apiClient, groupIDs)
	}

	groups := make([]userGroup, 0, len(groupIDs))

	for _, groupID := range groupIDs {
		group, shouldFallback, err := fetchGroupByID(ctx, apiClient, groupID)
		if err != nil {
			return nil, err
		}
		if shouldFallback {
			return fetchGroupsByIDsInBulk(ctx, apiClient, groupIDs)
		}
		if group == nil {
			// Missing groups are ignored so transient membership/listing races do not fail login.
			continue
		}

		groups = append(groups, *group)
	}

	return groups, nil
}

// fetchGroupsByIDsInBulk lists every group once and keeps those the user is a member of.
func fetchGroupsByIDsInBulk(ctx context.Context, apiClient *client.APIClient, groupIDs []string) ([]userGroup, error) {
	allGroups, err := fetchAllGroups(ctx, apiClient)
	if err != nil {
		return nil, err
	}

	memberOfSet := make(map[string]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		memberOfSet[id] = struct{}{}
	}

	filteredGroups := make([]userGroup, 0, len(groupIDs))
	for _, candidate := range allGroups {
		if candidate.ID == "" {
			continue
		}
		if _, ok := memberOfSet[candidate.ID]; !ok {
			continue
		}
		filteredGroups = append(filteredGroups, candidate)
	}

	return filteredGroups, nil
}

func fetchGroupByID(ctx context.Context, apiClient *client.APIClient, groupID string) (*userGroup, bool, error) {
	resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/v2/usergroups/"+url.PathEscape(groupID), nil, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to fetch JumpCloud group %s: %w", groupID, err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, false, fmt.Errorf("failed to read JumpCloud group response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var group userGroup
		if err := json.Unmarshal(body, &group); err != nil {
			return nil, false, fmt.Errorf("failed to decode JumpCloud group response: %w", err)
		}
		return &group, false, nil
	case http.StatusNotFound:
		return nil, false, nil
	case http.StatusBadRequest, http.StatusForbidden, http.StatusMethodNotAllowed:
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("usergroup endpoint returned status %d: %s", resp.StatusCode, string(body))
	}
}

func fetchUserMemberOfIDs(ctx context.Context, apiClient *client.APIClient, userID string) ([]string, error) {
	limit := 100
	groupIDs := map[string]struct{}{}

	for skip := 0; ; skip += limit {
		query := url.Values{}
		query.Set("limit", fmt.Sprintf("%d", limit))
		query.Set("skip", fmt.Sprintf("%d", skip))

		resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/v2/users/"+url.PathEscape(userID)+"/memberof", query, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch JumpCloud group memberships for user %s: %w", userID, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read JumpCloud memberships response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("memberof endpoint returned status %d: %s", resp.StatusCode, string(body))
		}

		var objs []graphObjectWithPaths
		if err := json.Unmarshal(body, &objs); err != nil {
			return nil, fmt.Errorf("failed to decode JumpCloud memberships response: %w", err)
		}

		for _, obj := range objs {
			if obj.ID == "" {
				continue
			}
			if obj.Type != "user_group" && obj.Type != "" {
				continue
			}
			groupIDs[obj.ID] = struct{}{}
		}

		if len(objs) < limit {
			break
		}
	}

	result := make([]string, 0, len(groupIDs))
	for id := range groupIDs {
		result = append(result, id)
	}
	return result, nil
}

func ensureUserAvailable(user *systemUser) error {
	if user == nil {
		return &UserUnavailableError{Reason: "JumpCloud account could not be resolved"}
	}
	if user.Suspended {
		return &UserUnavailableError{Reason: "JumpCloud account is suspended"}
	}
	if !user.Activated {
		return &UserUnavailableError{Reason: fmt.Sprintf("JumpCloud account is not active (state: %s)", firstNonEmpty(user.State, "unknown"))}
	}
	if user.State != "" && strings.ToUpper(user.State) != "ACTIVATED" {
		return &UserUnavailableError{Reason: fmt.Sprintf("JumpCloud account is not active (state: %s)", user.State)}
	}
	return nil
}

func preferredUserName(user *systemUser, oidcUser *oidcUserInfo) string {
	return firstNonEmpty(
		strings.TrimSpace(user.DisplayName),
		strings.TrimSpace(user.FirstName+" "+user.LastName),
		preferredOIDCUserName(oidcUser),
		strings.TrimSpace(user.Username),
		strings.TrimSpace(user.Email),
	)
}

func preferredOIDCUserName(oidcUser *oidcUserInfo) string {
	if oidcUser == nil {
		return ""
	}

	return firstNonEmpty(
		strings.TrimSpace(oidcUser.Name),
		strings.TrimSpace(oidcUser.GivenName+" "+oidcUser.FamilyName),
		strings.TrimSpace(oidcUser.PreferredUsername),
		strings.TrimSpace(oidcUser.Username),
		strings.TrimSpace(oidcUser.Email),
	)
}

func convertGroupsToGroupInfos(groups []userGroup) state.GroupInfoList {
	if len(groups) == 0 {
		return state.GroupInfoList{}
	}

	groupInfos := make(state.GroupInfoList, 0, len(groups))
	for _, group := range groups {
		if group.ID == "" {
			continue
		}
		groupInfos = append(groupInfos, state.GroupInfo{
			ID:      "jumpcloud/" + group.ID,
			Name:    group.Name,
			IconURL: nil,
		})
	}
	if groupInfos == nil {
		return state.GroupInfoList{}
	}
	return groupInfos
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// FetchGroupsByIDs resolves user group IDs to their current names.
//
// The v2 API has no batch group read, so this is one request per ID; authcommon overlaps them and
// caps how many IDs a single request may carry.
func FetchGroupsByIDs(ctx context.Context, apiClient *client.APIClient, ids []string) (state.GroupInfoList, error) {
	return authcommon.ResolveGroupsByLookup(ctx, ids, func(ctx context.Context, id string) (*state.GroupInfo, error) {
		resp, err := apiClient.DoRequest(ctx, http.MethodGet, "/api/v2/usergroups/"+url.PathEscape(id), nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch JumpCloud group %s: %w", id, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read JumpCloud group %s response: %w", id, err)
		}

		if resp.StatusCode == http.StatusNotFound {
			// The group was deleted in JumpCloud while a policy still references it.
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("usergroups endpoint returned status %d: %s", resp.StatusCode, string(body))
		}

		var group userGroup
		if err := json.Unmarshal(body, &group); err != nil {
			return nil, fmt.Errorf("failed to decode JumpCloud group %s response: %w", id, err)
		}
		if group.ID == "" {
			return nil, nil
		}

		return &state.GroupInfo{ID: "jumpcloud/" + group.ID, Name: group.Name}, nil
	})
}
