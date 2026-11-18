// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/octobud-hq/octobud/backend/internal/db"
	"go.uber.org/zap"
)

func readGitHubCLIToken(ctx context.Context, username string) (string, error) {
	path, err := exec.LookPath("gh")
	if errors.Is(err, exec.ErrNotFound) && runtime.GOOS == darwinOS {
		// Apps launched from Finder may not inherit Homebrew's PATH.
		for _, candidate := range []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh"} {
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
				path, err = candidate, nil
				break
			}
		}
	}
	if err != nil {
		return "", errors.New("GitHub CLI (gh) was not found. Install it and run gh auth login")
	}

	args := []string{"auth", "token", "--hostname", "github.com"}
	if username != "" {
		args = append(args, "--user", username)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// #nosec G204 -- resolves the fixed gh executable and passes arguments directly.
	cmd := exec.CommandContext(ctx, path, args...)
	// Use credentials stored by gh, even when a PAT is set in the environment.
	cmd.Env = append(cmd.Environ(), "GH_TOKEN=", "GITHUB_TOKEN=", "GH_PROMPT_DISABLED=1")
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New(
			"could not read GitHub CLI credentials. Run gh auth login for github.com",
		)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New(
			"GitHub CLI has no stored credentials for this account. Run gh auth login",
		)
	}
	return token, nil
}

// ConnectGitHubCLI reuses the active CLI account and stores its identity only.
func (m *TokenManager) ConnectGitHubCLI(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	token, err := m.readCLIToken(ctx, "")
	if err != nil {
		return "", err
	}
	userID, username, err := m.validateAndGetUser(ctx, token)
	if err != nil {
		return "", errors.New(
			"could not verify the GitHub CLI login. Run gh auth status to check your login",
		)
	}
	if accessErr := m.checkCLINotifications(ctx, token); accessErr != nil {
		return "", accessErr
	}
	_, err = m.store.ConnectUserGitHubCLI(ctx, db.UpdateUserGitHubIdentityParams{
		GithubUserID:   sql.NullString{String: userID, Valid: true},
		GithubUsername: sql.NullString{String: username, Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("save GitHub CLI connection: %w", err)
	}
	m.stopRecovery()
	m.applyCLIToken(token, userID, username)
	return username, nil
}

func (m *TokenManager) restoreGitHubCLI(ctx context.Context, user db.User) error {
	token, err := m.readCLIToken(ctx, user.GithubUsername.String)
	if err != nil {
		return err
	}
	userID, username, err := m.validateAndGetUser(ctx, token)
	if err != nil || user.GithubUserID.String != userID {
		return errors.New("GitHub CLI credentials for the connected account are unavailable")
	}
	m.applyCLIToken(token, userID, username)
	return nil
}

func (m *TokenManager) applyCLIToken(token, userID, username string) {
	m.githubClient.SetValidatedToken(token)
	m.currentToken = token
	m.currentSource = TokenSourceCLI
	m.githubUserID = userID
	m.githubUsername = username
	m.resetHealth()
}

func (m *TokenManager) checkCLINotifications(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://api.github.com/notifications?per_page=1",
		http.NoBody,
	)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return errors.New(
			"could not check GitHub notification access. Check your connection and try again",
		)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			m.logger.Warn("failed to close response body", zap.Error(closeErr))
		}
	}()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return errors.New(
			"the gh credentials cannot read GitHub notifications. Sign in using gh auth login --web",
		)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not check GitHub notification access (HTTP %d)", resp.StatusCode)
	}
	return nil
}
