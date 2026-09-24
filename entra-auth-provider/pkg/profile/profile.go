package profile

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	msgraphsdkgo "github.com/microsoftgraph/msgraph-sdk-go"
	msgraphcore "github.com/microsoftgraph/msgraph-sdk-go-core"
	"github.com/microsoftgraph/msgraph-sdk-go/directoryobjects"
	"github.com/microsoftgraph/msgraph-sdk-go/groups"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/models/odataerrors"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/authcommon"
)

const (
	graphPageSize = 999
	maxGroups     = 100000
)

// UserInfo represents basic user profile information.
type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

// photoCache stores profile photos as base64 data URLs.
// Photos are cached for up to 1 hour to improve performance and reduce API calls.
// Cache keys: "user:<userID>" or "group:<groupID>"
var photoCache = expirable.NewLRU[string, string](1000, nil, time.Hour)

const graphGroupsURLPrefix = "https://graph.microsoft.com/v1.0/groups?"

// graphHosts are the Microsoft Graph hosts a next link is allowed to name. They are the same hosts
// the SDK itself will attach a token to; see the validHosts list in msgraph-sdk-go.
var graphHosts = map[string]struct{}{
	"graph.microsoft.com":             {},
	"graph.microsoft.us":              {},
	"dod-graph.microsoft.us":          {},
	"graph.microsoft.de":              {},
	"microsoftgraph.chinacloudapi.cn": {},
	"canary.graph.microsoft.com":      {},
}

// graphGroupsPaths are the request paths a group listing next link may have.
var graphGroupsPaths = map[string]struct{}{
	"/v1.0/groups": {},
	"/beta/groups": {},
}

// FetchGroupPage retrieves one page of the tenant's groups, following Graph's @odata.nextLink as
// the continuation token.
func FetchGroupPage(ctx context.Context, client *msgraphsdkgo.GraphServiceClient, req authcommon.PageRequest) (authcommon.PageResult, error) {
	if req.Cursor != "" {
		link, err := expandNextLink(req.Cursor)
		if err != nil {
			return authcommon.PageResult{}, err
		}

		result, err := client.Groups().WithUrl(link).Get(ctx, nil)
		if err != nil {
			return authcommon.PageResult{}, classifyNextLinkError(ctx, err)
		}

		return groupPageFrom(result), nil
	}

	result, err := client.Groups().Get(ctx, groupsRequestConfiguration(req, true))
	if err != nil {
		// $orderby alongside $filter is accepted for a property Graph can sort and filter on, but
		// the combination is worth not betting the listing on. Retry unordered before giving up.
		if req.NameFilter == "" {
			return authcommon.PageResult{}, fmt.Errorf("failed to fetch groups: %w", err)
		}

		slog.Debug("group listing with $orderby was rejected, retrying unordered", "error", err)
		if result, err = client.Groups().Get(ctx, groupsRequestConfiguration(req, false)); err != nil {
			return authcommon.PageResult{}, fmt.Errorf("failed to fetch groups: %w", err)
		}
	}

	return groupPageFrom(result), nil
}

// FetchGroupsByIDs resolves group IDs to their current names.
func FetchGroupsByIDs(ctx context.Context, client *msgraphsdkgo.GraphServiceClient, ids []string) (state.GroupInfoList, error) {
	body := directoryobjects.NewGetByIdsPostRequestBody()
	body.SetIds(ids)
	// Without this, getByIds returns every kind of directory object an ID might name.
	body.SetTypes([]string{"group"})

	result, err := client.DirectoryObjects().GetByIds().PostAsGetByIdsPostResponse(ctx, body, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve groups by id: %w", err)
	}
	if result == nil {
		return state.GroupInfoList{}, nil
	}

	values := result.GetValue()
	groupInfos := make(state.GroupInfoList, 0, len(values))
	for _, object := range values {
		group, ok := object.(models.Groupable)
		if !ok {
			continue
		}
		if groupInfo := convertToGroupInfo(group); groupInfo != nil {
			groupInfos = append(groupInfos, *groupInfo)
		}
	}

	return groupInfos, nil
}

// groupsRequestConfiguration builds the query for the first page of a listing.
func groupsRequestConfiguration(req authcommon.PageRequest, ordered bool) *groups.GroupsRequestBuilderGetRequestConfiguration {
	params := &groups.GroupsRequestBuilderGetQueryParameters{
		Top: new(int32(req.Limit)),
		// Only these two fields are used; the default group object is far larger.
		Select: []string{"id", "displayName"},
	}

	if ordered {
		params.Orderby = []string{"displayName"}
	}

	if req.NameFilter != "" {
		// Graph offers a prefix match here. A contains-style match would mean $search, which needs
		// the advanced query header; see the note on FetchGroupPage.
		params.Filter = new(fmt.Sprintf("startswith(displayName,'%s')", escapeODataString(req.NameFilter)))
	}

	return &groups.GroupsRequestBuilderGetRequestConfiguration{QueryParameters: params}
}

// groupPageFrom converts a Graph collection response into one page of groups.
func groupPageFrom(result models.GroupCollectionResponseable) authcommon.PageResult {
	if result == nil {
		return authcommon.PageResult{Items: state.GroupInfoList{}}
	}

	values := result.GetValue()
	groupInfos := make(state.GroupInfoList, 0, len(values))
	for _, group := range values {
		if groupInfo := convertToGroupInfo(group); groupInfo != nil {
			groupInfos = append(groupInfos, *groupInfo)
		}
	}

	var nextCursor string
	if link := result.GetOdataNextLink(); link != nil {
		nextCursor = compactNextLink(*link)
	}

	return authcommon.PageResult{Items: groupInfos, NextCursor: nextCursor}
}

// escapeODataString escapes a value for use inside an OData string literal, where a single quote
// is written twice.
func escapeODataString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// compactNextLink strips the constant part of a Graph next link. Links from a national cloud have
// a different host, so those are carried whole.
func compactNextLink(link string) string {
	return strings.TrimPrefix(link, graphGroupsURLPrefix)
}

// expandNextLink restores a cursor produced by compactNextLink to a full URL. A cursor that is
// already absolute is one compactNextLink did not recognize, such as a link from a national cloud.
//
// The result is handed to a request builder that will fetch whatever URL it is given, and a cursor
// is only signed to the extent that its envelope is, so the URL is checked against the Graph hosts
// and group paths before it is used. Anything else is a bad cursor rather than a request to make.
func expandNextLink(cursor string) (string, error) {
	link := cursor
	if !strings.Contains(link, "://") {
		link = graphGroupsURLPrefix + link
	}

	parsed, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("%w: group page link is not a URL", authcommon.ErrInvalidCursor)
	}

	if parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: group page link is not https", authcommon.ErrInvalidCursor)
	}
	if parsed.User != nil {
		// Graph never mints a link with credentials in it, and carrying them into the request is
		// not something a cursor gets to ask for.
		return "", fmt.Errorf("%w: group page link carries credentials", authcommon.ErrInvalidCursor)
	}
	if _, ok := graphHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return "", fmt.Errorf("%w: group page link does not point at Microsoft Graph", authcommon.ErrInvalidCursor)
	}
	if _, ok := graphGroupsPaths[strings.TrimSuffix(parsed.Path, "/")]; !ok {
		return "", fmt.Errorf("%w: group page link is not a group listing", authcommon.ErrInvalidCursor)
	}

	return parsed.String(), nil
}

func classifyNextLinkError(ctx context.Context, err error) error {
	wrapped := fmt.Errorf("failed to follow group page link: %w", err)
	if ctx.Err() != nil {
		return wrapped
	}

	var odataErr *odataerrors.ODataError
	if !errors.As(err, &odataErr) {
		return wrapped
	}

	switch odataErr.ResponseStatusCode {
	case http.StatusBadRequest, http.StatusNotFound:
		// Graph rejected the link, which is what an expired or malformed skip token looks like.
		// Report it as a bad cursor so the caller restarts from the first page.
		return fmt.Errorf("%w: failed to follow group page link: %v", authcommon.ErrInvalidCursor, err)
	default:
		return wrapped
	}
}

// FetchUserGroupInfos retrieves all groups the specified user belongs to.
// Uses transitive membership to include nested groups.
// Requires application permission User.Read.All.
func FetchUserGroupInfos(ctx context.Context, client *msgraphsdkgo.GraphServiceClient, userID string) (state.GroupInfoList, error) {
	cfg := &users.ItemTransitiveMemberOfRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemTransitiveMemberOfRequestBuilderGetQueryParameters{
			Top: new(int32(graphPageSize)),
		},
	}

	// Query user-specific endpoint with user ID (GUID or UPN/email)
	result, err := client.Users().ByUserId(userID).TransitiveMemberOf().Get(ctx, cfg)
	if err != nil || result == nil || len(result.GetValue()) == 0 {
		// The user is likely a guest user if that did not work, so look up their email to get the proper user ID.
		newUser, lookupErr := lookupGuestByEmail(ctx, client, userID)
		if lookupErr != nil {
			return nil, fmt.Errorf("failed to lookup user %s: %w", userID, lookupErr)
		}

		if newUser.GetId() == nil {
			// probably impossible
			return nil, fmt.Errorf("user %s has no id", userID)
		}

		result, err = client.Users().ByUserId(*newUser.GetId()).TransitiveMemberOf().Get(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch user infos for user %s: %w", userID, err)
		}
	}

	pageIterator, err := msgraphcore.NewPageIterator[models.DirectoryObjectable](
		result, client.GetAdapter(), models.CreateDirectoryObjectCollectionResponseFromDiscriminatorValue,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create membership page iterator for user %s: %w", userID, err)
	}

	groupInfos := make(state.GroupInfoList, 0, graphPageSize)
	if err := pageIterator.Iterate(ctx, func(item models.DirectoryObjectable) bool {
		// TransitiveMemberOf also returns directory roles and administrative units; keep only groups.
		if group, ok := item.(*models.Group); ok {
			if groupInfo := convertToGroupInfo(group); groupInfo != nil {
				groupInfos = append(groupInfos, *groupInfo)
			}
		}
		if len(groupInfos) >= maxGroups {
			slog.Warn("Reached the maximum number of Entra groups that can be listed for a user; some groups were not loaded", "userID", userID, "groupLimit", maxGroups)
			return false
		}
		return true
	}); err != nil {
		return nil, fmt.Errorf("failed to page through memberships for user %s: %w", userID, err)
	}

	return groupInfos, nil
}

func lookupGuestByEmail(ctx context.Context, client *msgraphsdkgo.GraphServiceClient, email string) (models.Userable, error) {
	cfg := &users.UsersRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.UsersRequestBuilderGetQueryParameters{
			Filter: new(fmt.Sprintf("mail eq '%[1]s' or userPrincipalName eq '%[1]s'", email)),
		},
	}

	result, err := client.Users().Get(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("searching for guest by email: %w", err)
	}

	if len(result.GetValue()) == 0 {
		return nil, fmt.Errorf("no user found for email %s", email)
	}

	return result.GetValue()[0], nil
}

// convertToGroupInfo converts a Microsoft Graph Group model to our GroupInfo structure.
func convertToGroupInfo(group models.Groupable) *state.GroupInfo {
	id := getValue(group.GetId())
	if id == "" {
		return nil // Skip groups without IDs
	}

	name := getValue(group.GetDisplayName())
	if name == "" {
		name = "entra/" + id // Fallback if display name unavailable
	}

	groupInfo := &state.GroupInfo{
		ID:   "entra/" + id, // Add namespace prefix
		Name: name,
	}

	return groupInfo
}

// GetUser returns the authenticated user's id, name, and icon URL (data URL).
// Photo is fetched cache-first, then from the API on miss and cached.
func GetUserInfo(ctx context.Context, client *msgraphsdkgo.GraphServiceClient) (*UserInfo, error) {
	user, err := client.Me().Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get user profile: %w", err)
	}

	var (
		userID = getValue(user.GetId())
		name   = getValue(user.GetDisplayName())
	)
	if name == "" {
		name = userID
	}

	info := &UserInfo{
		ID:   userID,
		Name: name,
	}

	// Cache-first lookup for photo
	cacheKey := fmt.Sprintf("user:%s", userID)
	if cachedPhoto, found := photoCache.Get(cacheKey); found {
		// Cache hit
		info.IconURL = cachedPhoto
	} else if dataURL := fetchUserPhoto(ctx, client); dataURL != "" {
		// Cache miss, fetch from API and cache
		photoCache.Add(cacheKey, dataURL)
		info.IconURL = dataURL
	}

	return info, nil
}

// fetchUserPhoto retrieves user photo directly from Microsoft Graph.
func fetchUserPhoto(ctx context.Context, client *msgraphsdkgo.GraphServiceClient) string {
	data, err := client.Me().Photo().Content().Get(ctx, nil)
	if err != nil || len(data) == 0 {
		return ""
	}

	return createPhotoDataURL(data)
}

// createPhotoDataURL converts binary image data to a base64 data URL.
// Detects image format and sets appropriate MIME type.
func createPhotoDataURL(data []byte) string {
	// Default to JPEG, detect PNG by magic bytes
	contentType := "image/jpeg"
	if len(data) >= 3 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E {
		contentType = "image/png"
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", contentType, encoded)
}

func getValue[T any](p *T) (v T) {
	if p == nil {
		return v
	}

	return *p
}
