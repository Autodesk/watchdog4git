package watchdog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/git-lfs/git-lfs/filepathfilter"
	"github.com/google/go-github/v35/github"
	yaml "gopkg.in/yaml.v2"
)

const (
	configFile = ".github/watchdog.yml"

	// Warn if files are larger than the threshold in bytes
	lfsSizeThreshold = 512000

	lfsHelpContact     = "@github-solutions"
	lfsMessageTemplate = "" +
		"{{ if .LFSCandidates }}" +
		"**:warning: The following files are larger than {{ .LFSSizeThresholdKB }}KB and may need to be tracked with [Git LFS](https://git-lfs.github.com/):**" +
		"{{ range .LFSCandidates}}\n- {{ . }}{{ end }}\n\n" +
		"{{ end }}" +
		"> Watch the [Git LFS tutorial](https://www.youtube.com/watch?v=YQzNfb4IwEY) or contact {{ .LFSHelpContact }} for help."
)

var errGetContentsUpperLimit = errors.New(
	"reached Git contents API upper limit of 1,000 files for a directory")

type watchdogConfig struct {
	HelpContact                string `yaml:"helpContact"`
	LFSSuggestionsEnabled      bool   `yaml:"lfsSuggestionsEnabled"`
	LFSSizeThreshold           int    `yaml:"lfsSizeThreshold"`
	LFSSizeExemptions          string `yaml:"lfsSizeExemptions"`
	LFSSizeExemptionsThreshold int    `yaml:"lfsSizeExemptionsThreshold"`
	LFSExemptionsFilter        *filepathfilter.Filter
	LFSCommitStatusEnabled     bool `yaml:"lfsCommitStatusEnabled,omitempty"`
}

// Return sensible defaults no matter what the error scenario
func defaultWatchDogConfig() *watchdogConfig {
	return &watchdogConfig{
		HelpContact:                lfsHelpContact,
		LFSSuggestionsEnabled:      true,
		LFSSizeThreshold:           512000,
		LFSSizeExemptionsThreshold: 20000000,
		LFSCommitStatusEnabled:     false,
	}
}

// WatchDog holds all the state related to interacting with GitHub
type WatchDog struct {
	*github.Client
}

// Check all commits of a push for LFS problems
func (watchdog *WatchDog) Check(event *github.PushEvent) {
	for _, commit := range event.Commits {

		log.Printf("processing '%s' in '%s'\n", commit.GetID(), *event.GetRepo().FullName)

		if !*commit.Distinct {
			// Only process and comment on "distinct" commits
			// https://developer.github.com/enterprise/2.12/v3/activity/events/types/#events-api-payload-29
			// the .Distinct field indicates
			// "Whether this commit is distinct from any that have been pushed before."
			log.Printf("'%s' is not distinct in '%s'\n", commit.GetID(), *event.GetRepo().FullName)
			continue
		}

		// TODO: Limit the parallelism of the goroutine
		// If someone pushes a lot of commits then we could generate an
		// a large amount of parallel API requests against GitHub here.
		go func(sha string, added []string, modified []string) {
			var lfsCandidates []string

			config, err := watchdog.getWatchDogConfig(*event.GetRepo().GetOwner().Login, *event.GetRepo().Name, sha)
			if err != nil {
				log.Printf("could not obtain Watchdog configuration file for '%s': %v\n", *event.GetRepo().FullName, err)
			}

			if config.LFSCommitStatusEnabled {
				if err := watchdog.pendingCommitStatus(*event.GetRepo().GetOwner().Login, *event.GetRepo().Name, sha); err != nil {
					log.Printf("could not set a pending status for '%s': %v\n", *event.GetRepo().FullName, err)
					// If we can't update the status to "pending",
					// we nevertheless attempt adding comments and updating status to
					// "success" or "failure".
				}
			}

			files := added[:]
			files = append(files, modified...)

			for _, file := range files {
				size, err := watchdog.getFileSize(*event.GetRepo().GetOwner().Login, *event.GetRepo().Name, sha, file)
				if err != nil {
					log.Printf("could not obtain file size for '%s' at '%s' in '%s': %v\n", file, sha, *event.GetRepo().FullName, err)
					continue
				}

				log.Printf("'%s' has '%s' of size %d \n", *event.GetRepo().FullName, file, size)

				if config.LFSSuggestionsEnabled {
					if config.LFSExemptionsFilter != nil && config.LFSExemptionsFilter.Allows(file) {
						if size > config.LFSSizeExemptionsThreshold { // Super large text file
							lfsCandidates = append(lfsCandidates, file)
						}
					} else {
						if size > config.LFSSizeThreshold { // Large binary file
							lfsCandidates = append(lfsCandidates, file)
						}
					}
				}
			}

			if len(lfsCandidates) > 0 {
				log.Printf("detected potential Git LFS files in '%s'\n", *event.GetRepo().FullName)
				if config.LFSCommitStatusEnabled {
					if err := watchdog.failCommitStatus(*event.GetRepo().GetOwner().Login, *event.GetRepo().Name, sha); err != nil {
						log.Printf("could not update '%s' with a failed status: %v\n", *event.GetRepo().FullName, err)
					}
				}

				comment, err := watchdog.createComment(event.GetRepo().GetFullName(), lfsCandidates, config.HelpContact)
				if err != nil {
					log.Printf("could not create the LFSWatchdog comment for '%s' in '%s': %v\n", sha, *event.GetRepo().FullName, err)
					// We can't create the comment, no sense trying to post it.
					return
				}

				err = watchdog.postComment(*event.GetRepo().GetOwner().Login, *event.GetRepo().Name, sha, &comment)
				if err != nil {
					log.Printf("could not post the LFSWatchdog comment for '%s' in '%s': %v\n", sha, *event.GetRepo().FullName, err)
				}

			} else {
				if config.LFSCommitStatusEnabled {
					if err := watchdog.passCommitStatus(*event.GetRepo().GetOwner().Login, *event.GetRepo().Name, sha); err != nil {
						log.Printf("could not update '%s' with a success status: %v\n", *event.GetRepo().FullName, err)
					}
				}
			}

		}(commit.GetID(), commit.Added, commit.Modified)
	}
}

func (watchdog *WatchDog) getWatchDogConfig(org, repo, ref string) (*watchdogConfig, error) {
	content, err := watchdog.getFileContent(org, repo, ref, configFile)
	if err != nil {
		return defaultWatchDogConfig(), err
	}

	config := &watchdogConfig{}
	err = yaml.UnmarshalStrict([]byte(content), config)
	if err != nil {
		return defaultWatchDogConfig(), err
	}

	config.LFSExemptionsFilter = filepathfilter.New(strings.Fields(config.LFSSizeExemptions), nil)
	return config, nil
}

// New creates a new WatchDog object
func New(client *github.Client) *WatchDog {
	return &WatchDog{
		Client: client,
	}
}

// GetFile returns the content of a file from a GitHub repository.
func (watchdog *WatchDog) getFileContent(org, repo, ref, file string) (string, error) {
	fileContent, _, _, err := watchdog.Repositories.GetContents(
		context.Background(),
		org,
		repo,
		file,
		&github.RepositoryContentGetOptions{Ref: ref},
	)

	if err != nil {
		return "", err
	}

	if fileContent == nil {
		return "", fmt.Errorf("unexpected missing content for file %s at sha %s", file, ref)
	}

	content, err := fileContent.GetContent()
	if err != nil {
		return "", err
	}

	return content, nil
}

// Retrieve the metadata of a directory to obtain file size information
// c.f. https://developer.github.com/v3/repos/contents/
func (watchdog *WatchDog) getDirContent(org, repo, ref, path string) ([]*github.RepositoryContent, error) {
	_, dirContent, _, err := watchdog.Repositories.GetContents(
		context.Background(),
		org,
		repo,
		path,
		&github.RepositoryContentGetOptions{Ref: ref},
	)

	if err != nil {
		return nil, err
	}

	if dirContent == nil {
		return nil, fmt.Errorf("directory '%s' in '%s/%s' has no content", path, org, repo)
	}

	if len(dirContent) >= 1000 {
		return dirContent, errGetContentsUpperLimit
	}

	return dirContent, nil
}

func (watchdog *WatchDog) getFileSize(org, repo, ref, file string) (int, error) {
	directory := filepath.Dir(file)
	dirContent, err := watchdog.getDirContent(org, repo, ref, directory)

	switch err {
	case nil:
		// process directory
	case errGetContentsUpperLimit:
		// process directory, despite reaching API limit
		// The result set might not contain our desired file.
	default:
		return -1, err
	}

	for _, entry := range dirContent {
		if entry.GetPath() == file {
			if entry.GetType() == "file" {
				return entry.GetSize(), nil
			}
			return -1, fmt.Errorf("for file '%s' at ref '%s', name '%s' matches, but object is a %s", file, ref, file, entry.GetType())
		}
	}

	switch err {
	case errGetContentsUpperLimit:
		// The result set indeed did not contain our desired file.
		// TODO: Use the Get Trees API if we run into the 1,000 file limit.
		// https://developer.github.com/v3/git/trees/#get-a-tree
		return -1, err
	default:
		// The push webhook payload referenced a file that is not available!
		return -1, fmt.Errorf("something is seriously wrong with file '%s' at ref '%s' in repo '%s/%s'", file, ref, org, repo)
	}
}

// Create a comment message based on the found failures
func (watchdog *WatchDog) createComment(repoFullName string, lfsCandidates []string, helpContact string) (string, error) {
	t, err := template.New("master").Parse(lfsMessageTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing comment template failed: %v", err)
	}

	values := struct {
		LFSCandidates      []string
		LFSHelpContact     string
		LFSSizeThresholdKB int
	}{
		lfsCandidates,
		helpContact,
		lfsSizeThreshold / 1024,
	}

	var buf bytes.Buffer
	err = t.Execute(&buf, values)
	if err != nil {
		return "", fmt.Errorf("could not generate error message for '%s': %v", repoFullName, err)
	}

	return buf.String(), nil
}

// Post a comment to a given commit
func (watchdog *WatchDog) postComment(org, repo, ref string, comment *string) error {
	_, _, err := watchdog.Repositories.CreateComment(
		context.Background(),
		org,
		repo,
		ref,
		&github.RepositoryComment{Body: comment},
	)

	return err
}

func (watchdog *WatchDog) updateCommitStatus(org, repo, ref string, state string, description string) error {
	statusContext := "LFSWatchDog"
	commitStatus := &github.RepoStatus{
		Context:     &statusContext,
		State:       &state,
		Description: &description,
	}
	_, _, err := watchdog.Repositories.CreateStatus(
		context.Background(),
		org,
		repo,
		ref,
		commitStatus,
	)
	return err
}

func (watchdog *WatchDog) failCommitStatus(org, repo, ref string) error {
	state := "failure"
	description := "LFS error! See commit comments..."
	return watchdog.updateCommitStatus(org, repo, ref, state, description)
}

func (watchdog *WatchDog) passCommitStatus(org, repo, ref string) error {
	state := "success"
	description := "all clear!"
	return watchdog.updateCommitStatus(org, repo, ref, state, description)
}

func (watchdog *WatchDog) pendingCommitStatus(org, repo, ref string) error {
	state := "pending"
	description := "Checking for LFS errors and files ..."
	return watchdog.updateCommitStatus(org, repo, ref, state, description)
}
