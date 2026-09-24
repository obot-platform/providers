package authcommon

import (
	"net/url"
	"strconv"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

// PageRequest is one page of a group listing, as asked for by Obot. The cursor has already been
// unwrapped, so it holds whatever continuation token the identity provider itself understands.
type PageRequest struct {
	// NameFilter, when set, restricts the listing to groups matching it. What "matching" means is
	// up to the identity provider: a prefix match on Entra and Okta, a substring match on Auth0
	// and JumpCloud.
	NameFilter string

	Limit int

	// Cursor is the identity provider's own continuation token. Empty means the first page.
	Cursor string
}

// PageResult is one page as returned by the identity provider.
type PageResult struct {
	Items state.GroupInfoList

	// NextCursor is the identity provider's continuation token for the following page. Empty means
	// the listing is exhausted.
	//
	// This is the only signal of whether more pages exist. In particular it is not safe to infer
	// that from len(Items) == Limit: a provider may drop entries from a page after fetching it, so
	// a short page can still have a successor.
	NextCursor string
}

// GroupPage is the JSON body returned to Obot.
type GroupPage struct {
	Items      state.GroupInfoList `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// ParseGroupPageRequest reads the pagination parameters and unwraps the cursor. A cursor that
// cannot be trusted comes back as ErrInvalidCursor, which the handler turns into a 400 so that
// Obot restarts at the first page rather than serving an incoherent one.
func ParseGroupPageRequest(providerKind string, query url.Values) (PageRequest, error) {
	req := PageRequest{NameFilter: query.Get("name")}

	limit, err := strconv.Atoi(query.Get("limit"))
	if err != nil || limit <= 0 {
		limit = DefaultGroupPageSize
	}
	req.Limit = min(limit, MaxGroupPageSize)

	if req.Cursor, err = DecodeCursor(providerKind, req.NameFilter, req.Limit, query.Get("cursor")); err != nil {
		return PageRequest{}, err
	}

	return req, nil
}
