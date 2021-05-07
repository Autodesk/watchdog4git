package main

import (
	"os"

	"git.autodesk.com/github-solutions/lfswatchdog/server"
)

func main() {
	server.Run(
		os.Getenv("GITHUB_ENTERPRISE_URL"),
		os.Getenv("LFSWATCHDOG_SECRET"),
		os.Getenv("GITHUB_APP_ID"),
		os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"),
		os.Getenv("LFSWATCHDOG_PORT"),
		os.Getenv("LFSWATCHDOG_PATH"),
	)
}
