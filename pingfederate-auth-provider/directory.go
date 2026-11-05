package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

const (
	defaultListLimit = 50
	maxListLimit     = 500
	maxSeenGroups    = 10000
)

// groupDirectory stands in for the group directory PingFederate does not
// expose. It holds the groups an administrator declared up front, so they can
// be given roles in Obot before any member has signed in, plus every group
// seen in a token since the provider started.
type groupDirectory struct {
	mu    sync.Mutex
	names map[string]struct{}
	// order remembers insertion order so that, when the directory is full,
	// the oldest sign-in groups are dropped first. Declared groups are never
	// dropped.
	order    []string
	declared map[string]struct{}
}

func newGroupDirectory(known []string) *groupDirectory {
	d := &groupDirectory{names: map[string]struct{}{}, declared: map[string]struct{}{}}
	for _, name := range known {
		if name = strings.TrimSpace(name); name != "" {
			d.names[name] = struct{}{}
			d.declared[name] = struct{}{}
		}
	}
	return d
}

// remember records groups seen in a user's claims.
func (d *groupDirectory) remember(groups []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range groups {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if _, ok := d.names[name]; ok {
			continue
		}
		d.names[name] = struct{}{}
		d.order = append(d.order, name)
		for len(d.order) > maxSeenGroups {
			delete(d.names, d.order[0])
			d.order = d.order[1:]
		}
	}
}

// list returns one page of groups whose name contains filter, sorted by
// name. The cursor is the last name of the previous page.
func (d *groupDirectory) list(filter string, limit int, cursor string) (state.GroupInfoList, string) {
	d.mu.Lock()
	names := make([]string, 0, len(d.names))
	for name := range d.names {
		if filter == "" || strings.Contains(strings.ToLower(name), strings.ToLower(filter)) {
			names = append(names, name)
		}
	}
	d.mu.Unlock()

	sort.Slice(names, func(i, j int) bool {
		if a, b := strings.ToLower(names[i]), strings.ToLower(names[j]); a != b {
			return a < b
		}
		return names[i] < names[j]
	})

	start := 0
	if cursor != "" {
		start = sort.Search(len(names), func(i int) bool {
			if a, b := strings.ToLower(names[i]), strings.ToLower(cursor); a != b {
				return a > b
			}
			return names[i] > cursor
		})
	}
	end := min(start+limit, len(names))
	page := groupInfos(names[start:end])
	next := ""
	if end < len(names) {
		next = names[end-1]
	}
	return page, next
}

// listAuthGroups serves Obot's group search, which backs the group picker in
// the admin UI: GET /obot-list-auth-groups?name=<filter>&limit=<n>&cursor=<c>.
func (s *server) listAuthGroups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultListLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		if parsed > 0 {
			limit = min(parsed, maxListLimit)
		}
	}
	items, next := s.directory.list(q.Get("name"), limit, q.Get("cursor"))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Items      state.GroupInfoList `json:"items"`
		NextCursor string              `json:"nextCursor,omitempty"`
	}{items, next}); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
	}
}

// getAuthGroups resolves group IDs to groups: GET /obot-get-auth-groups?ids=a,b.
// A group's name is its ID without the prefix, so every ID in this provider's
// namespace resolves, whether or not it has been seen.
func (s *server) getAuthGroups(w http.ResponseWriter, r *http.Request) {
	var names []string
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(id), groupIDPrefix); ok && name != "" {
			names = append(names, name)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Items state.GroupInfoList `json:"items"`
	}{groupInfos(names)}); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
	}
}
