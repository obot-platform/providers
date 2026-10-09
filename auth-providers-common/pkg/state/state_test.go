package state

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteStateError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "invalid session",
			err:        fmt.Errorf("failed to refresh token: %w", fmt.Errorf("%w: refreshing token returned 401", ErrInvalidSession)),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "other error",
			err:        errors.New("error loading postgres session: connection refused"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteStateError(rec, tt.err)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
