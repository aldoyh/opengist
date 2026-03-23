package actions

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/thomiceli/opengist/internal/db"
	"gorm.io/gorm"
)

const githubAPIBaseURL = "https://api.github.com"
const githubAPIUserAgent = "Opengist"
const githubAPITimeout = 30 * time.Second

// githubGistFile represents a single file within a GitHub Gist response.
type githubGistFile struct {
	Filename string `json:"filename"`
	RawURL   string `json:"raw_url"`
	Size     int64  `json:"size"`
}

// githubGist represents a GitHub Gist returned by the API.
type githubGist struct {
	ID          string                    `json:"id"`
	Description string                    `json:"description"`
	Public      bool                      `json:"public"`
	Files       map[string]githubGistFile `json:"files"`
	UpdatedAt   time.Time                 `json:"updated_at"`
}

func syncGithubGists() {
	log.Info().Msg("Syncing gists from GitHub for all linked users...")

	users, err := db.GetAllUsersWithGithub()
	if err != nil {
		log.Error().Err(err).Msg("Cannot get users with GitHub accounts")
		return
	}

	if len(users) == 0 {
		log.Info().Msg("No users with linked GitHub accounts found")
		return
	}

	for _, user := range users {
		log.Info().Msgf("Syncing GitHub gists for user %s (GitHub: %s)", user.Username, user.GithubLogin)

		ghGists, err := fetchGithubGistsForUser(user.GithubLogin)
		if err != nil {
			log.Error().Err(err).Msgf("Cannot fetch GitHub gists for user %s", user.GithubLogin)
			continue
		}

		for _, ghGist := range ghGists {
			if err := importOrUpdateGithubGist(user, ghGist); err != nil {
				log.Error().Err(err).Msgf("Cannot import/update GitHub gist %s for user %s", ghGist.ID, user.Username)
			}
		}

		log.Info().Msgf("Synced %d GitHub gists for user %s", len(ghGists), user.Username)
	}

	log.Info().Msg("GitHub gist sync complete")
}

// SyncGithubGistsForUser syncs GitHub gists for a single user by their Opengist username.
// It is exported so it can be called directly from the CLI command.
func SyncGithubGistsForUser(username string) error {
	user, err := db.GetUserByUsername(username)
	if err != nil {
		return fmt.Errorf("cannot find user %s: %w", username, err)
	}

	if user.GithubLogin == "" {
		return fmt.Errorf("user %s does not have a linked GitHub account with a stored login", username)
	}

	log.Info().Msgf("Syncing GitHub gists for user %s (GitHub: %s)", user.Username, user.GithubLogin)

	ghGists, err := fetchGithubGistsForUser(user.GithubLogin)
	if err != nil {
		return fmt.Errorf("cannot fetch GitHub gists for user %s: %w", user.GithubLogin, err)
	}

	for _, ghGist := range ghGists {
		if err := importOrUpdateGithubGist(user, ghGist); err != nil {
			log.Error().Err(err).Msgf("Cannot import/update GitHub gist %s", ghGist.ID)
		}
	}

	log.Info().Msgf("Synced %d GitHub gists for user %s", len(ghGists), user.Username)
	return nil
}

// fetchGithubGistsForUser retrieves all public gists for a GitHub user via the GitHub API.
func fetchGithubGistsForUser(githubLogin string) ([]*githubGist, error) {
	var allGists []*githubGist
	client := &http.Client{Timeout: githubAPITimeout}

	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/users/%s/gists?page=%d&per_page=100", githubAPIBaseURL, githubLogin, page)

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("cannot create request: %w", err)
		}
		req.Header.Set("User-Agent", githubAPIUserAgent)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("cannot fetch gists from GitHub: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("GitHub API returned status %d for user %s", resp.StatusCode, githubLogin)
		}

		var gists []*githubGist
		if err = json.NewDecoder(resp.Body).Decode(&gists); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("cannot decode GitHub gists response: %w", err)
		}
		resp.Body.Close()

		allGists = append(allGists, gists...)

		if len(gists) < 100 {
			break
		}
	}

	return allGists, nil
}

// fetchFileContent downloads the raw content of a GitHub Gist file.
func fetchFileContent(rawURL string) (string, error) {
	client := &http.Client{Timeout: githubAPITimeout}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("cannot create request: %w", err)
	}
	req.Header.Set("User-Agent", githubAPIUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot fetch file content: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub returned status %d for file URL %s", resp.StatusCode, rawURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("cannot read file content: %w", err)
	}

	return string(body), nil
}

// importOrUpdateGithubGist creates a new Opengist gist or updates an existing one
// based on the GitHub gist data.
func importOrUpdateGithubGist(user *db.User, ghGist *githubGist) error {
	if len(ghGist.Files) == 0 {
		log.Debug().Msgf("Skipping GitHub gist %s: no files", ghGist.ID)
		return nil
	}

	var visibility db.Visibility
	if ghGist.Public {
		visibility = db.PublicVisibility
	} else {
		visibility = db.PrivateVisibility
	}

	title := ghGist.Description
	if title == "" {
		// Use the first filename as the title when description is empty
		for name := range ghGist.Files {
			title = name
			break
		}
	}

	// Collect file contents
	files := make([]db.FileDTO, 0, len(ghGist.Files))
	for _, ghFile := range ghGist.Files {
		content, err := fetchFileContent(ghFile.RawURL)
		if err != nil {
			log.Error().Err(err).Msgf("Cannot fetch content for file %s in GitHub gist %s", ghFile.Filename, ghGist.ID)
			continue
		}
		files = append(files, db.FileDTO{
			Filename: ghFile.Filename,
			Content:  content,
		})
	}

	if len(files) == 0 {
		return fmt.Errorf("no files could be fetched for GitHub gist %s", ghGist.ID)
	}

	// Check if this gist has already been imported
	existingGist, err := db.GetGistByGithubGistID(ghGist.ID, user.ID)
	if err != nil && err != gorm.ErrRecordNotFound {
		return fmt.Errorf("cannot look up existing gist for GitHub gist %s: %w", ghGist.ID, err)
	}

	if existingGist != nil && existingGist.ID != 0 {
		// Update the existing gist
		existingGist.Title = title
		existingGist.Description = ghGist.Description
		existingGist.Private = visibility

		if err := existingGist.Update(); err != nil {
			return fmt.Errorf("cannot update gist for GitHub gist %s: %w", ghGist.ID, err)
		}

		if err := existingGist.AddAndCommitFiles(&files); err != nil {
			return fmt.Errorf("cannot commit files for GitHub gist %s: %w", ghGist.ID, err)
		}

		if err := existingGist.UpdatePreviewAndCount(true); err != nil {
			log.Error().Err(err).Msgf("Cannot update preview for gist %d", existingGist.ID)
		}

		existingGist.UpdateLanguages()
		existingGist.AddInIndex()

		log.Debug().Msgf("Updated gist %d from GitHub gist %s", existingGist.ID, ghGist.ID)
		return nil
	}

	// Create a new gist
	uuidGist, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("cannot generate UUID: %w", err)
	}

	gist := &db.Gist{
		Uuid:         strings.ReplaceAll(uuidGist.String(), "-", ""),
		Title:        title,
		Description:  ghGist.Description,
		Private:      visibility,
		UserID:       user.ID,
		User:         *user,
		NbFiles:      len(files),
		GithubGistID: ghGist.ID,
	}

	if err := gist.InitRepository(); err != nil {
		return fmt.Errorf("cannot initialize repository for GitHub gist %s: %w", ghGist.ID, err)
	}

	if err := gist.AddAndCommitFiles(&files); err != nil {
		return fmt.Errorf("cannot commit files for GitHub gist %s: %w", ghGist.ID, err)
	}

	if err := gist.Create(); err != nil {
		return fmt.Errorf("cannot create gist record for GitHub gist %s: %w", ghGist.ID, err)
	}

	if err := gist.UpdatePreviewAndCount(true); err != nil {
		log.Error().Err(err).Msgf("Cannot update preview for new gist from GitHub gist %s", ghGist.ID)
	}

	gist.UpdateLanguages()
	gist.AddInIndex()

	log.Debug().Msgf("Imported GitHub gist %s as Opengist gist %d", ghGist.ID, gist.ID)
	return nil
}
