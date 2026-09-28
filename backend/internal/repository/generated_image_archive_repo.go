package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type generatedImageRepository struct{ db *sql.DB }

func NewGeneratedImageRepository(db *sql.DB) service.GeneratedImageRepository {
	return &generatedImageRepository{db: db}
}

// Advisory lock serializes capacity accounting and pruning across application instances.
const generatedImageArchiveLock = 8172755

func (r *generatedImageRepository) Insert(ctx context.Context, v service.GeneratedImage) (bool, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generatedImageArchiveLock); e != nil {
		return false, e
	}
	var id string
	e = tx.QueryRowContext(ctx, `INSERT INTO generated_images (id,user_id,api_key_id,group_id,account_id,model,request_id,endpoint,mime_type,byte_size,width,height,sha256,created_at,expires_at)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15
 WHERE (SELECT COALESCE(SUM(byte_size),0) FROM generated_images) + $10::bigint <= $16::bigint
 ON CONFLICT (user_id,request_id,sha256) DO NOTHING RETURNING id`, v.ID, v.UserID, v.APIKeyID, v.GroupID, v.AccountID, v.Model, v.RequestID, v.Endpoint, v.MIMEType, v.ByteSize, v.Width, v.Height, v.SHA256, v.CreatedAt, v.ExpiresAt, int64(10<<30)).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return true, nil
}
func (r *generatedImageRepository) List(ctx context.Context, f service.GeneratedImageFilter) (service.GeneratedImageList, error) {
	out := service.GeneratedImageList{Items: []service.GeneratedImage{}}
	clauses := []string{"g.expires_at > NOW()"}
	args := []any{}
	if f.UserID > 0 {
		args = append(args, f.UserID)
		clauses = append(clauses, fmt.Sprintf("g.user_id = $%d", len(args)))
	}
	if f.Model != "" {
		args = append(args, "%"+strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(f.Model, `\`, `\\`), `%`, `\%`), `_`, `\_`)+"%")
		clauses = append(clauses, fmt.Sprintf(`g.model ILIKE $%d ESCAPE '\'`, len(args)))
	}
	if f.StartDate != nil {
		args = append(args, *f.StartDate)
		clauses = append(clauses, fmt.Sprintf("g.created_at >= $%d", len(args)))
	}
	if f.EndDate != nil {
		args = append(args, *f.EndDate)
		clauses = append(clauses, fmt.Sprintf("g.created_at < $%d", len(args)))
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM generated_images g"+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := r.db.QueryContext(ctx, `SELECT g.id,g.user_id,COALESCE(u.email,''),g.group_id,COALESCE(gr.name,''),g.api_key_id,g.account_id,g.model,g.request_id,g.endpoint,g.mime_type,g.byte_size,g.width,g.height,g.sha256,g.created_at,g.expires_at
 FROM generated_images g LEFT JOIN users u ON u.id=g.user_id LEFT JOIN groups gr ON gr.id=g.group_id`+where+fmt.Sprintf(" ORDER BY g.created_at DESC,g.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v service.GeneratedImage
		if err = rows.Scan(&v.ID, &v.UserID, &v.UserEmail, &v.GroupID, &v.GroupName, &v.APIKeyID, &v.AccountID, &v.Model, &v.RequestID, &v.Endpoint, &v.MIMEType, &v.ByteSize, &v.Width, &v.Height, &v.SHA256, &v.CreatedAt, &v.ExpiresAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}
func (r *generatedImageRepository) Get(ctx context.Context, id string) (service.GeneratedImage, error) {
	var v service.GeneratedImage
	err := r.db.QueryRowContext(ctx, `SELECT id,user_id,api_key_id,group_id,account_id,model,request_id,endpoint,mime_type,byte_size,width,height,sha256,created_at,expires_at FROM generated_images WHERE id=$1`, id).Scan(&v.ID, &v.UserID, &v.APIKeyID, &v.GroupID, &v.AccountID, &v.Model, &v.RequestID, &v.Endpoint, &v.MIMEType, &v.ByteSize, &v.Width, &v.Height, &v.SHA256, &v.CreatedAt, &v.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, service.ErrGeneratedImageNotFound
	}
	return v, err
}
func (r *generatedImageRepository) Delete(ctx context.Context, id string) (bool, error) {
	v, e := r.db.ExecContext(ctx, "DELETE FROM generated_images WHERE id=$1", id)
	if e != nil {
		return false, e
	}
	n, e := v.RowsAffected()
	return n > 0, e
}
func (r *generatedImageRepository) HasID(ctx context.Context, id string) (bool, error) {
	var yes bool
	e := r.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM generated_images WHERE id=$1)", id).Scan(&yes)
	return yes, e
}
func (r *generatedImageRepository) Prune(ctx context.Context, now time.Time, maxBytes int64) ([]string, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generatedImageArchiveLock); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `WITH ranked AS (
 SELECT id,expires_at,SUM(CASE WHEN expires_at > $1 THEN byte_size ELSE 0 END) OVER (ORDER BY created_at DESC,id DESC) AS used_bytes FROM generated_images
 ), removed AS (DELETE FROM generated_images WHERE id IN (SELECT id FROM ranked WHERE expires_at <= $1 OR used_bytes > $2) RETURNING id)
 SELECT id FROM removed`, now, maxBytes)
	if e != nil {
		return nil, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return ids, nil
}
