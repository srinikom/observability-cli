package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"

	"github.com/pkg/errors"
	"github.com/spf13/viper"

	"github.com/akitasoftware/akita-libs/akid"
)

// Global flag exposed by the root command.
// This doesn't *really* belong here, but previously it
// was buried in the "internal" package where we couldn't use it.
var Domain string

// Error handling (to call into the telemetry library without
// creating a circular dependency.)
type APIErrorHandler = func(method string, path string, e error)

var (
	apiErrorHandler      APIErrorHandler = nil
	apiErrorHandlerMutex sync.RWMutex
)

func reportError(method string, path string, e error) {
	apiErrorHandlerMutex.RLock()
	defer apiErrorHandlerMutex.RUnlock()
	if apiErrorHandler != nil {
		apiErrorHandler(method, path, e)
	}
}

func SetAPIErrorHandler(f APIErrorHandler) {
	apiErrorHandlerMutex.Lock()
	defer apiErrorHandlerMutex.Unlock()
	apiErrorHandler = f
}

type baseClient struct {
	host     string
	scheme   string // http or https
	clientID akid.ClientID
}

func newBaseClient(rawHost string, cli akid.ClientID) baseClient {
	host := "api." + rawHost
	// If rawHost is IP or IP:port, use that directly. This is mostly to support
	// tests.
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		if net.ParseIP(h) != nil {
			host = rawHost
		}
	} else if net.ParseIP(rawHost) != nil {
		host = rawHost
	}

	c := baseClient{
		scheme:   "https",
		host:     host,
		clientID: cli,
	}
	if viper.GetBool("test_only_disable_https") {
		fmt.Fprintf(os.Stderr, "WARNING: using test backend without SSL\n")
		c.scheme = "http"
	}
	return c
}

// Sends GET request and parses the response as JSON.
func (c baseClient) get(ctx context.Context, path string, resp interface{}) error {
	return c.getWithQuery(ctx, path, nil, resp)
}

func (c baseClient) getWithQuery(ctx context.Context, path string, query url.Values, resp interface{}) (e error) {
	defer func() {
		if e != nil {
			reportError(http.MethodGet, path, e)
		}
	}()
	u := &url.URL{
		Scheme:   c.scheme,
		Host:     c.host,
		Path:     path,
		RawQuery: query.Encode(),
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.Wrap(err, "failed to create HTTP GET request")
	}

	respContent, err := sendRequest(ctx, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(respContent, resp); err != nil {
		return errors.Wrap(err, "failed to unmarshal response body as JSON")
	}
	return nil
}

// Sends POST request after marshaling body into JSON and parses the response as
// JSON.
func (c baseClient) post(ctx context.Context, path string, body interface{}, resp interface{}) (e error) {
	defer func() {
		if e != nil {
			reportError(http.MethodGet, path, e)
		}
	}()

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return errors.Wrap(err, "failed to marshal request body into JSON")
	}

	u := &url.URL{
		Scheme: c.scheme,
		Host:   c.host,
		Path:   path,
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return errors.Wrap(err, "failed to create HTTP POST request")
	}
	req.Header.Set("content-type", "application/json")

	respContent, err := sendRequest(ctx, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(respContent, resp); err != nil {
		return errors.Wrap(err, "failed to unmarshal response body as JSON")
	}
	return nil
}
