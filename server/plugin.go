package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"golang.org/x/oauth2"

	"github.com/google/go-github/github"
	"github.com/mattermost/mattermost-server/model"
	"github.com/mattermost/mattermost-server/plugin"
)

// Plugin implements the interface expected by the Mattermost server to communicate between the server and plugin processes.
type Plugin struct {
	plugin.MattermostPlugin

	// configurationLock synchronizes access to the configuration.
	configurationLock sync.RWMutex

	// configuration is the active plugin configuration. Consult getConfiguration and
	// setConfiguration for usage.
	configuration *configuration

	github *github.Client
}

func (p *Plugin) OnActivate() error {
	config := p.getConfiguration()
	if err := config.IsValid(); err != nil {
		return err
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: config.GitHubAPIKey},
	)
	tc := oauth2.NewClient(ctx, ts)

	p.github = github.NewClient(tc)

	return nil
}

// ServeHTTP demonstrates a plugin that handles HTTP requests by greeting the world.
func (p *Plugin) ServeHTTP(c *plugin.Context, w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/create":
		p.handleCreate(w, r)
	default:
		http.NotFound(w, r)
	}
}

type CreateAPIRequest struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	PostID string `json:"post_id"`
}

func (p *Plugin) handleCreate(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-ID")
	if userID == "" {
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}

	var createRequest *CreateAPIRequest
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&createRequest)
	if err != nil {
		p.API.LogError("Unable to decode JSON err=" + err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	config := p.getConfiguration()

	ownerAndRepo := ""
	switch createRequest.Type {
	case "admin":
		ownerAndRepo = config.AdminRepository
	case "developer":
		ownerAndRepo = config.DeveloperRepository
	case "handbook":
		ownerAndRepo = config.HandbookRepository
	}

	if ownerAndRepo == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	repoSplit := strings.Split(ownerAndRepo, "/")
	if len(repoSplit) != 2 {
		p.API.LogError("Bad configured repo: " + ownerAndRepo)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	owner := repoSplit[0]
	repo := repoSplit[1]

	labels := []string{}
	if config.Labels != "" {
		labels = strings.Split(config.Labels, ",")
	}

	user, appErr := p.API.GetUser(userID)
	if appErr != nil {
		p.API.LogError("Unable to get user err=" + appErr.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	serverConfig := p.API.GetConfig()

	docPost, appErr := p.API.GetPost(createRequest.PostID)
	if appErr != nil {
		p.API.LogError("Unable to get post err=" + appErr.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	rootID := docPost.RootId
	if rootID == "" {
		rootID = docPost.Id
	}

	permalink, err := url.Parse(*serverConfig.ServiceSettings.SiteURL)
	permalink.Path = path.Join(permalink.Path, "_redirect", "pl", docPost.Id)

	body := fmt.Sprintf("Mattermost user `%s` from %s has requested the following be documented:\n\n```\n%s\n```\n\nSee the original post [here](%s).\n\n_This issue was generated from [Mattermost](https://mattermost.com) using the [Doc Up](https://github.com/jwilander/mattermost-plugin-docup) plugin._",
		user.Username,
		*serverConfig.ServiceSettings.SiteURL,
		createRequest.Body,
		permalink.String(),
	)

	issueRequest := &github.IssueRequest{
		Title:  NewString("Request for Documentation: " + createRequest.Title),
		Body:   NewString(body),
		Labels: &labels,
	}

	issue, _, err := p.github.Issues.Create(context.Background(), owner, repo, issueRequest)
	if err != nil {
		p.API.LogError("Error creating GitHub issue err=" + err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}


	post := &model.Post{
		UserId:    userID,
		ChannelId: docPost.ChannelId,
		RootId:    rootID,
		Message:   fmt.Sprintf("Marked [this post](%s) for documentation [here](%s).\n\n_Generated by the [Doc Up](https://github.com/jwilander/mattermost-plugin-docup) plugin._", permalink.String(), issue.GetHTMLURL()),
	}

	_, appErr = p.API.CreatePost(post)
	if appErr != nil {
		p.API.LogError("Unable to create post err=" + appErr.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func NewString(s string) *string { return &s }
