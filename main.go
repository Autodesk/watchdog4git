package main

import (
	"log"
	"os"
	"strconv"

	webhooks "gopkg.in/go-playground/webhooks.v3"
	receiver "gopkg.in/go-playground/webhooks.v3/github"

	"github.com/Autodesk/watchdog4git/watchdog"
)

const (
	path = "/watchdog4git"
	port = 8080
)

func main() {
	if os.Getenv("GITHUB_TOKEN") == "" {
		log.Fatalf("Set your GITHUB_TOKEN environment variable to a GitHub personal access token\n")
	}

	hook := receiver.New(&receiver.Config{Secret: ""})
	hook.RegisterEvents(handlePush, receiver.PushEvent)

	err := webhooks.Run(hook, ":"+strconv.Itoa(port), path)
	if err != nil {
		log.Println(err)
	}
}

func handlePush(payload interface{}, header webhooks.Header) {
	if push, ok := payload.(receiver.PushPayload); ok {
		w, err := watchdog.New(&push, os.Getenv("GITHUB_TOKEN"))
		if err != nil {
			log.Println(err)
			return
		}
		w.Check()
	} else {
		log.Println("Received a payload that was not a GitHub push...")
	}
}
