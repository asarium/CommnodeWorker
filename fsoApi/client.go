package fsoApi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	baseURLFormat = "http://127.0.0.1:%v/api/1/"
)

// Client is a FSO server API client
type Client struct {
	baseURL       string
	httpClient    *http.Client
}

// NewClient creates a new FSO API client
func NewClient(port int32) *Client {
	return &Client{
		baseURL:       fmt.Sprintf(baseURLFormat, port),
		httpClient: &http.Client{
			Timeout: time.Minute,
		},
	}
}

func (c *Client) sendRequest(req *http.Request) error {
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.SetBasicAuth("admin", "admin")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}

	err = res.Body.Close()
	if err != nil {
		return err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New(fmt.Sprintf("HTTP call failed with code %v", res.StatusCode))
	}

	return nil
}

func (c *Client) getUrl(apiFunc string) string {
	return c.baseURL + apiFunc
}

type serverData struct {
	Name     string `json:"name,omitempty"`
	Password string `json:"password,omitempty"`
	FrameCap string `json:"framecap,omitempty"`
}

func (c *Client) WaitForOnline(ctx context.Context, timeout time.Duration) error {
	start := time.Now()
	for {
		since := time.Since(start)
		if since >= timeout {
			return errors.New("Timeout ocurred.")
		}

		req, err := http.NewRequest(http.MethodGet, c.getUrl("auth"), nil)
		if err != nil {
			return err
		}

		req = req.WithContext(ctx)

		if err := c.sendRequest(req); err == nil {
			return nil
		}
	}
}

// SetServerName updates the name of the server to the specified value
func (c *Client) SetServerName(ctx context.Context, name string) error {
	serverJson, err := json.Marshal(serverData{
		Name: name,
		FrameCap: "0", // Needs to be set to avoid crashing the server...
	})

	req, err := http.NewRequest(http.MethodPut, c.getUrl("server"), bytes.NewBuffer(serverJson))
	if err != nil {
		return err
	}

	req = req.WithContext(ctx)

	if err := c.sendRequest(req); err != nil {
		return err
	}

	return nil
}

// Close frees unused resources used by the client
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
