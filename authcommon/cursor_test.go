package authcommon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeCursorRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		providerKind string
		filter       string
		limit        int
		native       string
	}{
		{
			name:         "no filter",
			providerKind: "auth0",
			filter:       "",
			limit:        50,
			native:       "3",
		},
		{
			name:         "with filter",
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
			native:       "00g1a2b3c4",
		},
		{
			name:         "long graph style token",
			providerKind: "entra",
			filter:       "",
			limit:        MaxGroupPageSize,
			native:       "$skiptoken=" + strings.Repeat("X", 512),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := EncodeCursor(tt.providerKind, tt.filter, tt.limit, tt.native)
			if err != nil {
				t.Fatalf("EncodeCursor() error = %v", err)
			}
			if encoded == "" {
				t.Fatal("EncodeCursor() returned an empty cursor for a non-empty token")
			}

			got, err := DecodeCursor(tt.providerKind, tt.filter, tt.limit, encoded)
			if err != nil {
				t.Fatalf("DecodeCursor() error = %v", err)
			}
			if got != tt.native {
				t.Errorf("DecodeCursor() = %q, want %q", got, tt.native)
			}
		})
	}
}

func TestEncodeCursorEmptyTokenIsEmptyCursor(t *testing.T) {
	encoded, err := EncodeCursor("auth0", "", 10, "")
	if err != nil {
		t.Fatalf("EncodeCursor() error = %v", err)
	}
	if encoded != "" {
		t.Errorf("EncodeCursor() = %q, want an empty cursor for an exhausted listing", encoded)
	}
}

func TestDecodeCursorEmptyMeansFirstPage(t *testing.T) {
	got, err := DecodeCursor("auth0", "", 10, "")
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v, want nil for an absent cursor", err)
	}
	if got != "" {
		t.Errorf("DecodeCursor() = %q, want an empty token", got)
	}
}

// encodePayload builds a cursor directly so tests can produce ones the encoder would never mint.
func encodePayload(t *testing.T, payload cursorPayload) string {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestDecodeCursorRejectsUntrustworthyCursors(t *testing.T) {
	valid := cursorPayload{
		Version:           CursorVersion,
		ProviderKind:      "okta",
		FilterFingerprint: FilterFingerprint("eng"),
		Limit:             10,
		MintedAt:          time.Now().Unix(),
		NativeToken:       "00g1",
	}

	tests := []struct {
		name         string
		cursor       string
		providerKind string
		filter       string
		limit        int
	}{
		{
			name:         "wrong version",
			cursor:       encodePayload(t, cursorPayload{Version: CursorVersion + 1, ProviderKind: "okta", FilterFingerprint: FilterFingerprint("eng"), Limit: 10, MintedAt: time.Now().Unix(), NativeToken: "00g1"}),
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
		},
		{
			name:         "wrong provider",
			cursor:       encodePayload(t, valid),
			providerKind: "auth0",
			filter:       "eng",
			limit:        10,
		},
		{
			name:         "filter changed between pages",
			cursor:       encodePayload(t, valid),
			providerKind: "okta",
			filter:       "engineering",
			limit:        10,
		},
		{
			name:         "filter dropped between pages",
			cursor:       encodePayload(t, valid),
			providerKind: "okta",
			filter:       "",
			limit:        10,
		},
		{
			name:         "page size grew between pages",
			cursor:       encodePayload(t, valid),
			providerKind: "okta",
			filter:       "eng",
			limit:        100,
		},
		{
			name:         "page size shrank between pages",
			cursor:       encodePayload(t, valid),
			providerKind: "okta",
			filter:       "eng",
			limit:        5,
		},
		{
			name:         "expired",
			cursor:       encodePayload(t, cursorPayload{Version: CursorVersion, ProviderKind: "okta", FilterFingerprint: FilterFingerprint("eng"), Limit: 10, MintedAt: time.Now().Add(-CursorTTL - time.Minute).Unix(), NativeToken: "00g1"}),
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
		},
		{
			name:         "no continuation token",
			cursor:       encodePayload(t, cursorPayload{Version: CursorVersion, ProviderKind: "okta", FilterFingerprint: FilterFingerprint("eng"), Limit: 10, MintedAt: time.Now().Unix()}),
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
		},
		{
			name:         "not base64",
			cursor:       "not!valid!base64",
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
		},
		{
			name:         "not json",
			cursor:       base64.RawURLEncoding.EncodeToString([]byte("plain text")),
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
		},
		{
			name:         "oversized",
			cursor:       strings.Repeat("a", MaxCursorBytes+1),
			providerKind: "okta",
			filter:       "eng",
			limit:        10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeCursor(tt.providerKind, tt.filter, tt.limit, tt.cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Errorf("DecodeCursor() error = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestFilterFingerprintDistinguishesFilters(t *testing.T) {
	if FilterFingerprint("") != "" {
		t.Error("FilterFingerprint(\"\") should be empty so an unfiltered cursor carries no fingerprint")
	}
	if FilterFingerprint("eng") != FilterFingerprint("eng") {
		t.Error("FilterFingerprint should be stable for the same filter")
	}
	if FilterFingerprint("eng") == FilterFingerprint("Eng") {
		t.Error("FilterFingerprint should distinguish filters that differ only by case, since the upstream filters do")
	}
	if strings.Contains(FilterFingerprint("secret-team"), "secret") {
		t.Error("FilterFingerprint should not leak the filter text")
	}
}
