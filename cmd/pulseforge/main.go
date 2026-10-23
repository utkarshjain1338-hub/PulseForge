package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Repository struct {
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	URL         string    `json:"url"`
	Private     bool      `json:"private"`
	Stars       int       `json:"stars"`
	Language    string    `json:"language"`
	ConnectedAt time.Time `json:"connected_at"`
}

type GitHubRepository struct {
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
	Private  bool   `json:"private"`
	Stars    int    `json:"stargazers_count"`
	Language string `json:"language"`
}

type AddRepositoryRequest struct {
	URL string `json:"url"`
}

var (
	repositories = make(map[string]Repository)
	repoMu       sync.RWMutex
	repoPattern  = regexp.MustCompile(
		`^https://github\.com/([A-Za-z0-9-]+)/([A-Za-z0-9_.-]+)/?$`,
	)
	githubClient = &http.Client{Timeout: 8 * time.Second}
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"service": "PulseForge",
		"status":  "healthy",
	})
}

func listRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	repoMu.RLock()
	result := make([]Repository, 0, len(repositories))
	for _, repo := range repositories {
		result = append(result, repo)
	}
	repoMu.RUnlock()

	writeJSON(w, http.StatusOK, result)
}

func addRepository(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()

	var input AddRepositoryRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "Send JSON containing a valid url field", http.StatusBadRequest)
		return
	}

	input.URL = strings.TrimSpace(input.URL)
	match := repoPattern.FindStringSubmatch(input.URL)
	if match == nil {
		http.Error(w, "Use a GitHub repository URL: https://github.com/owner/repo", http.StatusBadRequest)
		return
	}

	owner, name := match[1], strings.TrimSuffix(match[2], ".git")
	if name == "" || name == "." || name == ".." {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, name)
	req, err := http.NewRequestWithContext(
		r.Context(), http.MethodGet, apiURL, nil,
	)
	if err != nil {
		http.Error(w, "Could not create GitHub request", http.StatusInternalServerError)
		return
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "PulseForge")

	resp, err := githubClient.Do(req)
	if err != nil {
		http.Error(w, "Could not reach GitHub", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			http.Error(w, "Repository not found or not publicly accessible", http.StatusNotFound)
			return
		}
		if resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusTooManyRequests {
			http.Error(w, "GitHub rate limit reached; try again later", http.StatusTooManyRequests)
			return
		}

		http.Error(w, "GitHub returned an unexpected response", http.StatusBadGateway)
		return
	}

	var ghRepo GitHubRepository
	if err := json.NewDecoder(resp.Body).Decode(&ghRepo); err != nil {
		http.Error(w, "Could not parse GitHub response", http.StatusBadGateway)
		return
	}

	repo := Repository{
		Owner:       owner,
		Name:        name,
		FullName:    ghRepo.FullName,
		URL:         ghRepo.HTMLURL,
		Private:     ghRepo.Private,
		Stars:       ghRepo.Stars,
		Language:    ghRepo.Language,
		ConnectedAt: time.Now().UTC(),
	}

	repoMu.Lock()
	repositories[strings.ToLower(repo.FullName)] = repo
	repoMu.Unlock()

	writeJSON(w, http.StatusCreated, repo)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/api/repositories", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listRepositories(w, r)
		case http.MethodPost:
			addRepository(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	server := &http.Server{
		Addr:              ":8081",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("PulseForge API listening on :8081")
	log.Fatal(server.ListenAndServe())
}
