package stream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/nomad/structs"
)

func defaultHttpClient() *http.Client {
	httpClient := cleanhttp.DefaultClient()
	transport := httpClient.Transport.(*http.Transport)
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	return httpClient
}

type SinkCfg struct {
	Address string

	// HttpClient is the client to use. Default will be used if not provided.
	HttpClient *http.Client
}

func defaultCfg() *SinkCfg {
	cfg := &SinkCfg{
		HttpClient: defaultHttpClient(),
	}
	return cfg
}

type WebhookSink struct {
	client *http.Client
	config SinkCfg

	subscription *Subscription

	// lastIndex should be accessed atomically
	lastIndex uint64

	l hclog.Logger
}

func NewWebhookSink(cfg *SinkCfg, broker *EventBroker, subReq *SubscribeRequest) (*WebhookSink, error) {
	defConfig := defaultCfg()

	if cfg.Address == "" {
		return nil, fmt.Errorf("invalid address for websink")
	} else if _, err := url.Parse(cfg.Address); err != nil {
		return nil, fmt.Errorf("invalid address '%s' : %v", cfg.Address, err)
	}

	httpClient := defConfig.HttpClient

	sub, err := broker.Subscribe(subReq)
	if err != nil {
		return nil, fmt.Errorf("configuring webhook sink subscription: %w", err)
	}

	return &WebhookSink{
		client:       httpClient,
		config:       *cfg,
		subscription: sub,
	}, nil
}

func NewWebhookSinks(eventSink *structs.EventSink) (*WebhookSink, error) {
	defConfig := defaultCfg()

	if eventSink.Address == "" {
		return nil, fmt.Errorf("invalid address for websink")
	} else if _, err := url.Parse(eventSink.Address); err != nil {
		return nil, fmt.Errorf("invalid address '%s' : %v", eventSink.Address, err)
	}

	httpClient := defConfig.HttpClient

	return &WebhookSink{
		client: httpClient,
	}, nil
}

func (ws *WebhookSink) Start(ctx context.Context) {
	defer ws.subscription.Unsubscribe()

	// TODO handle reconnect
	for {
		events, err := ws.subscription.Next(ctx)
		if err != nil {
			if err == ErrSubscriptionClosed {

			}
			return
			// TODO handle err
		}
		if len(events.Events) == 0 {
			continue
		}

		if err := ws.Send(ctx, &events); err != nil {
			ws.l.Error("failed to sending event to webhook", "error", err)
			continue
		}
		// Update last successfully sent index
		atomic.StoreUint64(&ws.lastIndex, events.Index)
	}
}

func (ws *WebhookSink) Send(ctx context.Context, e *structs.Events) error {
	req, err := ws.toRequest(e)
	if err != nil {
		return fmt.Errorf("converting event to request: %w", err)
	}
	req.WithContext(ctx)

	err = ws.doRequest(req)
	if err != nil {
		return fmt.Errorf("sending request to webhook %w", err)
	}

	return nil
}

func (ws *WebhookSink) doRequest(req *http.Request) error {
	_, err := ws.client.Do(req)
	if err != nil {
		return err
	}

	return nil

}

func (ws *WebhookSink) toRequest(e *structs.Events) (*http.Request, error) {
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ws.config.Address, buf)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-Type", "application/json")

	return req, nil
}
