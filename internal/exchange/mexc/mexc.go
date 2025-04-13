package mexc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"notlelouch/ArbiBot/internal/exchange"

	"github.com/gorilla/websocket"
)

type MEXCWS struct {
	conn       *websocket.Conn
	endpoint   string
	orderBooks map[string]*struct {
		timeStamp time.Time
		book      exchange.OrderBook
	}
	mu sync.RWMutex
}

type DepthMessage struct {
	Channel string `json:"channel"`
	Data    struct {
		Symbol    string     `json:"symbol"`
		Bids      [][]string `json:"bids"`
		Asks      [][]string `json:"asks"`
		Timestamp int64      `json:"timestamp"`
	} `json:"data"`
}

func NewMEXCWS() *MEXCWS {
	return &MEXCWS{
		endpoint: "wss://wbs.mexc.com/ws",
		orderBooks: make(map[string]*struct {
			timeStamp time.Time
			book      exchange.OrderBook
		}),
	}
}

func (m *MEXCWS) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, m.endpoint, nil)
	if err != nil {
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}
	m.conn = conn
	log.Println("Successfully connected to MEXC WebSocket")

	go m.pingLoop(ctx)
	go m.handleMessages(ctx)

	return nil
}

func (m *MEXCWS) SubscribeToOrderBook(coin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Assuming symbol format "BTC_USDT"
	symbol := strings.ToUpper(coin) + "_USDT"
	// Subscription message – adjust the method/params per MEXC API specification.
	subMsg := map[string]interface{}{
		"method": "sub.deals", // or "sub.depth" if applicable
		"params": map[string]interface{}{
			"symbol": symbol,
			"depth":  5,
		},
		"id": 1,
	}
	if err := m.conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("failed to subscribe to MEXC order book: %w", err)
	}
	log.Printf("Successfully subscribed to MEXC depth for %s", coin)
	return nil
}

func (m *MEXCWS) GetOrderBook(coin string) (exchange.OrderBook, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, exists := m.orderBooks[coin]
	if !exists {
		return exchange.OrderBook{}, fmt.Errorf("no order book for %s", coin)
	}
	if time.Since(entry.timeStamp) > 5*time.Second {
		return exchange.OrderBook{}, fmt.Errorf("stale order book for %s", coin)
	}
	return entry.book, nil
}

func (m *MEXCWS) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				log.Printf("MEXC ping error: %v", err)
				return
			}
		}
	}
}

func (m *MEXCWS) handleMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := m.conn.ReadMessage()
			if err != nil {
				log.Printf("MEXC WebSocket read error: %v", err)
				return
			}
			var depthMsg DepthMessage
			if err := json.Unmarshal(message, &depthMsg); err != nil {
				continue
			}
			// Assuming the channel indicates a depth update.
			if strings.Contains(depthMsg.Channel, "depth") {
				m.updateOrderBook(&depthMsg)
			}
		}
	}
}

func (m *MEXCWS) updateOrderBook(msg *DepthMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Extract coin from symbol, e.g. "BTC_USDT" → "BTC"
	parts := strings.Split(msg.Data.Symbol, "_")
	coin := parts[0]
	if _, exists := m.orderBooks[coin]; !exists {
		m.orderBooks[coin] = &struct {
			timeStamp time.Time
			book      exchange.OrderBook
		}{
			book:      exchange.OrderBook{},
			timeStamp: time.Now(),
		}
	}
	newBids := make([]exchange.Order, 0, len(msg.Data.Bids))
	newAsks := make([]exchange.Order, 0, len(msg.Data.Asks))

	for _, bid := range msg.Data.Bids {
		if len(bid) < 2 {
			continue
		}
		price, err := strconv.ParseFloat(bid[0], 64)
		if err != nil {
			continue
		}
		amount, err := strconv.ParseFloat(bid[1], 64)
		if err != nil {
			continue
		}
		newBids = append(newBids, exchange.Order{Price: price, Amount: amount, Exchange: "MEXC"})
	}

	for _, ask := range msg.Data.Asks {
		if len(ask) < 2 {
			continue
		}
		price, err := strconv.ParseFloat(ask[0], 64)
		if err != nil {
			continue
		}
		amount, err := strconv.ParseFloat(ask[1], 64)
		if err != nil {
			continue
		}
		newAsks = append(newAsks, exchange.Order{Price: price, Amount: amount, Exchange: "MEXC"})
	}

	m.orderBooks[coin].book = exchange.OrderBook{Bids: newBids, Asks: newAsks}
	m.orderBooks[coin].timeStamp = time.Now()
}
