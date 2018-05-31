package watchdog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/git-lfs/git-lfs/filepathfilter"
	"github.com/git-lfs/git-lfs/lfs"
	github "github.com/google/go-github/github"
	"github.com/gregjones/httpcache"
	"golang.org/x/oauth2"
	receiver "gopkg.in/go-playground/webhooks.v3/github"
	yaml "gopkg.in/yaml.v2"

	"github.com/autodesk/watchdog4git/attributes"
)

const (
	configFile = ".github/watchdog.yml"

	lfsSizeThreshold

	// We expect Git LFS pointer files to have a size between 20 and 15 bytes
	// Don't look at Git LFS pointer files larger than `lfsPtrBlobMaxSize`
	// c.f. https://github.com/git-lfs/git-lfs/blob/master/lfs/scanner.go#L6-L8
	lfsPtrBlobMinSize = 20
	lfsPtrBlobMaxSize = 150

	// Template for the warning message posted as comment to a GitHub commit
	lfsMessageTemplate = "" +
		"{{ if .LFSCandidates }}" +
		"**:warning: The following files are larger than {{ .LFSSizeThresholdKB }}KB and may need to be tracked with [Git LFS](https://git-lfs.github.com/):**" +
		"{{ range .LFSCandidates}}\n- {{ . }}{{ end }}\n\n" +
		"{{ end }}" +
		"{{ if .LFSInvalidPointer }}" +
		"**:warning: The following files have not been properly added to [Git LFS](https://git-lfs.github.com/):**" +
		"{{ range .LFSInvalidPointer }}\n- {{ . }}{{ end }}\n\n" +
		"{{ end }}" +
		"> Watch the [Git LFS tutorial](https://www.youtube.com/watch?v=YQzNfb4IwEY) or contact {{ .LFSHelpContact }} for help."
)

var errGetContentsUpperLimit = errors.New(
	"reached Git contents API upper limit of 1,000 files for a directory")

var errLFSPointer = errors.New("LFS pointer size or structure is incorrect")

type watchdogConfig struct {
	HelpContact                string `yaml:"helpContact"`
	LFSSuggestionsEnabled      bool   `yaml:"lfsSuggestionsEnabled"`
	LFSSizeThreshold           int    `yaml:"lfsSizeThreshold"`
	LFSSizeExemptions          string `yaml:"lfsSizeExemptions"`
	LFSSizeExemptionsThreshold int    `yaml:"lfsSizeExemptionsThreshold"`
	LFSExemptionsFilter        *filepathfilter.Filter
}

// Define sensible defaults if ".github/watchdog.yml" is not present or incomplete
func defaultWatchDogConfig() *watchdogConfig {
	return &watchdogConfig{
		HelpContact:                "@mlbright or @larsxschneider",
		LFSSuggestionsEnabled:      true,
		LFSSizeThreshold:           512000,
		LFSSizeExemptionsThreshold: 20000000,
	}
}

// WatchDog holds all the state related to interacting with GitHub
type WatchDog struct {
	*receiver.PushPayload
	*github.Client
}

func (watchdog *WatchDog) repo() string {
	return watchdog.Repository.Name
}

func (watchdog *WatchDog) org() string {
	return watchdog.Repository.Owner.Name
}

func (watchdog *WatchDog) fullName() string {
	return watchdog.Repository.FullName
}

func (watchdog *WatchDog) getWatchDogConfig(ref string) (*watchdogConfig, error) {
	content, err := watchdog.getFileContent(ref, configFile)
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
func New(payload *receiver.PushPayload, token string) (*WatchDog, error) {
	client := github.NewClient(&http.Client{
		Transport: &oauth2.Transport{
			Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
			Base:   httpcache.NewMemoryCacheTransport(),
		},
	})
	watchdog := &WatchDog{
		Client:      client,
		PushPayload: payload,
	}
	if err := watchdog.setGitHubAPIURL(); err != nil {
		return nil, err
	}

	return watchdog, nil
}

func (watchdog *WatchDog) error(msg, sha, file string) error {
	return fmt.Errorf("error: '%s'\n\t'%s' in '%s' at '%.6s'", msg, file, watchdog.fullName(), sha)
}

func (watchdog *WatchDog) setGitHubAPIURL() error {
	gh, err := url.Parse(watchdog.Repository.URL)
	if err != nil {
		return err
	}

	if gh.Hostname() == "github.com" {
		gh.Host = "api.github.com"
		gh.Path = "/"
	} else {
		gh.Path = "/api/v3/"
	}

	watchdog.BaseURL = gh
	return nil
}

// Returns a filter object to check if a file is filtered by LFS
func (watchdog *WatchDog) getAttributeFilter(ref string) (*filepathfilter.Filter, error) {
	attributesText, err := watchdog.getFileContent(ref, ".gitattributes")
	if err != nil {
		return nil, err
	}

	return attributes.GetAttributePaths(attributesText), nil
}

// GetFile returns the content of a file from a GitHub repository
//
// Attention:
// GetContents returns inconsistent results for Git LFS files. Git LFS
// files with a content smaller than 1MB return the Git LFS pointer
// file (~140 bytes) encoded as base64. Git LFS files with a content
// larger than 1MB return a "blob to large" error. Reported to GitHub
// in https://support.enterprise.github.com/hc/en-us/requests/55040
func (watchdog *WatchDog) getFileContent(ref string, file string) (string, error) {
	fileContent, _, _, err := watchdog.Repositories.GetContents(
		context.Background(),
		watchdog.org(),
		watchdog.repo(),
		file,
		&github.RepositoryContentGetOptions{Ref: ref},
	)

	if err != nil {
		return "", err
	}

	if fileContent == nil {
		return "", watchdog.error("unexpected missing file content", ref, file)
	}

	content, err := fileContent.GetContent()
	if err != nil {
		return "", err
	}

	return content, nil
}

// Retrieve the metadata of a directory to obtain file size information
// c.f. https://developer.github.com/v3/repos/contents/
func (watchdog *WatchDog) getDirContent(ref string, path string) ([]*github.RepositoryContent, error) {
	_, dirContent, _, err := watchdog.Repositories.GetContents(
		context.Background(),
		watchdog.org(),
		watchdog.repo(),
		path,
		&github.RepositoryContentGetOptions{Ref: ref},
	)

	if err != nil {
		return nil, err
	}

	if dirContent == nil {
		return nil, fmt.Errorf("directory '%s' in '%s' has no content", path, watchdog.fullName())
	}

	if len(dirContent) >= 1000 {
		return dirContent, errGetContentsUpperLimit
	}

	return dirContent, nil
}

func (watchdog *WatchDog) getFileSize(ref, file string) (int, error) {
	directory := filepath.Dir(file)
	dirContent, err := watchdog.getDirContent(ref, directory)

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
			return -1, watchdog.error(fmt.Sprintf("name '%s' matches, but object is a %s", file, entry.GetType()), ref, file)
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
		return -1, watchdog.error("something is seriously wrong", ref, file)
	}
}

// Check if a given Git LFS pointer file is valid (expected size and content)
func (watchdog *WatchDog) validateLFSPointer(ref string, file string, size int) error {
	if size > lfsPtrBlobMaxSize {
		return errLFSPointer
	}

	if size < lfsPtrBlobMinSize {
		return errLFSPointer
	}

	content, err := watchdog.getFileContent(ref, file)
	if err != nil {
		return err
	}

	_, err = lfs.DecodePointer(strings.NewReader(content))
	if err != nil {
		return errLFSPointer
	}

	return nil
}

// Check all commits of a push for LFS problems
func (watchdog *WatchDog) Check() {
	for _, commit := range watchdog.Commits {

		if !commit.Distinct {
			// A distinct commit has never been pushed before.
			// Only process distinct commits to avoid multiple comments
			// on the same commit.
			// c.f. https://developer.github.com/enterprise/2.12/v3/activity/events/types/#events-api-payload-29
			continue
		}

		// TODO: Limit the parallelism of the goroutine
		// If someone pushes a lot of commits then we could generate an
		// a large amount of parallel API requests against GitHub here.
		go func(sha string, added []string, modified []string) {
			var lfsInvalidPointers []string
			var lfsCandidates []string

			config, err := watchdog.getWatchDogConfig(sha)
			if err != nil {
				log.Println(err)
			}

			lfsFilter, err := watchdog.getAttributeFilter(sha)
			if err != nil {
				log.Println(err)
			}

			files := added[:]
			files = append(files, modified...)

			for _, file := range files {
				size, err := watchdog.getFileSize(sha, file)
				if err != nil {
					log.Println(err)
					continue
				}
				if os.Getenv("WATCHDOG_DEBUG") != "" {
					log.Printf("%s %d\n", file, size)
				}

				if lfsFilter != nil && lfsFilter.Allows(file) {
					err := watchdog.validateLFSPointer(sha, file, size)
					switch err {
					case nil:
						// Valid LFS pointer!
					case errLFSPointer:
						log.Println(err)
						lfsInvalidPointers = append(lfsInvalidPointers, file)
					default:
						// An error, but not necessarily an invalid LFS pointer error
						log.Println(err)
					}
				} else if config.LFSSuggestionsEnabled {
					if config.LFSExemptionsFilter.Allows(file) {
						if size > config.LFSSizeExemptionsThreshold {
							// The file was exempt from the normal size
							// check but even exceeds the maximum file
							// size for exempt files.
							lfsCandidates = append(lfsCandidates, file)
						}
					} else {
						if size > config.LFSSizeThreshold {
							// The file exceed the maxium file size.
							lfsCandidates = append(lfsCandidates, file)
						}
					}
				}
			}

			if len(lfsInvalidPointers) > 0 || len(lfsCandidates) > 0 {
				if os.Getenv("WATCHDOG_DEBUG") == "" {
					comment, err := watchdog.createComment(
						lfsInvalidPointers,
						lfsCandidates,
						config.HelpContact,
						config.LFSSizeThreshold)
					if err != nil {
						log.Println(err)
						return
					}

					err = watchdog.postComment(sha, &comment)
					if err != nil {
						log.Println(err)
						return
					}
				} else {
					for _, invalid := range lfsInvalidPointers {
						log.Printf("invalid pointer: %s", invalid)
					}
					for _, candidate := range lfsCandidates {
						log.Printf("LFS candidate: %s", candidate)
					}
				}
			}
		}(commit.ID, commit.Added, commit.Modified)
	}
}

// Create a comment message based on the found problems
func (watchdog *WatchDog) createComment(
	lfsInvalidPointer, lfsCandidates []string, helpContact string, sizeThreshold int) (string, error) {
	t, err := template.New("master").Parse(lfsMessageTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing comment template failed: %v", err)
	}

	values := struct {
		LFSInvalidPointer  []string
		LFSCandidates      []string
		LFSHelpContact     string
		LFSSizeThresholdKB int
	}{
		lfsInvalidPointer,
		lfsCandidates,
		helpContact,
		sizeThreshold / 1024,
	}

	var buf bytes.Buffer
	err = t.Execute(&buf, values)
	if err != nil {
		return "", fmt.Errorf("could not generate error message for %s: %v", watchdog.fullName(), err)
	}

	return buf.String(), nil
}

// Post a comment to a given commit
func (watchdog *WatchDog) postComment(ref string, comment *string) error {
	c, _, err := watchdog.Repositories.CreateComment(
		context.Background(),
		watchdog.org(),
		watchdog.repo(),
		ref,
		&github.RepositoryComment{Body: comment},
	)

	if err != nil {
		return err
	}

	log.Printf("Posted comment: %s", c.GetHTMLURL())
	return nil
}
