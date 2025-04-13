package binance

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

type BinanceWS struct {
	conn       *websocket.Conn
	endpoint   string
	orderBooks map[string]*struct {
		timeStamp time.Time
		book      exchange.OrderBook
	}
	mu sync.RWMutex
}

type DepthUpdateMessage struct {
	EventType string     `json:"e"`
	EventTime int64      `json:"E"`
	Symbol    string     `json:"s"`
	Bids      [][]string `json:"b"`
	Asks      [][]string `json:"a"`
}

func NewBinanceWS() *BinanceWS {
	return &BinanceWS{
		endpoint: "wss://stream.binance.com:9443/ws",
		orderBooks: make(map[string]*struct {
			timeStamp time.Time
			book      exchange.OrderBook
		}),
	}
}

func (b *BinanceWS) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, b.endpoint, nil)
	if err != nil {
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}
	b.conn = conn
	log.Println("Successfully connected to Binance WebSocket")

	// Start ping loop and message handler
	go b.pingLoop(ctx)
	go b.handleMessages(ctx)

	return nil
}

func (b *BinanceWS) SubscribeToOrderBook(coin string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Binance expects lower-case symbol (e.g. btcusdt)
	symbol := strings.ToLower(coin) + "usdt"
	subscription := map[string]interface{}{
		"method": "SUBSCRIBE",
		"params": []string{symbol + "@depth5"},
		"id":     1,
	}
	if err := b.conn.WriteJSON(subscription); err != nil {
		return fmt.Errorf("failed to subscribe to Binance order book: %w", err)
	}
	log.Printf("Successfully subscribed to Binance depth for %s", coin)
	return nil
}

func (b *BinanceWS) GetOrderBook(coin string) (exchange.OrderBook, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	entry, exists := b.orderBooks[coin]
	if !exists {
		return exchange.OrderBook{}, fmt.Errorf("no order book for %s", coin)
	}
	if time.Since(entry.timeStamp) > 5*time.Second {
		return exchange.OrderBook{}, fmt.Errorf("stale order book for %s", coin)
	}
	return entry.book, nil
}

func (b *BinanceWS) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				log.Printf("Binance ping error: %v", err)
				return
			}
		}
	}
}

func (b *BinanceWS) handleMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := b.conn.ReadMessage()
			if err != nil {
				log.Printf("Binance WebSocket read error: %v", err)
				return
			}
			var depthMsg DepthUpdateMessage
			if err := json.Unmarshal(message, &depthMsg); err != nil {
				continue
			}
			if depthMsg.EventType == "depthUpdate" {
				b.updateOrderBook(&depthMsg)
			}
		}
	}
}

func (b *BinanceWS) updateOrderBook(msg *DepthUpdateMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Convert symbol (e.g. "BTCUSDT") to coin ("BTC")
	symbol := msg.Symbol
	coin := strings.ToUpper(symbol[:len(symbol)-4])
	if _, exists := b.orderBooks[coin]; !exists {
		b.orderBooks[coin] = &struct {
			timeStamp time.Time
			book      exchange.OrderBook
		}{
			book:      exchange.OrderBook{},
			timeStamp: time.Now(),
		}
	}
	newBids := make([]exchange.Order, 0, len(msg.Bids))
	newAsks := make([]exchange.Order, 0, len(msg.Asks))

	for _, bid := range msg.Bids {
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
		newBids = append(newBids, exchange.Order{Price: price, Amount: amount, Exchange: "Binance"})
	}

	for _, ask := range msg.Asks {
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
		newAsks = append(newAsks, exchange.Order{Price: price, Amount: amount, Exchange: "Binance"})
	}

	b.orderBooks[coin].book = exchange.OrderBook{Bids: newBids, Asks: newAsks}
	b.orderBooks[coin].timeStamp = time.Now()
}
