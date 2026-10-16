package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The protocol promises 400 for malformed bodies. A valid JSON value with
// trailing data is malformed as a request body: accepting it silently would
// let a client believe arbitrary trailing content was processed.
func TestExecute_TrailingDataAfterBodyRejected(t *testing.T) {
	srv, accessToken, _ := newTestServer()
	body := []byte(`{"args":["whoami"]}garbage`)
	req := httptest.NewRequest("POST", "/op/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()

	srv.handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for trailing data after the JSON body", w.Code)
	}
}
