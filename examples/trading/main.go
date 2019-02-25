/*
This is a simple app that demonstrates placing a trade on Bitfinex using
supplied API keys.
*/
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"

	"code.cryptowat.ch/cw-sdk-go/client/websocket"
	"code.cryptowat.ch/cw-sdk-go/common"
)

var (
	apiKey            = flag.String("apikey", "", "API key to use")
	secretKey         = flag.String("secretkey", "", "Secret key to use")
	marketID          = flag.String("marketid", "", "Market to trade on")
	exchangeAPIKey    = flag.String("exchangekey", "", "Exchange API key")
	exchangeSecretKey = flag.String("exchangesecret", "", "Exchange secret key")
	url               = flag.String("url", "", "Trading API url")
	mode              = flag.String("mode", "place", "PlaceOrder or CancelOrder mode")
	orderID           = flag.String("orderId", "", "OrderID to cancel")
)

func main() {
	flag.Parse()

	if *mode != "place" && *mode != "cancel" {
		log.Println("mode must be either place or cancel")
		os.Exit(1)
	}

	if *mode == "cancel" && *orderID == "" {
		log.Println("orderId must be a non-empty string")
		os.Exit(1)
	}

	ready := make(chan struct{}, 1)
	clientErr := make(chan error, 1)
	client, err := setupClient(ready, clientErr)
	if err != nil {
		panic(err)
	}

	done := make(chan struct{})
	tradeErr := make(chan error, 1)
	go trade(client, ready, done, tradeErr)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	select {
	case <-interrupt:
		log.Printf("Closing connection...\n")
		close(done)
	case err := <-clientErr:
		log.Printf("Client error: %s\n", err)
		close(done)
		signal.Stop(interrupt)
	case err := <-tradeErr:
		if err != nil {
			log.Printf("Trading error: %s\n", err)
		}
		signal.Stop(interrupt)
	}

	if err := client.Close(); err != nil {
		log.Fatalf("Failed to close connection: %s", err)
	}
}

func setupClient(readyChan chan struct{}, errChan chan<- error) (*websocket.TradeClient, error) {
	tc, err := websocket.NewTradeClient(&websocket.TradeClientParams{
		WSParams: &websocket.WSParams{
			APIKey:    *apiKey,
			SecretKey: *secretKey,
			URL:       *url,
		},
		Subscriptions: []*websocket.TradeSubscription{
			&websocket.TradeSubscription{
				MarketID: common.MarketID(*marketID),

				// If Auth is left out, the client will fall back on your bitfinex
				// keys stored in Cryptowatch
				Auth: &websocket.TradeSessionAuth{
					APIKey:    *exchangeAPIKey,
					APISecret: *exchangeSecretKey,
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	tc.OnStateChange(
		websocket.ConnStateAny,
		func(oldState, state websocket.ConnState) {
			log.Printf(
				"State updated: %s -> %s",
				websocket.ConnStateNames[oldState],
				websocket.ConnStateNames[state],
			)
		},
	)

	tc.OnSubscriptionResult(func(sr websocket.SubscriptionResult) {
		log.Println("subscription result", sr)
	})

	tc.OnError(func(mID common.MarketID, err error, disconnecting bool) {
		if err != nil {
			errChan <- err
		}
	})

	tc.OnReady(func() {
		readyChan <- struct{}{}
	})

	tc.Connect()

	return tc, nil
}

func trade(client *websocket.TradeClient, ready <-chan struct{}, done <-chan struct{}, errChan chan<- error) {
	for {
		select {
		case <-ready:
			switch *mode {
			case "place":
				log.Println("Trading ready: placing order...")

				order, err := client.PlaceOrder(common.PlaceOrderOpt{
					PriceParams: []*common.PriceParam{
						&common.PriceParam{
							Type:  common.AbsoluteValuePrice,
							Value: "0.01",
						},
					},
					MarketID:  common.MarketID(*marketID),
					Amount:    "0.01",
					OrderSide: common.BuyOrder,
					OrderType: common.LimitOrder,
				})

				if err == nil {
					log.Println("Order placed:", order)
				}

				errChan <- err
				return
			case "cancel":
				log.Println("Trading ready: canceling order...")

				err := client.CancelOrder(common.CancelOrderOpt{
					MarketID: common.MarketID(*marketID),
					OrderID:  *orderID,
				})

				if err == nil {
					log.Println("Order canceled:", *orderID)
				}

				errChan <- err
				return
			}
		case <-done:
			return
		}
	}
}
