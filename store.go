package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

type Share struct {
	ID      string `json:"id"`
	VideoID string `json:"videoId"`
	Created int64  `json:"created"`
	Revoked bool   `json:"revoked"`
}

type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS shares(id TEXT PRIMARY KEY, video_id TEXT NOT NULL, created INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func (s *Store) create(videoID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	_, err := s.db.Exec("INSERT INTO shares(id,video_id,created) VALUES(?,?,?)", tokenHash(token), videoID, time.Now().Unix())
	return token, err
}
func (s *Store) lookup(token string) (Share, error) {
	var v Share
	if len(token) != 43 {
		return v, sql.ErrNoRows
	}
	err := s.db.QueryRow("SELECT id,video_id,created,revoked FROM shares WHERE id=? AND revoked=0", tokenHash(token)).Scan(&v.ID, &v.VideoID, &v.Created, &v.Revoked)
	return v, err
}
func (s *Store) list() ([]Share, error) {
	rows, err := s.db.Query("SELECT id,video_id,created,revoked FROM shares ORDER BY created DESC LIMIT 1000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Share{}
	for rows.Next() {
		var v Share
		if err = rows.Scan(&v.ID, &v.VideoID, &v.Created, &v.Revoked); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) revoke(id string) error {
	r, err := s.db.Exec("UPDATE shares SET revoked=1 WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("unknown share")
	}
	return nil
}
