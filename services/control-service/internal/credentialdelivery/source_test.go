package credentialdelivery

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDatabaseSourceScopesAndEligibility(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	source := DatabaseSource{DB: db, Deployment: "deployment-a"}
	filter := `credentials.deployment_id = \$1 AND credentials.enabled = \$2 AND credentials.delivery_mode = \$3 AND credentials.type = \$4.*EXISTS.*m.deployment_id = credentials.deployment_id.*m.credential_id = credentials.id.*m.source_type = 'gateway'.*m.enabled = true.*m.protocol IN`
	mock.ExpectQuery(`SELECT "credentials"\."id" FROM "credentials" WHERE \(`+filter+`.*credentials.id > \$5 ORDER BY credentials.id LIMIT \$6`).
		WithArgs("deployment-a", true, "server_only", "api_key", "cursor", 100).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("credential-a"))
	ids, err := source.IDs(context.Background(), "cursor", 100)
	if err != nil || len(ids) != 1 {
		t.Fatalf("IDs: %v, %v", ids, err)
	}
	for _, mode := range []string{"found", "absent", "failure"} {
		query := mock.ExpectQuery(`SELECT \* FROM "credentials" WHERE \(`+filter+`.*credentials.id = \$5 LIMIT \$6`).WithArgs("deployment-a", true, "server_only", "api_key", "credential-a", 1)
		switch mode {
		case "found":
			query.WillReturnRows(sqlmock.NewRows([]string{"id", "key_id", "nonce", "encrypted_value"}).AddRow("credential-a", "key-version", []byte("nonce"), []byte("sealed")))
		case "absent":
			query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
		case "failure":
			query.WillReturnError(errors.New("database offline"))
		}
		envelope, found, readErr := source.Read(context.Background(), "credential-a")
		if mode == "found" && (readErr != nil || !found || envelope.KeyID != "key-version") {
			t.Fatalf("Read: %v, %v", found, readErr)
		}
		if mode == "absent" && (readErr != nil || found) {
			t.Fatal("absence was not distinguished from error")
		}
		if mode == "failure" && readErr == nil {
			t.Fatal("database error became absence")
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
