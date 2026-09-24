package authcommon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

func TestParseGroupIDs(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    []string
		wantErr bool
	}{
		{
			name:  "single id loses its prefix",
			query: "ids=entra/a",
			want:  []string{"a"},
		},
		{
			name:  "multiple ids",
			query: "ids=entra/a,entra/b",
			want:  []string{"a", "b"},
		},
		{
			name:  "trims whitespace",
			query: "ids= entra/a , entra/b ",
			want:  []string{"a", "b"},
		},
		{
			name:  "drops blanks",
			query: "ids=entra/a,,entra/b,",
			want:  []string{"a", "b"},
		},
		{
			name:  "drops duplicates",
			query: "ids=entra/a,entra/b,entra/a",
			want:  []string{"a", "b"},
		},
		{
			name:  "drops ids belonging to another provider",
			query: "ids=entra/a,okta/b,entra/c",
			want:  []string{"a", "c"},
		},
		{
			name:  "drops an unprefixed id",
			query: "ids=entra/a,bare",
			want:  []string{"a"},
		},
		{
			name:  "drops an id that is only a prefix",
			query: "ids=entra/,entra/a",
			want:  []string{"a"},
		},
		{
			name:  "every id belongs to another provider",
			query: "ids=okta/a,okta/b",
			want:  []string{},
		},
		{
			name:    "missing ids is an error",
			query:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("bad test query: %v", err)
			}

			got, err := ParseGroupIDs("entra", query)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGroupIDs() error = %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("position %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseGroupIDsRejectsOversizedBatch(t *testing.T) {
	ids := make([]string, 0, MaxGroupIDsPerRequest+1)
	for i := range MaxGroupIDsPerRequest + 1 {
		ids = append(ids, fmt.Sprintf("entra/%d", i))
	}

	query := url.Values{"ids": {strings.Join(ids, ",")}}
	if _, err := ParseGroupIDs("entra", query); !errors.Is(err, ErrTooManyIDs) {
		t.Errorf("error = %v, want ErrTooManyIDs; the caller has to chunk rather than be truncated", err)
	}
}

func TestResolveGroupsByLookup(t *testing.T) {
	resolved, err := ResolveGroupsByLookup(t.Context(), []string{"a", "b", "c"}, func(_ context.Context, id string) (*state.GroupInfo, error) {
		return &state.GroupInfo{ID: "entra/" + id, Name: strings.ToUpper(id)}, nil
	})
	if err != nil {
		t.Fatalf("ResolveGroupsByLookup() error = %v", err)
	}

	if len(resolved) != 3 {
		t.Fatalf("len = %d, want 3", len(resolved))
	}
}

func TestResolveGroupsByLookupOmitsDeletedGroups(t *testing.T) {
	resolved, err := ResolveGroupsByLookup(t.Context(), []string{"a", "gone", "c"}, func(_ context.Context, id string) (*state.GroupInfo, error) {
		if id == "gone" {
			// A group deleted upstream is a normal answer, not a failure.
			return nil, nil
		}
		return &state.GroupInfo{ID: "entra/" + id, Name: id}, nil
	})
	if err != nil {
		t.Fatalf("ResolveGroupsByLookup() error = %v", err)
	}

	if len(resolved) != 2 {
		t.Fatalf("len = %d, want 2", len(resolved))
	}
	for _, group := range resolved {
		if group.ID == "entra/gone" {
			t.Error("a group the lookup reported as absent should not appear in the result")
		}
	}
}

func TestResolveGroupsByLookupSurvivesOneFailure(t *testing.T) {
	resolved, err := ResolveGroupsByLookup(t.Context(), []string{"a", "bad", "c"}, func(_ context.Context, id string) (*state.GroupInfo, error) {
		if id == "bad" {
			return nil, errors.New("boom")
		}
		return &state.GroupInfo{ID: "entra/" + id, Name: id}, nil
	})
	if err != nil {
		t.Fatalf("one unreadable group should not cost the caller the others: %v", err)
	}

	if len(resolved) != 2 {
		t.Fatalf("len = %d, want 2", len(resolved))
	}
}

func TestResolveGroupsByLookupReportsATotalFailure(t *testing.T) {
	_, err := ResolveGroupsByLookup(t.Context(), []string{"a", "b"}, func(_ context.Context, _ string) (*state.GroupInfo, error) {
		return nil, errors.New("boom")
	})
	if err == nil {
		t.Error("every lookup failing is an outage and should be reported, not returned as an empty result")
	}
}

func TestResolveGroupsByLookupBoundsConcurrency(t *testing.T) {
	ids := make([]string, 0, 50)
	for i := range 50 {
		ids = append(ids, fmt.Sprintf("%d", i))
	}

	var (
		mu      sync.Mutex
		current int
		peak    int
	)

	if _, err := ResolveGroupsByLookup(t.Context(), ids, func(_ context.Context, id string) (*state.GroupInfo, error) {
		mu.Lock()
		current++
		peak = max(peak, current)
		mu.Unlock()

		defer func() {
			mu.Lock()
			current--
			mu.Unlock()
		}()

		return &state.GroupInfo{ID: "entra/" + id, Name: id}, nil
	}); err != nil {
		t.Fatalf("ResolveGroupsByLookup() error = %v", err)
	}

	if peak > resolveLookupConcurrency {
		t.Errorf("peak concurrency = %d, want at most %d", peak, resolveLookupConcurrency)
	}
}

func TestGetGroupsHandler(t *testing.T) {
	handler := GetGroupsHandler("entra", func(_ context.Context, ids []string) (state.GroupInfoList, error) {
		groups := make(state.GroupInfoList, 0, len(ids))
		for _, id := range ids {
			groups = append(groups, state.GroupInfo{ID: "entra/" + id, Name: "Group " + id})
		}
		return groups, nil
	})

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/obot-get-auth-groups?ids=entra/a,entra/b", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var body GroupList
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("len = %d, want 2", len(body.Items))
	}
	if body.Items[0].ID != "entra/a" {
		t.Errorf("id = %q, want entra/a; the handler must hand back prefixed ids", body.Items[0].ID)
	}
}

func TestGetGroupsHandlerWithNoIDsForThisProvider(t *testing.T) {
	handler := GetGroupsHandler("entra", func(_ context.Context, _ []string) (state.GroupInfoList, error) {
		t.Error("the fetch should not run when no id belongs to this provider")
		return nil, nil
	})

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/obot-get-auth-groups?ids=okta/a", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	// An empty list, not null: a caller that decodes this should get a usable slice.
	if body := strings.TrimSpace(recorder.Body.String()); body != `{"items":[]}` {
		t.Errorf("body = %s, want {\"items\":[]}", body)
	}
}

func TestGetGroupsHandlerRejectsAMissingIDsParameter(t *testing.T) {
	handler := GetGroupsHandler("entra", func(_ context.Context, _ []string) (state.GroupInfoList, error) {
		return nil, nil
	})

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/obot-get-auth-groups", nil))

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
