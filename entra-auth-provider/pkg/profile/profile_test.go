package profile

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/microsoftgraph/msgraph-sdk-go/models/odataerrors"
	"github.com/obot-platform/providers/authcommon"
)

func TestExpandNextLinkAcceptsGraphLinks(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
		want   string
	}{
		{
			name:   "compacted link is restored",
			cursor: "$skiptoken=abc123",
			want:   graphGroupsURLPrefix + "$skiptoken=abc123",
		},
		{
			name:   "absolute global cloud link",
			cursor: "https://graph.microsoft.com/v1.0/groups?$skiptoken=abc123",
			want:   "https://graph.microsoft.com/v1.0/groups?$skiptoken=abc123",
		},
		{
			name:   "national cloud link",
			cursor: "https://microsoftgraph.chinacloudapi.cn/v1.0/groups?$skiptoken=abc123",
			want:   "https://microsoftgraph.chinacloudapi.cn/v1.0/groups?$skiptoken=abc123",
		},
		{
			name:   "beta endpoint",
			cursor: "https://graph.microsoft.com/beta/groups?$skiptoken=abc123",
			want:   "https://graph.microsoft.com/beta/groups?$skiptoken=abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandNextLink(tt.cursor)
			if err != nil {
				t.Fatalf("expandNextLink() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("expandNextLink() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A cursor is not a signed capability, so the URL inside one is attacker-chosen. Following it
// unchecked would make the provider issue a request wherever the caller pointed it.
func TestExpandNextLinkRejectsForeignLinks(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{
			name:   "attacker controlled host",
			cursor: "https://evil.example.com/v1.0/groups?$skiptoken=abc123",
		},
		{
			name:   "graph host as a userinfo prefix",
			cursor: "https://graph.microsoft.com@evil.example.com/v1.0/groups",
		},
		{
			name:   "graph host as a subdomain of another host",
			cursor: "https://graph.microsoft.com.evil.example.com/v1.0/groups",
		},
		{
			name:   "credentials smuggled into the link",
			cursor: "https://user:password@graph.microsoft.com/v1.0/groups?$skiptoken=abc123",
		},
		{
			name:   "instance metadata service",
			cursor: "http://169.254.169.254/latest/meta-data/",
		},
		{
			name:   "plaintext graph",
			cursor: "http://graph.microsoft.com/v1.0/groups?$skiptoken=abc123",
		},
		{
			name:   "another graph collection",
			cursor: "https://graph.microsoft.com/v1.0/users?$skiptoken=abc123",
		},
		{
			name:   "not a parseable URL",
			cursor: "https://graph.microsoft.com/v1.0/groups\x7f",
		},
		{
			name:   "unparseable port",
			cursor: "https://graph.microsoft.com:notaport/v1.0/groups",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandNextLink(tt.cursor)
			if !errors.Is(err, authcommon.ErrInvalidCursor) {
				t.Errorf("expandNextLink() error = %v, want ErrInvalidCursor (returned %q)", err, got)
			}
		})
	}
}

// odataError builds the error the SDK returns for a given upstream status.
func odataError(status int) error {
	err := odataerrors.NewODataError()
	err.SetStatusCode(status)

	return err
}

func TestClassifyNextLinkError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		canceled      bool
		wantBadCursor bool
	}{
		{
			name:          "graph rejected the link",
			err:           odataError(http.StatusBadRequest),
			wantBadCursor: true,
		},
		{
			name:          "link no longer exists",
			err:           odataError(http.StatusNotFound),
			wantBadCursor: true,
		},
		{
			name:          "throttled",
			err:           odataError(http.StatusTooManyRequests),
			wantBadCursor: false,
		},
		{
			name:          "upstream outage",
			err:           odataError(http.StatusServiceUnavailable),
			wantBadCursor: false,
		},
		{
			name:          "credentials rejected",
			err:           odataError(http.StatusForbidden),
			wantBadCursor: false,
		},
		{
			name:          "transport failure",
			err:           errors.New("dial tcp: i/o timeout"),
			wantBadCursor: false,
		},
		{
			name:          "request canceled",
			err:           context.Canceled,
			canceled:      true,
			wantBadCursor: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}

			err := classifyNextLinkError(ctx, tt.err)
			if err == nil {
				t.Fatal("classifyNextLinkError() = nil, want an error")
			}
			if got := errors.Is(err, authcommon.ErrInvalidCursor); got != tt.wantBadCursor {
				t.Errorf("errors.Is(err, ErrInvalidCursor) = %t, want %t (err = %v)", got, tt.wantBadCursor, err)
			}
		})
	}
}
