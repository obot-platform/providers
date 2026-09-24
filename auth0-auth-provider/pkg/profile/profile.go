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
	"github.com/obot-platform/providers/auth0-auth-provider/pkg/client"
	"github.com/obot-platform/providers/authcommon"
)

const maxGroups = 100000

// UserInfo represents basic user profile information.
type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

// auth0Role represents a role from the Auth0 Management API.
type auth0Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// GetUserInfo returns the authenticated user's ID, name, and profile picture.
// Uses the /userinfo endpoint with the user's access token.
func GetUserInfo(ctx context.Context, accessToken, domain string) (*UserInfo, error) {
	baseURL := strings.TrimSuffix(domain, "/")
	userInfoURL := fmt.Sprintf("https://%s/userinfo", baseURL)

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

	var userInfo struct {
		Sub     string `json:"sub"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}

	return &UserInfo{
		ID:      userInfo.Sub,
		Name:    userInfo.Name,
		IconURL: userInfo.Picture,
	}, nil
}

// FetchGroupPage fetches one page of roles from the Auth0 tenant.
// Uses the Management API endpoint: GET /api/v2/roles
// Roles are used as the group equivalent for Auth0.
func FetchGroupPage(ctx context.Context, mgmtClient *client.ManagementClient, req authcommon.PageRequest) (authcommon.PageResult, error) {
	page := 0
	if req.Cursor != "" {
		var err error
		if page, err = strconv.Atoi(req.Cursor); err != nil || page < 0 {
			return authcommon.PageResult{}, fmt.Errorf("%w: %q is not a page number", authcommon.ErrInvalidCursor, req.Cursor)
		}
	}

	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(req.Limit))
	// include_totals is what makes the next-page decision exact rather than inferred from a full
	// page. The total is used here only and is not reported upstream.
	query.Set("include_totals", "true")
	if req.NameFilter != "" {
		// Auth0 matches name_filter as a case-insensitive substring.
		query.Set("name_filter", req.NameFilter)
	}

	resp, err := mgmtClient.DoRequest(ctx, http.MethodGet, "/api/v2/roles?"+query.Encode(), nil)
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to fetch roles: %w", err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to read roles response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return authcommon.PageResult{}, fmt.Errorf("roles endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Roles []auth0Role `json:"roles"`
		Start int         `json:"start"`
		Total int         `json:"total"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return authcommon.PageResult{}, fmt.Errorf("failed to decode roles response: %w", err)
	}

	var nextCursor string
	if len(result.Roles) > 0 && result.Start+len(result.Roles) < result.Total {
		nextCursor = strconv.Itoa(page + 1)
	}

	return authcommon.PageResult{
		Items:      convertRolesToGroupInfos(result.Roles),
		NextCursor: nextCursor,
	}, nil
}

// FetchUserGroupInfos retrieves all roles assigned to the specified user.
// Uses the Management API endpoint: GET /api/v2/users/{userId}/roles
func FetchUserGroupInfos(ctx context.Context, mgmtClient *client.ManagementClient, userID string) (state.GroupInfoList, error) {
	var allRoles []auth0Role
	page := 0
	perPage := 100

	for {
		path := fmt.Sprintf("/api/v2/users/%s/roles?page=%d&per_page=%d&include_totals=true", url.PathEscape(userID), page, perPage)
		resp, err := mgmtClient.DoRequest(ctx, "GET", path, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch user roles: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			return nil, fmt.Errorf("failed to read user roles response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("user roles endpoint returned status %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Roles []auth0Role `json:"roles"`
			Total int         `json:"total"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("failed to decode user roles response: %w", err)
		}

		allRoles = append(allRoles, result.Roles...)

		if len(result.Roles) == 0 || len(allRoles) >= result.Total {
			break
		}
		if len(allRoles) >= maxGroups {
			slog.Warn("Reached the maximum number of Auth0 roles that can be listed; some roles were not loaded", "roleLimit", maxGroups)
			break
		}
		page++
	}

	return convertRolesToGroupInfos(allRoles), nil
}

// convertRolesToGroupInfos converts Auth0 roles to GroupInfo structs.
func convertRolesToGroupInfos(roles []auth0Role) state.GroupInfoList {
	if len(roles) == 0 {
		return state.GroupInfoList{}
	}

	groupInfos := make(state.GroupInfoList, 0, len(roles))
	for _, role := range roles {
		groupInfos = append(groupInfos, state.GroupInfo{
			ID:      "auth0/" + role.ID,
			Name:    role.Name,
			IconURL: nil,
		})
	}

	return groupInfos
}

// FetchGroupsByIDs resolves role IDs to their current names.
func FetchGroupsByIDs(ctx context.Context, mgmtClient *client.ManagementClient, ids []string) (state.GroupInfoList, error) {
	return authcommon.ResolveGroupsByLookup(ctx, ids, func(ctx context.Context, id string) (*state.GroupInfo, error) {
		resp, err := mgmtClient.DoRequest(ctx, http.MethodGet, "/api/v2/roles/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch role %s: %w", id, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read role %s response: %w", id, err)
		}

		if resp.StatusCode == http.StatusNotFound {
			// The role was deleted in Auth0 while a policy still references it.
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("role endpoint returned status %d: %s", resp.StatusCode, string(body))
		}

		var role auth0Role
		if err := json.Unmarshal(body, &role); err != nil {
			return nil, fmt.Errorf("failed to decode role %s response: %w", id, err)
		}
		if role.ID == "" {
			return nil, nil
		}

		return &state.GroupInfo{ID: "auth0/" + role.ID, Name: role.Name}, nil
	})
}
