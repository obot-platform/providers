package authcommon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

const (
	// CursorVersion is the wire format version of an encoded cursor. Bump it when the payload
	// changes shape so that stale cursors are rejected instead of misread.
	CursorVersion = 1

	// CursorTTL bounds how long a cursor stays usable. Some identity providers expire their own
	// continuation tokens (Microsoft Graph skip tokens in particular), so a cursor that has been
	// sitting in a browser tab is more likely to fail upstream than to work.
	CursorTTL = 15 * time.Minute

	// MaxCursorBytes caps the encoded cursor. Graph next links are the large case; this leaves
	// room for them while keeping a malicious caller from handing us an unbounded string to decode.
	MaxCursorBytes = 4096
)

var ErrInvalidCursor = errors.New("invalid cursor")

// cursorPayload is the decoded form of a cursor. The JSON names are single letters because the
// encoded cursor travels in a query string and Graph next links are already long.
type cursorPayload struct {
	Version           int    `json:"v"`
	ProviderKind      string `json:"p"`
	FilterFingerprint string `json:"f,omitempty"`

	// Limit is the page size the cursor was minted for. Some native continuation tokens only mean
	// what they say at a fixed page size: an Auth0 cursor is a page number, so page 1 is roles
	// 10-19 at a limit of 10 and roles 100-199 at a limit of 100.
	Limit int `json:"l"`

	// MintedAt is a Unix timestamp, used to expire a cursor that has been sitting around.
	MintedAt int64 `json:"t"`

	// NativeToken is the identity provider's own continuation token.
	NativeToken string `json:"n"`
}

// FilterFingerprint reduces a name filter to a short token used to detect that a cursor is being
// replayed against a different search. Only equality matters, so a short non-cryptographic hash is
// enough and keeps the cursor small. It is not a privacy control.
func FilterFingerprint(nameFilter string) string {
	if nameFilter == "" {
		return ""
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(nameFilter))

	return fmt.Sprintf("%x", h.Sum64())
}

// EncodeCursor wraps an identity-provider continuation token in the envelope that binds it to a
// provider, a filter, and a page size.
func EncodeCursor(providerKind, nameFilter string, limit int, native string) (string, error) {
	if native == "" {
		return "", nil
	}

	payload, err := json.Marshal(cursorPayload{
		Version:           CursorVersion,
		ProviderKind:      providerKind,
		FilterFingerprint: FilterFingerprint(nameFilter),
		Limit:             limit,
		MintedAt:          time.Now().Unix(),
		NativeToken:       native,
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode cursor: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(encoded) > MaxCursorBytes {
		return "", fmt.Errorf("%w: encoded cursor is %d bytes, limit is %d", ErrInvalidCursor, len(encoded), MaxCursorBytes)
	}

	return encoded, nil
}

// DecodeCursor unwraps a cursor and returns the identity-provider continuation token inside it.
// An empty cursor is not an error: it means "start at the first page".
func DecodeCursor(providerKind, nameFilter string, limit int, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	if len(encoded) > MaxCursorBytes {
		return "", fmt.Errorf("%w: cursor is %d bytes, limit is %d", ErrInvalidCursor, len(encoded), MaxCursorBytes)
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: not valid base64", ErrInvalidCursor)
	}

	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("%w: not valid JSON", ErrInvalidCursor)
	}

	switch {
	case payload.Version != CursorVersion:
		return "", fmt.Errorf("%w: version %d is not %d", ErrInvalidCursor, payload.Version, CursorVersion)
	case payload.ProviderKind != providerKind:
		// A cursor minted by a different provider would be meaningless upstream.
		return "", fmt.Errorf("%w: minted for provider %q, not %q", ErrInvalidCursor, payload.ProviderKind, providerKind)
	case payload.FilterFingerprint != FilterFingerprint(nameFilter):
		// The caller changed the search between pages, so the position no longer means anything.
		return "", fmt.Errorf("%w: minted for a different name filter", ErrInvalidCursor)
	case payload.Limit != limit:
		// The caller changed the page size between pages, which moves where the native token
		// points. See the note on cursorPayload.Limit.
		return "", fmt.Errorf("%w: minted for a page size of %d, not %d", ErrInvalidCursor, payload.Limit, limit)
	case time.Since(time.Unix(payload.MintedAt, 0)) > CursorTTL:
		return "", fmt.Errorf("%w: older than %s", ErrInvalidCursor, CursorTTL)
	case payload.NativeToken == "":
		return "", fmt.Errorf("%w: no continuation token", ErrInvalidCursor)
	}

	return payload.NativeToken, nil
}
