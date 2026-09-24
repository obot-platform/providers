package authcommon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

const MaxGroupIDsPerRequest = 100

var ErrTooManyIDs = errors.New("too many group ids")

type FetchGroupsByIDsFunc func(context.Context, []string) (state.GroupInfoList, error)

type GroupList struct {
	Items state.GroupInfoList `json:"items"`
}

// ParseGroupIDs reads the ids parameter and strips the provider prefix that every group ID carries
// on the Obot side, leaving the identity provider's own IDs.
func ParseGroupIDs(providerKind string, query url.Values) ([]string, error) {
	raw := query.Get("ids")
	if raw == "" {
		return nil, errors.New("ids is required")
	}

	parts := strings.Split(raw, ",")
	if len(parts) > MaxGroupIDsPerRequest {
		return nil, fmt.Errorf("%w: %d ids, limit is %d", ErrTooManyIDs, len(parts), MaxGroupIDsPerRequest)
	}

	var (
		prefix = providerKind + "/"
		ids    = make([]string, 0, len(parts))
		seen   = make(map[string]struct{}, len(parts))
	)

	for _, part := range parts {
		native, ok := strings.CutPrefix(strings.TrimSpace(part), prefix)
		if !ok || native == "" {
			continue
		}
		if _, duplicate := seen[native]; duplicate {
			continue
		}
		seen[native] = struct{}{}
		ids = append(ids, native)
	}

	return ids, nil
}

const resolveLookupConcurrency = 8

// ResolveGroupsByLookup resolves IDs by calling lookup once per ID, for the providers whose APIs
// offer no batch read. Lookups run concurrently up to resolveLookupConcurrency.
func ResolveGroupsByLookup(ctx context.Context, ids []string, lookup func(context.Context, string) (*state.GroupInfo, error)) (state.GroupInfoList, error) {
	if len(ids) == 0 {
		return state.GroupInfoList{}, nil
	}

	var (
		mu       sync.Mutex
		resolved = make(state.GroupInfoList, 0, len(ids))
		firstErr error
		failures int
		wg       sync.WaitGroup
		sem      = make(chan struct{}, min(resolveLookupConcurrency, len(ids)))
	)

	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}

		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			group, err := lookup(ctx, id)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				slog.Debug("failed to resolve group by id", "groupID", id, "error", err)
				failures++
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if group != nil {
				resolved = append(resolved, *group)
			}
		})
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("failed to resolve groups: %w", err)
	}

	if failures == len(ids) {
		return nil, fmt.Errorf("failed to resolve any of the %d requested groups: %w", len(ids), firstErr)
	}

	return resolved, nil
}
