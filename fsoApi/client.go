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
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new FSO API client
func NewClient(port uint16) *Client {
	return &Client{
		baseURL: fmt.Sprintf(baseURLFormat, port),
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

func (c *Client) sendRequestWithResponse(req *http.Request, v interface{}) (err error) {
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.SetBasicAuth("admin", "admin")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer func() {
		closeErr := res.Body.Close()

		if err == nil {
			err = closeErr
		}
	}()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New(fmt.Sprintf("HTTP call failed with code %v", res.StatusCode))
	}

	if err = json.NewDecoder(res.Body).Decode(&v); err != nil {
		return
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
		Name:     name,
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

type PlayerData struct {
	Id       int32  `json:"id,omitempty"`
	Address  string `json:"address,omitempty"`
	Ping     int32  `json:"ping,omitempty"`
	Host     bool   `json:"host,omitempty"`
	Observer bool   `json:"observer,omitempty"`
	Callsign string `json:"callsign,omitempty"`
	Ship     string `json:"ship,omitempty"`
}

// SetServerName updates the name of the server to the specified value
func (c *Client) GetPlayers(ctx context.Context) ([]PlayerData, error) {
	req, err := http.NewRequest(http.MethodGet, c.getUrl("player"), nil)
	if err != nil {
		return nil, err
	}

	req = req.WithContext(ctx)

	var players []PlayerData

	if err := c.sendRequestWithResponse(req, &players); err != nil {
		return nil, err
	}

	return players, nil
}

// Close frees unused resources used by the client
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
