package kraken

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

type KrakenWS struct {
	conn       *websocket.Conn
	endpoint   string
	orderBooks map[string]*struct {
		timeStamp time.Time
		book      exchange.OrderBook
	}
	mu sync.RWMutex
}

func NewKrakenWS() *KrakenWS {
	return &KrakenWS{
		endpoint: "wss://ws.kraken.com",
		orderBooks: make(map[string]*struct {
			timeStamp time.Time
			book      exchange.OrderBook
		}),
	}
}

func (k *KrakenWS) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, k.endpoint, nil)
	if err != nil {
		return fmt.Errorf("WebSocket connection failed: %w", err)
	}
	k.conn = conn
	log.Println("Successfully connected to Kraken WebSocket")

	go k.pingLoop(ctx)
	go k.handleMessages(ctx)

	return nil
}

func (k *KrakenWS) SubscribeToOrderBook(coin string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	// Kraken uses pairs such as "XBT/USD" for Bitcoin.
	pair := coin
	if strings.ToUpper(coin) == "BTC" {
		pair = "XBT/USD"
	} else {
		pair = strings.ToUpper(coin) + "/USD"
	}
	subMsg := map[string]interface{}{
		"event": "subscribe",
		"pair":  []string{pair},
		"subscription": map[string]interface{}{
			"name":  "book",
			"depth": 5,
		},
	}
	if err := k.conn.WriteJSON(subMsg); err != nil {
		return fmt.Errorf("failed to subscribe to Kraken order book: %w", err)
	}
	log.Printf("Successfully subscribed to Kraken order book for %s", coin)
	return nil
}

func (k *KrakenWS) GetOrderBook(coin string) (exchange.OrderBook, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	entry, exists := k.orderBooks[coin]
	if !exists {
		return exchange.OrderBook{}, fmt.Errorf("no order book for %s", coin)
	}
	if time.Since(entry.timeStamp) > 5*time.Second {
		return exchange.OrderBook{}, fmt.Errorf("stale order book for %s", coin)
	}
	return entry.book, nil
}

func (k *KrakenWS) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := k.conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				log.Printf("Kraken ping error: %v", err)
				return
			}
		}
	}
}

func (k *KrakenWS) handleMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := k.conn.ReadMessage()
			if err != nil {
				log.Printf("Kraken WebSocket read error: %v", err)
				return
			}
			// Kraken messages arrive as arrays.
			var rawMsg interface{}
			if err := json.Unmarshal(message, &rawMsg); err != nil {
				continue
			}
			msgArray, ok := rawMsg.([]interface{})
			if !ok || len(msgArray) < 4 {
				continue
			}
			// The second element should contain the update data.
			data, ok := msgArray[1].(map[string]interface{})
			if !ok {
				continue
			}
			k.updateOrderBook(data, msgArray[3])
		}
	}
}

func (k *KrakenWS) updateOrderBook(data map[string]interface{}, pair interface{}) {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Determine coin from the pair string (e.g., "XBT/USD")
	pairStr, ok := pair.(string)
	if !ok {
		return
	}
	coin := strings.Split(pairStr, "/")[0]
	if coin == "XBT" {
		coin = "BTC"
	}
	if _, exists := k.orderBooks[coin]; !exists {
		k.orderBooks[coin] = &struct {
			timeStamp time.Time
			book      exchange.OrderBook
		}{
			book:      exchange.OrderBook{},
			timeStamp: time.Now(),
		}
	}
	var newBids []exchange.Order
	var newAsks []exchange.Order

	// Kraken sends snapshots in "as" and "bs", and updates in "a" and "b".
	if asks, ok := data["as"]; ok {
		if arr, ok := asks.([]interface{}); ok {
			for _, item := range arr {
				if rec, ok := item.([]interface{}); ok && len(rec) >= 2 {
					priceStr, _ := rec[0].(string)
					volumeStr, _ := rec[1].(string)
					price, err1 := strconv.ParseFloat(priceStr, 64)
					volume, err2 := strconv.ParseFloat(volumeStr, 64)
					if err1 != nil || err2 != nil {
						continue
					}
					newAsks = append(newAsks, exchange.Order{Price: price, Amount: volume, Exchange: "Kraken"})
				}
			}
		}
	}
	if bids, ok := data["bs"]; ok {
		if arr, ok := bids.([]interface{}); ok {
			for _, item := range arr {
				if rec, ok := item.([]interface{}); ok && len(rec) >= 2 {
					priceStr, _ := rec[0].(string)
					volumeStr, _ := rec[1].(string)
					price, err1 := strconv.ParseFloat(priceStr, 64)
					volume, err2 := strconv.ParseFloat(volumeStr, 64)
					if err1 != nil || err2 != nil {
						continue
					}
					newBids = append(newBids, exchange.Order{Price: price, Amount: volume, Exchange: "Kraken"})
				}
			}
		}
	}
	// Also process incremental updates if available.
	if asks, ok := data["a"]; ok {
		if arr, ok := asks.([]interface{}); ok {
			for _, item := range arr {
				if rec, ok := item.([]interface{}); ok && len(rec) >= 2 {
					priceStr, _ := rec[0].(string)
					volumeStr, _ := rec[1].(string)
					price, err1 := strconv.ParseFloat(priceStr, 64)
					volume, err2 := strconv.ParseFloat(volumeStr, 64)
					if err1 != nil || err2 != nil {
						continue
					}
					newAsks = append(newAsks, exchange.Order{Price: price, Amount: volume, Exchange: "Kraken"})
				}
			}
		}
	}
	if bids, ok := data["b"]; ok {
		if arr, ok := bids.([]interface{}); ok {
			for _, item := range arr {
				if rec, ok := item.([]interface{}); ok && len(rec) >= 2 {
					priceStr, _ := rec[0].(string)
					volumeStr, _ := rec[1].(string)
					price, err1 := strconv.ParseFloat(priceStr, 64)
					volume, err2 := strconv.ParseFloat(volumeStr, 64)
					if err1 != nil || err2 != nil {
						continue
					}
					newBids = append(newBids, exchange.Order{Price: price, Amount: volume, Exchange: "Kraken"})
				}
			}
		}
	}
	k.orderBooks[coin].book = exchange.OrderBook{Bids: newBids, Asks: newAsks}
	k.orderBooks[coin].timeStamp = time.Now()
}
