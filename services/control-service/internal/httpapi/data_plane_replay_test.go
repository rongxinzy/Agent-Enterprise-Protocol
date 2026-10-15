package httpapi

import (
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRecoveryReplayHTTPValidationAndAuthentication(t *testing.T) {
	path := publicationPath + "/" + publicationTestID + "/replay"
	for _, body := range []string{"", `null`, `[]`, `{"revision":"draft"}`, `{} {}`, strings.Repeat(" ", 4097) + `{}`} {
		a, _, token := newStoreBackedHTTPApplication(t)
		a.Config.GatewayPublicationsEnabled = true
		r := publicationHTTP(New(a).Handler(), token, "POST", path, body, "key")
		if r.Code != 400 || !strings.Contains(r.Body.String(), "INVALID_REQUEST") {
			t.Fatalf("invalid replay = %d %s", r.Code, r.Body.String())
		}
	}
	a, _, token := newStoreBackedHTTPApplication(t)
	for _, test := range []struct {
		token, key, path string
		enabled          bool
		status           int
	}{
		{"", "key", path, true, 401}, {token, "key", path, false, 503},
		{token, "", path, true, 400}, {token, "key", publicationPath + "/unknown/replay", true, 404},
	} {
		a.Config.GatewayPublicationsEnabled = test.enabled
		r := publicationHTTP(New(a).Handler(), test.token, "POST", test.path, `{}`, test.key)
		if r.Code != test.status {
			t.Fatalf("replay = %d %s", r.Code, r.Body.String())
		}
	}
}

func TestRecoveryReplayHTTPReturnsIdempotentOperation(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		a, mock, token := newStoreBackedHTTPApplication(t)
		a.Config.GatewayPublicationsEnabled = true
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
		hash := publicationDigest([]byte("replay:" + publicationTestID))
		if conflict {
			hash = "different-request"
		}
		mock.ExpectQuery(`SELECT \* FROM "data_plane_publications"`).WithArgs("deployment-a", publicationDigest([]byte("key")), 1).WillReturnRows(sqlmock.NewRows([]string{"operation_id", "deployment_id", "revision", "phase", "request_hash"}).AddRow(publicationTestID, "deployment-a", "selected", "queued", hash))
		if conflict {
			mock.ExpectRollback()
		} else {
			mock.ExpectCommit()
		}
		r := publicationHTTP(New(a).Handler(), token, "POST", publicationPath+"/"+publicationTestID+"/replay", `{}`, "key")
		if conflict {
			if r.Code != 409 || !strings.Contains(r.Body.String(), "IDEMPOTENCY_KEY_CONFLICT") {
				t.Fatalf("replay conflict = %d %s", r.Code, r.Body.String())
			}
		} else if r.Code != 202 || r.Header().Get("Location") != publicationPath+"/"+publicationTestID || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("replay = %d %s", r.Code, r.Body.String())
		}
	}
}
