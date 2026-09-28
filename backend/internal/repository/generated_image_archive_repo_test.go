package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestGeneratedImageRepositoryDuplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WithArgs(generatedImageArchiveLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO generated_images").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	ok, err := NewGeneratedImageRepository(db).Insert(context.Background(), service.GeneratedImage{ID: "0123456789abcdef0123456789abcdef", UserID: 3, ByteSize: 100})
	if ok || err != nil {
		t.Fatalf("duplicate: %t %v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestGeneratedImageRepositoryPrune(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WithArgs(generatedImageArchiveLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("WITH ranked AS").WithArgs(sqlmock.AnyArg(), int64(500)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("0123456789abcdef0123456789abcdef"))
	mock.ExpectCommit()
	ids, err := NewGeneratedImageRepository(db).Prune(context.Background(), time.Now(), 500)
	if err != nil || len(ids) != 1 {
		t.Fatalf("prune: %v %v", ids, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestGeneratedImageRepositoryMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id,user_id").WithArgs("0123456789abcdef0123456789abcdef").WillReturnError(sql.ErrNoRows)
	_, err = NewGeneratedImageRepository(db).Get(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != service.ErrGeneratedImageNotFound {
		t.Fatalf("wrong missing error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
