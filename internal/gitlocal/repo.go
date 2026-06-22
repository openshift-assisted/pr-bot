package gitlocal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shay23bra/pr-bot/internal/github"
	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
)

const defaultFetchTTL = 5 * time.Minute

type RepoManager struct {
	cacheDir string
	fetchTTL time.Duration
	repos    map[string]*Repo
	mu       sync.Mutex
}

type Repo struct {
	path      string
	lastFetch time.Time
	fetchMu   sync.Mutex
}

func NewRepoManager(cacheDir string) (*RepoManager, error) {
	if cacheDir == "" {
		return nil, fmt.Errorf("PR_BOT_REPO_CACHE_DIR is required")
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir %s: %w", cacheDir, err)
	}
	return &RepoManager{
		cacheDir: cacheDir,
		fetchTTL: defaultFetchTTL,
		repos:    make(map[string]*Repo),
	}, nil
}

var supportedRepos = [][2]string{
	{"openshift", "assisted-service"},
	{"openshift", "assisted-installer"},
	{"openshift", "assisted-installer-agent"},
	{"openshift-assisted", "assisted-installer-ui"},
}

func (rm *RepoManager) CloneAllSupported(token string) error {
	for _, r := range supportedRepos {
		logger.Debug("Pre-cloning %s/%s...", r[0], r[1])
		if _, err := rm.EnsureRepo(r[0], r[1], token); err != nil {
			logger.Debug("Warning: failed to clone %s/%s: %v", r[0], r[1], err)
		}
	}
	return nil
}

func (rm *RepoManager) EnsureRepo(owner, repo, token string) (*Repo, error) {
	key := owner + "/" + repo

	rm.mu.Lock()
	r, exists := rm.repos[key]
	if !exists {
		repoPath := filepath.Join(rm.cacheDir, owner, repo+".git")
		r = &Repo{path: repoPath}
		rm.repos[key] = r
	}
	rm.mu.Unlock()

	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()

	if _, err := os.Stat(r.path); os.IsNotExist(err) {
		cloneURL := fmt.Sprintf("https://%s@github.com/%s/%s.git", token, owner, repo)
		logger.Debug("Cloning %s/%s (bare) to %s", owner, repo, r.path)
		if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
			return nil, fmt.Errorf("failed to create parent dir: %w", err)
		}
		cmd := exec.Command("git", "clone", "--bare", cloneURL, r.path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git clone failed: %w\n%s", err, string(out))
		}
		r.lastFetch = time.Now()
		logger.Debug("Cloned %s/%s successfully", owner, repo)
		return r, nil
	}

	if time.Since(r.lastFetch) > rm.fetchTTL {
		logger.Debug("Fetching %s/%s (stale for %v)", owner, repo, time.Since(r.lastFetch))
		fetchURL := fmt.Sprintf("https://%s@github.com/%s/%s.git", token, owner, repo)
		cmd := exec.Command("git", "-C", r.path, "fetch", "--prune", fetchURL, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		if out, err := cmd.CombinedOutput(); err != nil {
			logger.Debug("Warning: git fetch failed for %s/%s: %v\n%s", owner, repo, err, string(out))
		} else {
			r.lastFetch = time.Now()
			logger.Debug("Fetched %s/%s successfully", owner, repo)
		}
	}

	return r, nil
}

func (r *Repo) ListBranches() ([]github.BranchInfo, error) {
	cmd := exec.Command("git", "-C", r.path, "branch", "--list")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git branch failed: %w", err)
	}

	branchPatterns := []string{"release-ocm-", "releases/v", "release-v", "release-", "v"}
	var result []github.BranchInfo

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(line, "* "))
		if name == "" {
			continue
		}

		for _, pattern := range branchPatterns {
			if !strings.HasPrefix(name, pattern) {
				continue
			}
			if pattern == "release-" && (strings.HasPrefix(name, "release-v") || strings.HasPrefix(name, "release-ocm-") || strings.HasPrefix(name, "releases/v")) {
				continue
			}
			if pattern == "release-v" && strings.HasPrefix(name, "releases/v") {
				continue
			}
			if pattern == "v" {
				if strings.HasPrefix(name, "release-v") || strings.HasPrefix(name, "releases/v") {
					continue
				}
				if len(name) > 1 && !regexp.MustCompile(`^v\d`).MatchString(name) {
					continue
				}
			}

			version := github.ExtractVersionFromBranchWithPattern(name, pattern)
			result = append(result, github.BranchInfo{Name: name, Pattern: pattern, Version: version})
			break
		}
	}

	return result, nil
}

func (r *Repo) IsAncestor(commitSHA, ref string) (bool, error) {
	cmd := exec.Command("git", "-C", r.path, "merge-base", "--is-ancestor", commitSHA, ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, nil
}

func (r *Repo) GetCommitDate(sha string) (*time.Time, error) {
	cmd := exec.Command("git", "-C", r.path, "log", "-1", "--format=%cI", sha)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log failed for %s: %w", sha, err)
	}
	dateStr := strings.TrimSpace(string(out))
	if dateStr == "" {
		return nil, fmt.Errorf("no commit date for %s", sha)
	}
	t, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse date %s: %w", dateStr, err)
	}
	return &t, nil
}

func (r *Repo) ListTags(prefix string) ([]string, error) {
	pattern := prefix + "*"
	cmd := exec.Command("git", "-C", r.path, "tag", "-l", pattern)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git tag failed: %w", err)
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		tag := strings.TrimSpace(line)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

func (r *Repo) TagExists(tag string) (bool, error) {
	cmd := exec.Command("git", "-C", r.path, "rev-parse", "--verify", "refs/tags/"+tag)
	err := cmd.Run()
	return err == nil, nil
}

func (r *Repo) FindCommitInVersionTags(commitSHA, versionPrefix string) ([]string, error) {
	tags, err := r.ListTags(versionPrefix + ".")
	if err != nil {
		return nil, err
	}

	var foundTags []string
	for _, tag := range tags {
		found, err := r.IsAncestor(commitSHA, "refs/tags/"+tag)
		if err != nil {
			continue
		}
		if found {
			foundTags = append(foundTags, tag)
		}
	}

	if len(foundTags) > 0 {
		earliest := foundTags[0]
		for _, tag := range foundTags[1:] {
			if models.CompareSemanticVersions(tag, earliest) < 0 {
				earliest = tag
			}
		}
		return []string{earliest}, nil
	}

	return foundTags, nil
}

func (r *Repo) LogBetween(base, head string) ([]models.CommitInfo, error) {
	cmd := exec.Command("git", "-C", r.path, "log", "--format=%H|%cI|%s", base+".."+head)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log failed: %w", err)
	}

	var commits []models.CommitInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		hash := parts[0]
		if len(hash) > 8 {
			hash = hash[:8]
		}
		commits = append(commits, models.CommitInfo{
			ShortHash: hash,
			Date:      parts[1],
			Title:     parts[2],
		})
	}

	return commits, nil
}

func (r *Repo) ShowFile(sha, path string) (string, error) {
	cmd := exec.Command("git", "-C", r.path, "show", sha+":"+path)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show failed for %s:%s: %w", sha, path, err)
	}
	return string(out), nil
}

func (r *Repo) FindPreviousVersion(version string) (string, error) {
	allTags, err := r.ListTags("v")
	if err != nil {
		return "", fmt.Errorf("failed to list tags: %w", err)
	}

	major, minor, patch, err := parseVersion(version)
	if err != nil {
		return "", fmt.Errorf("invalid version %s: %w", version, err)
	}

	var candidates []string

	if patch > 0 {
		for p := patch - 1; p >= 0; p-- {
			target := fmt.Sprintf("v%d.%d.%d", major, minor, p)
			for _, tag := range allTags {
				if tag == target {
					return tag, nil
				}
			}
		}
		prefix := fmt.Sprintf("v%d.%d.", major, minor-1)
		for _, tag := range allTags {
			if strings.HasPrefix(tag, prefix) {
				candidates = append(candidates, tag)
			}
		}
	} else {
		prefix := fmt.Sprintf("v%d.%d.", major, minor-1)
		for _, tag := range allTags {
			if strings.HasPrefix(tag, prefix) {
				candidates = append(candidates, tag)
			}
		}
	}

	if len(candidates) > 0 {
		latest := candidates[0]
		for _, c := range candidates[1:] {
			if models.CompareSemanticVersions(c, latest) > 0 {
				latest = c
			}
		}
		return latest, nil
	}

	return "", fmt.Errorf("no previous version found for %s", version)
}

func parseVersion(version string) (major, minor, patch int, err error) {
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, fmt.Errorf("invalid version format")
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, 0, err
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return 0, 0, 0, err
	}
	if len(parts) == 3 {
		if _, err := fmt.Sscanf(parts[2], "%d", &patch); err != nil {
			return 0, 0, 0, err
		}
	}
	return major, minor, patch, nil
}
