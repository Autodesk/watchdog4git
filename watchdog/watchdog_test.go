package watchdog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	github "github.com/google/go-github/github"
	"github.com/gregjones/httpcache"
	"github.com/stretchr/testify/assert"
	receiver "gopkg.in/go-playground/webhooks.v3/github"
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
	pushPayload := &receiver.PushPayload{}
	pushPayload.Repository.URL = url
	pushPayload.Repository.FullName = "testorg/testrepo"
	pushPayload.Repository.Name = "testrepo"
	pushPayload.Repository.Owner.Name = "testorg"
	w, _ := New(pushPayload, "abc123")
	return w
}

func TestSetGitHubAPIURL(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{
			in:  "https://github.com/baxterthehacker/public-repo",
			out: "https://api.github.com/",
		},
		{
			in:  "https://ghe.company.com/baxterthehacker/public-repo",
			out: "https://ghe.company.com/api/v3/",
		},
	}

	for _, c := range cases {
		w := newWatchDog(c.in)
		w.setGitHubAPIURL()
		assert.Equal(t, w.Client.BaseURL.String(), c.out)
	}
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
	endpoint := fmt.Sprintf("/api/v3/repos/%s/contents/%s", w.fullName(), path)
	mux.HandleFunc(endpoint,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s", payload)
		},
	)
	retrieved, err := w.getFileContent("abc123", path)
	assert.Nil(t, err)
	assert.Equal(t, "something else", retrieved)
}

func TestHTTPCache(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := "README.md"
	payload := `{ "name": "README.md" }`

	endpoint := fmt.Sprintf("/api/v3/repos/%s/contents/%s", w.fullName(), path)
	requestCount := 0
	mux.HandleFunc(endpoint,
		func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			// Cache header as defined by GitHub Enterprise
			w.Header().Set("Last-Modified", "Fri, 14 Dec 2010 01:01:50 GMT")
			w.Header().Set("Cache-Control", "private, max-age=60, s-maxage=60")
			fmt.Fprintf(w, "%s", payload)
		},
	)

	_, _, resp, err := w.Client.Repositories.GetContents(
		context.Background(), w.org(), w.repo(), path,
		&github.RepositoryContentGetOptions{})
	assert.Nil(t, err)
	assert.Equal(t, requestCount, 1)
	_, exists := resp.Header[httpcache.XFromCache]
	assert.False(t, exists, "first request should not have a cache header")

	_, _, resp, err = w.Client.Repositories.GetContents(
		context.Background(), w.org(), w.repo(), path, &github.RepositoryContentGetOptions{})
	assert.Nil(t, err)
	assert.Equal(t, requestCount, 1, "second request should be cached")
	_, exists = resp.Header[httpcache.XFromCache]
	assert.True(t, exists, "second request should have a cache header")
}

func TestGetDirContent(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := "some/path"
	payload := `[{ "name": "file1" }, { "name": "file2" }]`
	endpoint := fmt.Sprintf(
		"/api/v3/repos/%s/contents/%s/",
		w.Repository.FullName,
		filepath.Dir(path),
	)

	mux.HandleFunc(endpoint,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s", payload)
		},
	)

	dir, err := w.getDirContent("abc123", path)
	assert.Nil(t, err)
	assert.Equal(t, 2, len(dir))
	assert.Equal(t, *dir[0].Name, "file1")
	assert.Equal(t, *dir[1].Name, "file2")
}

func TestGetFileSize(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	path := "some/path"
	payload := `[{ "type": "file", "size": 5, "name": "file1", "path": "some/path/file1" }, { "type": "symlink", "size": 6, "name": "file2", "path": "some/path/file2" }]`
	endpoint := fmt.Sprintf(
		"/api/v3/repos/%s/contents/%s/",
		w.Repository.FullName,
		filepath.Dir(path),
	)

	mux.HandleFunc(endpoint,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s", payload)
		},
	)

	size, err := w.getFileSize("abc123", "some/path/file1")
	assert.Nil(t, err)
	assert.Equal(t, 5, size)
	size, err = w.getFileSize("abc123", "some/path/file2")
	assert.NotNil(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "error: 'name 'some/path/file2' matches, but object is a symlink'"))
}
func TestCommentAll(t *testing.T) {
	w := newWatchDog("http://testserver.com")

	comment, err := w.createComment(
		[]string{"pointer/without/content/1", "pointer/without/content/2"},
		[]string{"path/to/large/file1", "other/path/to/large/file2"},
		"[#tech-git](https://company.slack.com/messages/123)",
		512000,
	)
	assert.Nil(t, err)
	assert.Equal(t, strings.Replace(
		`**:warning: The following files are larger than 500KB and may need to be tracked with [Git LFS](https://git-lfs.github.com/):**
		- path/to/large/file1
		- other/path/to/large/file2

		**:warning: The following files have not been properly added to [Git LFS](https://git-lfs.github.com/):**
		- pointer/without/content/1
		- pointer/without/content/2

		> Watch the [Git LFS tutorial](https://www.youtube.com/watch?v=YQzNfb4IwEY) or contact [#tech-git](https://company.slack.com/messages/123) for help.`, "\t", "", -1),
		comment,
	)
}

func TestCommentLargeFiles(t *testing.T) {
	w := newWatchDog("http://testserver.com")

	comment, err := w.createComment(
		nil,
		[]string{"path/to/large/file1", "other/path/to/large/file2"},
		"someone@somecompany.com",
		512000,
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

func TestCommentInvalidPointer(t *testing.T) {
	w := newWatchDog("http://testserver.com")

	comment, err := w.createComment(
		[]string{"pointer/without/content/1", "pointer/without/content/2"},
		nil,
		"[#tech-git](https://company.slack.com/messages/123)",
		100000,
	)
	assert.Nil(t, err)
	assert.Equal(t, strings.Replace(
		`**:warning: The following files have not been properly added to [Git LFS](https://git-lfs.github.com/):**
		- pointer/without/content/1
		- pointer/without/content/2

		> Watch the [Git LFS tutorial](https://www.youtube.com/watch?v=YQzNfb4IwEY) or contact [#tech-git](https://company.slack.com/messages/123) for help.`, "\t", "", -1),
		comment,
	)
}
func TestPostComment(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)
	sha := "123abc"

	endpoint := fmt.Sprintf("/api/v3/repos/%s/commits/%s/comments", w.Repository.FullName, sha)

	mux.HandleFunc(endpoint,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "")
		},
	)

	misses := []string{"something/that/should/be/a/pointer", "badpointer"}
	suggestions := []string{"a/large/file", "largish"}
	comment, err := w.createComment(misses, suggestions, "@someone", 200)
	assert.Nil(t, err)
	err = w.postComment(sha, &comment)
	assert.Nil(t, err)
}

func TestInvalidLFSPointerSize(t *testing.T) {
	w := newWatchDog("http://testserver.com")
	err := w.validateLFSPointer("abc123", "some/big/pointer", 10)
	assert.Equal(t, errLFSPointer, err)
	err = w.validateLFSPointer("abc123", "some/big/pointer", 9999)
	assert.Equal(t, errLFSPointer, err)
}

func TestLFSPointerBlobDownloadError(t *testing.T) {
	_, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)
	// Since we did not configure any mock, we cannot retrieve content
	err := w.validateLFSPointer("abc123", "some/pointer", 140)
	assert.IsType(t, &github.ErrorResponse{}, err)
}

func TestInvalidLFSPointer(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	mux.HandleFunc("/api/v3/repos/testorg/testrepo/contents/path/to/some/pointer",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `
				{
					"name": "pointer",
					"path": "path/to/some/pointer",
					"sha": "abc456",
					"size": 18,
					"type": "file",
					"content": "YSBiYWQgbGZzIHBvaW50ZXI=",
					"encoding": "base64"
				}`)
		})

	err := w.validateLFSPointer("abc456", "path/to/some/pointer", 140)
	assert.Equal(t, err, errLFSPointer)
}

func TestValidLFSPointer(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)

	mux.HandleFunc("/api/v3/repos/testorg/testrepo/contents/path/to/some/pointer",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `
				{
					"name": "pointer",
					"path": "path/to/some/pointer",
					"sha": "abc456",
					"size": 5902,
					"type": "file",
					"content": "dmVyc2lvbiBodHRwczovL2dpdC1sZnMuZ2l0aHViLmNvbS9zcGVjL3YxCm9p\nZCBzaGEyNTY6ZjA5NmY4MzJmMTYwNGI4ZWYwMmJiYTg1ZDIzZDQ1Y2JkOTE0\nNGEzMjkzNmMyNzYxYzMyNzc1ZmFiN2IxMDAxZQpzaXplIDU5MDIK\n",
					"encoding": "base64"
				}`)
		})

	err := w.validateLFSPointer("abc456", "path/to/some/pointer", 140)
	assert.Nil(t, err)
}

func TestWatchDogConfigFile(t *testing.T) {
	mux, server := setup()
	defer teardown(server)
	w := newWatchDog(server.URL)
	lfsHelpContact := "Mr. Watch"

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
		"lfsSuggestionsEnabled: Yes"

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

	endpoint := fmt.Sprintf("/api/v3/repos/%s/contents/%s", w.fullName(), path)
	mux.HandleFunc(endpoint,
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s", marshalled)
		},
	)

	c, err := w.getWatchDogConfig(sha)
	assert.Nil(t, err)
	assert.Equal(t, 512000, c.LFSSizeThreshold)
	assert.Equal(t, 20000000, c.LFSSizeExemptionsThreshold)
	assert.Equal(t, lfsHelpContact, c.HelpContact)
	assert.Equal(t, true, c.LFSSuggestionsEnabled)
	assert.True(t, c.LFSExemptionsFilter.Allows("Regression/Something.txt"))
	assert.True(t, c.LFSExemptionsFilter.Allows("wildcard.xml"))
	assert.False(t, c.LFSExemptionsFilter.Allows("Regression/non-existent.txt"))
}
