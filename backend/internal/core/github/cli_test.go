// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/octobud-hq/octobud/backend/internal/db"
	dbmocks "github.com/octobud-hq/octobud/backend/internal/db/mocks"
	githubmocks "github.com/octobud-hq/octobud/backend/internal/github/mocks"
)

type cliTestTransport func(*http.Request) (*http.Response, error)

func (f cliTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadGitHubCLIToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX shell")
	}
	bin := t.TempDir()
	const script = `#!/bin/sh
test -z "$GH_TOKEN" && test -z "$GITHUB_TOKEN" || exit 1
test "$1 $2 $3 $4 $5 $6" = "auth token --hostname github.com --user octocat" || exit 1
printf 'fake-cli-token\n'
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700))
	t.Setenv("PATH", bin)
	t.Setenv("GH_TOKEN", "fake-env-token")
	t.Setenv("GITHUB_TOKEN", "fake-fallback-env-token")

	token, err := readGitHubCLIToken(context.Background(), "octocat")
	require.NoError(t, err)
	require.Equal(t, "fake-cli-token", token)
	_, err = readGitHubCLIToken(context.Background(), "other")
	require.ErrorContains(t, err, "could not read GitHub CLI credentials")
}

func TestConnectGitHubCLI(t *testing.T) {
	ctx := context.Background()
	const testToken = "cli-test-token"
	account := db.UpdateUserGitHubIdentityParams{
		GithubUserID:   sql.NullString{String: "42", Valid: true},
		GithubUsername: sql.NullString{String: "octocat", Valid: true},
	}

	for _, test := range []struct {
		name                string
		notificationsStatus int
	}{
		{"connect, restore, and disconnect", http.StatusOK},
		{"reject gh login without notification access", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := dbmocks.NewMockStore(ctrl)
			client := githubmocks.NewMockClient(ctrl)
			client.EXPECT().SetTokenObserver(gomock.Any()).Times(2)
			httpClient := &http.Client{
				Transport: cliTestTransport(func(r *http.Request) (*http.Response, error) {
					require.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))
					status, body := http.StatusOK, `{"id":42,"login":"octocat"}`
					if r.URL.Path == "/notifications" {
						status, body = test.notificationsStatus, "[]"
					} else {
						require.Equal(t, "/user", r.URL.Path)
					}
					return &http.Response{
						StatusCode: status,
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}),
			}

			connect := NewTokenManager(store, nil, nil, client, nil, zap.NewNop())
			connect.httpClient = httpClient
			connect.readCLIToken = func(_ context.Context, username string) (string, error) {
				require.Empty(t, username)
				return testToken, nil
			}
			restore := NewTokenManager(store, nil, nil, client, nil, zap.NewNop())
			restore.httpClient = httpClient
			restore.readCLIToken = func(_ context.Context, username string) (string, error) {
				require.Equal(t, "octocat", username)
				return testToken, nil
			}

			if test.notificationsStatus != http.StatusOK {
				_, err := connect.ConnectGitHubCLI(ctx)
				require.ErrorContains(t, err, "cannot read GitHub notifications")
				require.False(t, connect.IsConnected())
				return
			}

			savedUser := db.User{
				GithubCLIAuth:  true,
				GithubUserID:   account.GithubUserID,
				GithubUsername: account.GithubUsername,
			}
			store.EXPECT().ConnectUserGitHubCLI(ctx, account).Return(savedUser, nil)
			client.EXPECT().SetValidatedToken(testToken).Times(2)
			username, err := connect.ConnectGitHubCLI(ctx)
			require.NoError(t, err)
			require.Equal(t, "octocat", username)
			require.Empty(t, connect.GetStatusMaskedToken())

			store.EXPECT().GetUser(ctx).Return(savedUser, nil).Times(2)
			require.NoError(t, restore.Initialize(ctx))
			require.Equal(t, "gh", restore.GetStatusSource())
			require.True(t, restore.IsConnected())

			store.EXPECT().ClearUserGitHubToken(ctx).Return(db.User{}, nil)
			client.EXPECT().SetValidatedToken("")
			require.NoError(t, restore.ClearToken(ctx))
			require.False(t, restore.IsConnected())
		})
	}
}
