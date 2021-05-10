package server

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"git.autodesk.com/github-solutions/lfswatchdog/clientgroup"
	"github.com/google/go-github/v35/github"
)

const (
	defaultPath = "/lfs/v2"
	defaultPort = "8080"
)

func Run(github, secret, appID, privateKeyFile, port, path string) {
	if github == "" {
		log.Fatalf("Set your GITHUB_HOST environment variable to and instance of GitHub Enterprise")
	}

	if appID == "" {
		log.Fatalf("Set your GITHUB_APP_ID environment variable to a GitHub App ID\n")
	}

	appID64, err := strconv.ParseInt(appID, 10, 64)
	if err != nil {
		log.Fatalf("Set your GITHUB_APP_ID environment variable to something that can convert to int64\n")
	}

	if privateKeyFile == "" {
		log.Fatalf("Set your GITHUB_APP_PRIVATE_KEY_FILE environment variable to a GitHub App private key pem file\n")
	}

	if port == "" {
		port = defaultPort
	}

	if path == "" {
		path = defaultPath
	}

	log.Printf("server started at path '%s' on port %s...", path, port)
	http.HandleFunc(path, HandlePushEvent(github, secret, appID64, privateKeyFile))
	err = http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func HandlePushEvent(githubEnterprise, secret string, appID int64, privateKeyFile string) func(http.ResponseWriter, *http.Request) {

	clientGroup, err := clientgroup.New(githubEnterprise, appID, privateKeyFile)
	if err != nil {
		log.Fatalf("could not create HTTP client: %v", err)
	}

	result := func(w http.ResponseWriter, r *http.Request) {
		payload, err := github.ValidatePayload(r, []byte(secret))
		if err != nil {
			message := fmt.Sprintf("error validating request body: err=%s\n", err)
			log.Print(message)
			http.Error(w, message, 400)
			return
		}
		defer r.Body.Close()

		event, err := github.ParseWebHook(github.WebHookType(r), payload)
		if err != nil {
			message := fmt.Sprintf("could not parse webhook: err=%v\n", err)
			log.Print(message)
			http.Error(w, message, 400)
			return
		}

		switch e := event.(type) {
		case *github.PushEvent:
			// https://docs.github.com/en/developers/webhooks-and-events/webhook-events-and-payloads#pull_request_review

			guard, err := clientGroup.GetWatchdog(e.Installation.GetID())
			if err != nil {
				log.Printf("could not obtain Watchdog client: %v\n", err)
				http.Error(w, err.Error(), 500)
				return
			}

			guard.Check(e)

		case *github.PingEvent:
			io.WriteString(w, fmt.Sprintf("pong!\nhook_id: %d\nzen: %s\n", e.GetHookID(), e.GetZen()))
		default:
			message := fmt.Sprintf("unhandled event type: '%s'\n", github.WebHookType(r))
			log.Print(message)
			http.Error(w, message, 400)
		}
	}

	return result
}
