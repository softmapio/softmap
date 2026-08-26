package service

import (
	"net/http"
	"strings"

	"example.com/toyshop/model"
)

// Notifier has two live implementations so calls through it must appear as
// edges to both, tagged "static-multi".
type Notifier interface {
	Notify(o *model.Order) error
}

type EmailNotifier struct {
	Endpoint string
}

func (n *EmailNotifier) Notify(o *model.Order) error {
	_, err := http.Post(n.Endpoint, "application/json", strings.NewReader(o.ID))
	return err
}

type SlackNotifier struct {
	WebhookURL string
	Client     *http.Client
}

func (n *SlackNotifier) Notify(o *model.Order) error {
	req, err := http.NewRequest("POST", n.WebhookURL, strings.NewReader(o.ID))
	if err != nil {
		return err
	}
	_, err = n.Client.Do(req)
	return err
}
