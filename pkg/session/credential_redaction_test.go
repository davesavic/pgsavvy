package session_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/jackc/pgx/v5/pgconn"
)

type resolvedCredentialConn struct {
	*fakeConn
	password string
}

func (c *resolvedCredentialConn) RedactSecrets(text string) string {
	return strings.ReplaceAll(text, c.password, "***")
}

type recordedCredentialSession struct {
	*fakeSess
	query models.Query
}

func (s *recordedCredentialSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	s.query = q
	return s.fakeSess.Execute(ctx, q)
}

func (s *recordedCredentialSession) Stream(ctx context.Context, q models.Query) (drivers.RowStream, error) {
	s.query = q
	return s.fakeSess.Stream(ctx, q)
}

func TestResolvedConnectionCredentialLogBoundary(t *testing.T) {
	for _, source := range []string{"inline", "helper", "keyring", "pgpass"} {
		t.Run(source, func(t *testing.T) {
			resetLogEnv(t)
			secret := "synthetic-" + source + "-credential"
			conn := &resolvedCredentialConn{fakeConn: &fakeConn{}, password: secret}
			fs := &recordedCredentialSession{fakeSess: &fakeSess{id: 42}}
			buf := &syncBuf{}
			s := session.New(conn, fs, session.Options{Logger: slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
			t.Cleanup(func() { _ = s.Close() })
			query := models.Query{SQL: "SELECT '" + secret + "'", Args: []any{secret}}
			fs.executeErr = errors.New("backend diagnostic " + secret)
			_, _ = s.Execute(context.Background(), query)
			if fs.query.SQL != query.SQL || fs.query.Args[0] != secret {
				t.Fatal("redaction changed executable SQL or its parameters")
			}
			fs.executeErr = nil
			fs.streams = []func() drivers.RowStream{func() drivers.RowStream {
				fs.noticeCh <- pgconn.Notice{Severity: "NOTICE", Code: "00000", Message: "server notice " + secret}
				return &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}}
			}}
			rh, err := s.Stream(context.Background(), query)
			if err != nil {
				t.Fatal("synthetic stream did not open")
			}
			if err := rh.Rows().Close(); err != nil {
				t.Fatal("synthetic stream did not settle")
			}
			notices := 0
			for notice := range rh.Notices() {
				notices++
				if notice.Message != "server notice "+secret {
					t.Fatal("log redaction changed the actual routed notice")
				}
			}
			if notices != 1 || strings.Contains(buf.String(), secret) || !strings.Contains(buf.String(), "***") || fs.query.SQL != query.SQL {
				t.Fatal("resolved credential escaped the connection-scoped SQL/notice/error log boundary")
			}
			if s.RedactSecrets("unrelated-credential") != "unrelated-credential" {
				t.Fatal("credential redactor escaped its connection scope")
			}
		})
	}
}
