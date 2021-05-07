package watchdog

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v35/github"
	"github.com/stretchr/testify/assert"
)

// Setup a HTTP test server to serve mocked API responses.
func setup() (mux *http.ServeMux, server *httptest.Server) {
	mux = http.NewServeMux()
	server = httptest.NewServer(mux)
	return mux, server
}

// Shutdown HTTP test server.
func teardown(server *httptest.Server) {
	server.Close()
}

func newWatchDog(url string) *WatchDog {
	http := http.DefaultClient
	client, _ := github.NewEnterpriseClient(url, url, http)
	w := New(client)
	return w
}
func TestGetFile(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := "README.md"
	payload := `
	{
		"type": "file",
		"encoding": "base64",
		"size": 5362,
		"name": "README.md",
		"path": "README.md",
		"content": "c29tZXRoaW5nIGVsc2U="
	  }
	`
	endpoint := fmt.Sprintf("/api/v3/repos/%s/contents/%s", "test-org/test-repo", path)
	mux.HandleFunc(endpoint,
		func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(rw, "%s", payload)
		},
	)
	retrieved, err := w.getFileContent("test-org", "test-repo", "abc123", path)
	assert.Nil(t, err)
	assert.Equal(t, "something else", retrieved)
}

func TestGetDirContent(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := "some/path"
	payload := `[{ "name": "file1" }, { "name": "file2" }]`
	endpoint := fmt.Sprintf(
		"/api/v3/repos/%s/contents/%s/",
		"test-org/test-repo",
		filepath.Dir(path),
	)

	mux.HandleFunc(endpoint,
		func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(rw, "%s", payload)
		},
	)

	dir, err := w.getDirContent("test-org", "test-repo", "abc123", path)
	assert.Nil(t, err)
	assert.Equal(t, 2, len(dir))
	assert.Equal(t, *dir[0].Name, "file1")
	assert.Equal(t, *dir[1].Name, "file2")
}

func TestGetFileSize(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := "some/path/bogus-basename"
	payload := `[{ "type": "file", "size": 5, "name": "file1", "path": "some/path/file1" }, { "type": "symlink", "size": 6, "name": "file2", "path": "some/path/file2" }]`
	endpoint := fmt.Sprintf(
		"/api/v3/repos/%s/contents/%s/",
		"test-org/test-repo",
		filepath.Dir(path),
	)

	mux.HandleFunc(endpoint,
		func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(rw, "%s", payload)
		},
	)

	size, err := w.getFileSize("test-org", "test-repo", "abc123", "some/path/file1")
	assert.Nil(t, err)
	assert.Equal(t, 5, size)
	_, err = w.getFileSize("test-org", "test-repo", "abc123", "some/path/file2")
	assert.NotNil(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "for file 'some/path/file2' at ref 'abc123', name 'some/path/file2' matches, but object is a symlink"))
}

func TestCommentAll(t *testing.T) {
	w := newWatchDog("http://testserver.com")

	comment, err := w.createComment(
		"test-org/test-repo",
		[]string{"path/to/large/file1", "other/path/to/large/file2"},
		"[#tech-git](https://autodesk.slack.com/messages/C0E0BH9T5)",
	)
	assert.Nil(t, err)
	assert.Equal(t, strings.Replace(
		`**:warning: The following files are larger than 500KB and may need to be tracked with [Git LFS](https://git-lfs.github.com/):**
		- path/to/large/file1
		- other/path/to/large/file2

		> Watch the [Git LFS tutorial](https://www.youtube.com/watch?v=YQzNfb4IwEY) or contact [#tech-git](https://autodesk.slack.com/messages/C0E0BH9T5) for help.`, "\t", "", -1),
		comment,
	)
}

func TestCommentLargeFiles(t *testing.T) {
	w := newWatchDog("http://testserver.com")

	comment, err := w.createComment(
		"test-org/test-repo",
		[]string{"path/to/large/file1", "other/path/to/large/file2"},
		"someone@somecompany.com",
	)
	assert.Nil(t, err)
	assert.Equal(t, strings.Replace(
		`**:warning: The following files are larger than 500KB and may need to be tracked with [Git LFS](https://git-lfs.github.com/):**
		- path/to/large/file1
		- other/path/to/large/file2

		> Watch the [Git LFS tutorial](https://www.youtube.com/watch?v=YQzNfb4IwEY) or contact someone@somecompany.com for help.`, "\t", "", -1),
		comment,
	)
}
func TestPostComment(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)
	sha := "123abc"

	endpoint := fmt.Sprintf("/api/v3/repos/%s/commits/%s/comments", "test-org/test-repo", sha)

	mux.HandleFunc(endpoint,
		func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprint(rw, "")
		},
	)

	suggestions := []string{"a/large/file", "largish"}
	comment, err := w.createComment("test-org/test-repo", suggestions, "@someone")
	assert.Nil(t, err)
	err = w.postComment("test-org", "test-repo", sha, &comment)
	assert.Nil(t, err)
}
func TestWatchDogConfigFile(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := ".github/watchdog.yml"
	yml := "# Contact used in violation comments\n" +
		"helpContact: \"" + lfsHelpContact + "\"\n\n" +
		"# Warn if files are larger than the threshold in bytes\n" +
		"lfsSizeThreshold: 512000\n\n" +
		"lfsSizeExemptionsThreshold: 20000000\n\n" +
		"# List of files that are not checked for size\n" +
		"lfsSizeExemptions: |\n" +
		"  Regression/CrsTestSuite.txt\n" +
		"  Regression/Something.txt\n" +
		"  SomethingElse.txt\n" +
		"  Yet/another/SomethingElse.txt\n" +
		"  *.xml\n\n" +
		"lfsSuggestionsEnabled: Yes\n" +
		"lfsCommitStatusEnabled: Yes\n"

	size := len(yml)
	contentType := "file"
	encoded := base64.StdEncoding.EncodeToString([]byte(yml))
	sha := "abc123"
	name := "watchdog.yml"
	encoding := "base64"

	rc := &github.RepositoryContent{
		Content:  &encoded,
		Size:     &size,
		Path:     &path,
		Type:     &contentType,
		SHA:      &sha,
		Name:     &name,
		Encoding: &encoding,
	}

	marshalled, err := json.Marshal(rc)
	assert.Nil(t, err)

	endpoint := fmt.Sprintf("/api/v3/repos/%s/contents/%s", "test-org/test-repo", path)
	mux.HandleFunc(endpoint,
		func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(rw, "%s", marshalled)
		},
	)

	c, err := w.getWatchDogConfig("test-org", "test-repo", sha)
	assert.Nil(t, err)
	assert.Equal(t, 512000, c.LFSSizeThreshold)
	assert.Equal(t, 20000000, c.LFSSizeExemptionsThreshold)
	assert.Equal(t, lfsHelpContact, c.HelpContact)
	assert.Equal(t, true, c.LFSSuggestionsEnabled)
	assert.True(t, c.LFSExemptionsFilter.Allows("Regression/Something.txt"))
	assert.True(t, c.LFSExemptionsFilter.Allows("wildcard.xml"))
	assert.False(t, c.LFSExemptionsFilter.Allows("Regression/non-existent.txt"))
	assert.True(t, c.LFSCommitStatusEnabled)
}

func TestUpdateCommitStatus(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)
	sha := "6dcb09b5b57875f334f61aebed695e2e4193db5e"

	endpoint := fmt.Sprintf("/api/v3/repos/%s/statuses/%s", "test-org/test-repo", sha)

	reply := `
	{
		"url": "https://api.github.com/repos/octocat/Hello-World/statuses/6dcb09b5b57875f334f61aebed695e2e4193db5e",
		"avatar_url": "https://github.com/images/error/hubot_happy.gif",
		"id": 1,
		"node_id": "MDY6U3RhdHVzMQ==",
		"state": "success",
		"description": "Build has completed successfully",
		"target_url": "https://ci.example.com/1000/output",
		"context": "continuous-integration/jenkins",
		"created_at": "2012-07-20T01:19:13Z",
		"updated_at": "2012-07-20T01:19:13Z",
		"creator": {
		  "login": "octocat",
		  "id": 1,
		  "node_id": "MDQ6VXNlcjE=",
		  "avatar_url": "https://github.com/images/error/octocat_happy.gif",
		  "gravatar_id": "",
		  "url": "https://api.github.com/users/octocat",
		  "html_url": "https://github.com/octocat",
		  "followers_url": "https://api.github.com/users/octocat/followers",
		  "following_url": "https://api.github.com/users/octocat/following{/other_user}",
		  "gists_url": "https://api.github.com/users/octocat/gists{/gist_id}",
		  "starred_url": "https://api.github.com/users/octocat/starred{/owner}{/repo}",
		  "subscriptions_url": "https://api.github.com/users/octocat/subscriptions",
		  "organizations_url": "https://api.github.com/users/octocat/orgs",
		  "repos_url": "https://api.github.com/users/octocat/repos",
		  "events_url": "https://api.github.com/users/octocat/events{/privacy}",
		  "received_events_url": "https://api.github.com/users/octocat/received_events",
		  "type": "User",
		  "site_admin": false
		}
	  }
	`

	mux.HandleFunc(endpoint,
		func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprint(rw, reply)
		},
	)

	err := w.updateCommitStatus("test-org", "test-repo", sha, "success", "Build has completed successfully")
	assert.Nil(t, err)
}
