package mqtt

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

// UpdateEvent is the lightweight metadata published to MQTT when a site's cookies are updated.
// Per CookieSyncChromeExtension/docs/architecture.md section 4, cookie values are NOT sent over MQTT.
type UpdateEvent struct {
	Origin      string `json:"origin"`
	CookieCount int    `json:"cookieCount"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// Publisher publishes site update events to an MQTT broker if configured.
type Publisher struct {
	client pahomqtt.Client
	topic  string
	logger *slog.Logger
}

// NewPublisher connects to an MQTT broker if brokerURL is non-empty.
// If brokerURL is empty, it returns a no-op Publisher.
func NewPublisher(brokerURL, topic, username, password string, logger *slog.Logger) (*Publisher, error) {
	if strings.TrimSpace(brokerURL) == "" {
		return &Publisher{client: nil, topic: topic, logger: logger}, nil
	}
	if strings.TrimSpace(topic) == "" {
		topic = "cookiesync/updated"
	}

	opts := pahomqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("cookiesyncsvr-%d", time.Now().UnixNano())).
		SetAutoReconnect(true).
		SetConnectTimeout(5 * time.Second)

	if username != "" {
		opts.SetUsername(username)
		opts.SetPassword(password)
	}

	client := pahomqtt.NewClient(opts)
	token := client.Connect()
	if token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return nil, fmt.Errorf("connect mqtt broker %s: %w", brokerURL, token.Error())
	}

	logger.Info("connected to MQTT broker", "broker", brokerURL, "topic", topic)
	return &Publisher{
		client: client,
		topic:  topic,
		logger: logger,
	}, nil
}

// PublishUpdate sends an UpdateEvent to the configured MQTT topic.
func (p *Publisher) PublishUpdate(event UpdateEvent) {
	if p == nil || p.client == nil || !p.client.IsConnected() {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		p.logger.Warn("failed to marshal MQTT event", "err", err)
		return
	}
	token := p.client.Publish(p.topic, 1, false, payload)
	go func() {
		if token.WaitTimeout(3*time.Second) && token.Error() != nil {
			p.logger.Warn("failed to publish MQTT update", "err", token.Error())
		}
	}()
}

// Close disconnects the MQTT client cleanly.
func (p *Publisher) Close() {
	if p != nil && p.client != nil && p.client.IsConnected() {
		p.client.Disconnect(250)
	}
}
