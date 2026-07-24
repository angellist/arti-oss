package obo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres-backed Store. It envelope-encrypts the secret fields
// (client secret, access/refresh tokens) with the column AEAD before they touch
// the database, so a DB compromise yields only ciphertext.
type PGStore struct {
	pool   *pgxpool.Pool
	cipher *Cipher
}

func NewPGStore(pool *pgxpool.Pool, cipher *Cipher) *PGStore {
	return &PGStore{pool: pool, cipher: cipher}
}

func (s *PGStore) GetClient(ctx context.Context, issuer string) (ClientReg, bool, error) {
	var id string
	var secEnc []byte
	err := s.pool.QueryRow(ctx,
		`SELECT client_id, client_secret_enc FROM oauth_client_registrations WHERE issuer=$1`,
		issuer,
	).Scan(&id, &secEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientReg{}, false, nil
	}
	if err != nil {
		return ClientReg{}, false, err
	}
	sec, err := s.cipher.OpenCol(secEnc)
	if err != nil {
		return ClientReg{}, false, err
	}
	return ClientReg{ClientID: id, ClientSecret: sec}, true, nil
}

func (s *PGStore) PutClient(ctx context.Context, issuer string, c ClientReg) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_client_registrations (issuer, client_id, client_secret_enc)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (issuer) DO NOTHING`,
		issuer, c.ClientID, s.cipher.SealCol(c.ClientSecret),
	)
	return err
}

func (s *PGStore) GetToken(ctx context.Context, email, resource string) (TokenRec, bool, error) {
	var accEnc, refEnc []byte
	var exp time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT access_token_enc, refresh_token_enc, expiry
		 FROM oauth_obo_tokens WHERE email=$1 AND resource=$2`,
		email, resource,
	).Scan(&accEnc, &refEnc, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return TokenRec{}, false, nil
	}
	if err != nil {
		return TokenRec{}, false, err
	}
	acc, err := s.cipher.OpenCol(accEnc)
	if err != nil {
		return TokenRec{}, false, err
	}
	ref, err := s.cipher.OpenCol(refEnc)
	if err != nil {
		return TokenRec{}, false, err
	}
	return TokenRec{Access: acc, Refresh: ref, Expiry: exp}, true, nil
}

func (s *PGStore) PutToken(ctx context.Context, email, resource string, t TokenRec) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oauth_obo_tokens
		   (email, resource, access_token_enc, refresh_token_enc, expiry, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (email, resource) DO UPDATE SET
		   access_token_enc  = EXCLUDED.access_token_enc,
		   refresh_token_enc = EXCLUDED.refresh_token_enc,
		   expiry            = EXCLUDED.expiry,
		   updated_at        = now()`,
		email, resource, s.cipher.SealCol(t.Access), s.cipher.SealCol(t.Refresh), t.Expiry,
	)
	return err
}
