package authcommon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

type FetchGroupPageFunc func(context.Context, PageRequest) (PageResult, error)

// ListGroupsHandler serves GET /obot-list-auth-groups?name=&limit=&cursor= for a provider that can
// fetch one page natively.
//
// It owns everything that is not provider specific: parameter parsing, cursor encode and decode,
// and the response envelope. Each provider supplies only the fetch, so the four providers cannot
// drift apart in how they page.
//
// providerKind is baked into every cursor this handler mints, so a cursor from one provider is
// rejected if it is ever replayed against another.
func ListGroupsHandler(providerKind string, fetch FetchGroupPageFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := ParseGroupPageRequest(providerKind, r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		result, err := fetch(r.Context(), req)
		if err != nil {
			// A stale continuation token surfaces as an upstream rejection. Report it as a bad
			// cursor so the caller retries from the top instead of treating it as an outage.
			if errors.Is(err, ErrInvalidCursor) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			http.Error(w, fmt.Sprintf("failed to fetch groups: %v", err), http.StatusInternalServerError)
			return
		}

		nextCursor, err := EncodeCursor(providerKind, req.NameFilter, req.Limit, result.NextCursor)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to encode cursor: %v", err), http.StatusInternalServerError)
			return
		}

		items := result.Items
		if items == nil {
			items = state.GroupInfoList{}
		}

		if err := json.NewEncoder(w).Encode(GroupPage{Items: items, NextCursor: nextCursor}); err != nil {
			http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
			return
		}
	}
}

// GetGroupsHandler serves GET /obot-get-auth-groups?ids= for a provider that can resolve group IDs
// to their current names.
func GetGroupsHandler(providerKind string, fetch FetchGroupsByIDsFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ids, err := ParseGroupIDs(providerKind, r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		items := state.GroupInfoList{}
		if len(ids) > 0 {
			if items, err = fetch(r.Context(), ids); err != nil {
				http.Error(w, fmt.Sprintf("failed to resolve groups: %v", err), http.StatusInternalServerError)
				return
			}
			if items == nil {
				items = state.GroupInfoList{}
			}
		}

		if err := json.NewEncoder(w).Encode(GroupList{Items: items}); err != nil {
			http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
			return
		}
	}
}
