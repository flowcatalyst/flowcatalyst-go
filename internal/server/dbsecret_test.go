package server

import (
	"context"
	"testing"
)

func intPtr(n int) *int { return &n }

func TestBuildDBSecretDSN(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		dbName  string
		envPort string
		sec     dbSecret
		want    string
	}{
		{
			name: "basic with env port default",
			host: "db.internal", dbName: "flowcatalyst", envPort: "",
			sec:  dbSecret{Username: "fc", Password: "p4ss"},
			want: "postgresql://fc:p4ss@db.internal:5432/flowcatalyst",
		},
		{
			name: "DB_PORT env honoured",
			host: "db.internal", dbName: "fc", envPort: "6543",
			sec:  dbSecret{Username: "u", Password: "p"},
			want: "postgresql://u:p@db.internal:6543/fc",
		},
		{
			name: "secret JSON port overrides DB_PORT",
			host: "db.internal", dbName: "fc", envPort: "6543",
			sec:  dbSecret{Username: "u", Password: "p", Port: intPtr(7000)},
			want: "postgresql://u:p@db.internal:7000/fc",
		},
		{
			name: "host already has port → no port appended",
			host: "db.internal:9999", dbName: "fc", envPort: "5432",
			sec:  dbSecret{Username: "u", Password: "p", Port: intPtr(7000)},
			want: "postgresql://u:p@db.internal:9999/fc",
		},
		{
			name: "password is URL-escaped",
			host: "db.internal", dbName: "fc", envPort: "5432",
			sec:  dbSecret{Username: "u", Password: "p@ss:w/rd?+"},
			want: "postgresql://u:p%40ss%3Aw%2Frd%3F%2B@db.internal:5432/fc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildDBSecretDSN(tc.host, tc.dbName, tc.envPort, tc.sec); got != tc.want {
				t.Fatalf("buildDBSecretDSN:\n got  %s\n want %s", got, tc.want)
			}
		})
	}
}

func TestResolveDBSecretURLNotApplicable(t *testing.T) {
	// Explicit URL present → SM never consulted (no AWS call).
	t.Setenv("FC_DATABASE_URL", "postgresql://x@h/db")
	t.Setenv("DB_SECRET_ARN", "arn:aws:secretsmanager:...")
	t.Setenv("DB_HOST", "db.internal")
	if url, ok, err := ResolveDBSecretURL(context.Background()); ok || url != "" || err != nil {
		t.Fatalf("explicit URL must short-circuit SM; got url=%q ok=%v err=%v", url, ok, err)
	}

	// No DB_SECRET_ARN → not applicable.
	t.Setenv("FC_DATABASE_URL", "")
	t.Setenv("DB_SECRET_ARN", "")
	if _, ok, err := ResolveDBSecretURL(context.Background()); ok || err != nil {
		t.Fatalf("no DB_SECRET_ARN must be a no-op; got ok=%v err=%v", ok, err)
	}

	// ARN set but DB_HOST missing → not applicable.
	t.Setenv("DB_SECRET_ARN", "arn:...")
	t.Setenv("DB_HOST", "")
	if _, ok, err := ResolveDBSecretURL(context.Background()); ok || err != nil {
		t.Fatalf("missing DB_HOST must be a no-op; got ok=%v err=%v", ok, err)
	}

	// Unsupported provider → error.
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_SECRET_PROVIDER", "vault")
	if _, _, err := ResolveDBSecretURL(context.Background()); err == nil {
		t.Fatal("non-aws DB_SECRET_PROVIDER must error")
	}
}

func TestNewDBSecretRefresherNotApplicable(t *testing.T) {
	// Explicit URL → no refresher (and no AWS call).
	t.Setenv("FC_DATABASE_URL", "postgresql://x@h/db")
	t.Setenv("DB_SECRET_ARN", "arn:aws:secretsmanager:...")
	t.Setenv("DB_HOST", "db.internal")
	if r, err := NewDBSecretRefresher(context.Background()); r != nil || err != nil {
		t.Fatalf("explicit URL must yield no refresher; got r=%v err=%v", r, err)
	}

	// No DB_SECRET_ARN → not applicable.
	t.Setenv("FC_DATABASE_URL", "")
	t.Setenv("DB_SECRET_ARN", "")
	if r, err := NewDBSecretRefresher(context.Background()); r != nil || err != nil {
		t.Fatalf("no ARN must yield no refresher; got r=%v err=%v", r, err)
	}

	// Rotation disabled (interval 0) → no refresher, even with ARN+host set.
	t.Setenv("DB_SECRET_ARN", "arn:aws:secretsmanager:...")
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_SECRET_REFRESH_INTERVAL_MS", "0")
	if r, err := NewDBSecretRefresher(context.Background()); r != nil || err != nil {
		t.Fatalf("interval 0 must disable rotation; got r=%v err=%v", r, err)
	}
}
