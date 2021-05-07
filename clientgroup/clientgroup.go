package clientgroup

import (
	"fmt"
	"net/http"
	"sync"

	"git.autodesk.com/github-solutions/lfswatchdog/watchdog"
	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/v35/github"
)

type GatekeeperGroup struct {
	gitHubURL      string
	appID          int64
	privateKeyFile string
	sync.RWMutex
	clients map[int64]*watchdog.WatchDog
}

func New(githubInstance string, appID int64, privateKeyFile string) (*GatekeeperGroup, error) {
	m := make(map[int64]*watchdog.WatchDog)

	return &GatekeeperGroup{
		gitHubURL:      githubInstance,
		appID:          appID,
		privateKeyFile: privateKeyFile,
		clients:        m,
		RWMutex:        sync.RWMutex{},
	}, nil
}

func (group *GatekeeperGroup) GetWatchdog(installationID int64) (*watchdog.WatchDog, error) {
	group.RLock()
	gatekeeper, retrieved := group.clients[installationID]
	group.RUnlock()

	if retrieved {
		return gatekeeper, nil
	} else {
		tr := http.DefaultTransport

		// Wrap the shared transport for use with the app ID 1 authenticating with installation ID 99.
		itr, err := ghinstallation.NewKeyFromFile(tr, group.appID, installationID, group.privateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("could not create a new installation object for appID '%d', installation ID '%d': %w", group.appID, installationID, err)
		}
		itr.BaseURL = group.gitHubURL

		// Use installation transport with github.com/google/go-github
		client, err := github.NewEnterpriseClient(group.gitHubURL, group.gitHubURL, &http.Client{Transport: itr})
		if err != nil {
			return nil, fmt.Errorf("could not create a new client for installation ID '%d': %w", installationID, err)
		}

		gatekeeper := watchdog.New(client)
		group.Lock()
		group.clients[installationID] = gatekeeper
		group.Unlock()
		return gatekeeper, nil
	}
}
