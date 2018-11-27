package main

// import (
// 	"flag"
// 	"fmt"
// 	"log"
// 	"os"
// 	"os/signal"
// 	"strconv"
// 	"time"

// 	pbm "code.cryptowat.ch/ws-client-go/proto/markets"
// 	"code.cryptowat.ch/ws-client-go"
// )

// var (
// 	apiKey    = flag.String("apikey", "", "API key to use.")
// 	secretKey = flag.String("secretkey", "", "Secret key to use.")
// )

// // Simple trading bot using both streamclient and tradeclient
// const (
// 	bitfinexETHBTC = "5" // bitfinex ethbtc
// 	bitfinexBTCUSD = "1"

// 	coinbaseETHBTC = "69"
// )

// var USDPrice float64

// // Channels to help with incoming data
// var spreadUpdates = make(chan *pbm.OrderBookSpreadUpdate)
// var priceUpdates = make(chan *pbm.OrderBookSpreadUpdate)

// func init() {
// 	log.SetFlags(log.LstdFlags | log.Lshortfile)
// }

// func main() {
// 	tc := initTradeClient()

// 	// Start data streaming and trade handling once the client is initialized
// 	tc.OnReady(func() {
// 		log.Println("Trade client initialized")
// 		log.Println("Open orders")
// 		log.Println(tc.GetOrders())

// 		// Listen to spread updates for btc/usd to keep track of usd price
// 		go priceUpdateLoop()

// 		// Receive spread updates for eth/btc from handleStreamData
// 		go makeTrades(tc)

// 		// Connect to stream and listen to order book data from eth/btc and btc/usd
// 		go handleStreamData()
// 	})

// 	tc.OnError(func(err error) {
// 		log.Println("Trade client error:", err)
// 	})

// 	// Log state transitions
// 	tc.AddStateListener(
// 		wsclient.ConnStateAny,
// 		func(oldState, state wsclient.ConnState, cause error) {
// 			causeStr := ""
// 			if cause != nil {
// 				causeStr = fmt.Sprintf(" (%s)", cause)
// 			}
// 			log.Printf(
// 				"State updated: %s -> %s%s",
// 				wsclient.ConnStateNames[oldState],
// 				wsclient.ConnStateNames[state],
// 				causeStr,
// 			)
// 		},
// 	)

// 	tc.Connect()

// 	// Setup OS signal handler
// 	interrupt := make(chan os.Signal, 1)
// 	signal.Notify(interrupt, os.Interrupt)

// 	// Wait until the OS signal is received, at which point we'll close the
// 	// connection and quit
// 	<-interrupt
// 	log.Printf("Closing connection...\n")
// }

// // Create a trade client, set handlers for all the listeners
// func initTradeClient() *wsclient.TradeClient {
// 	tc, err := wsclient.NewTradeClient(&wsclient.WSParams{
// 		APIKey:        *apiKey,
// 		SecretKey:     *secretKey,
// 		Subscriptions: []string{bitfinexETHBTC},
// 	})
// 	if err != nil {
// 		log.Fatalf("NewTradeClient error: %v", err)
// 	}

// 	tc.OnOrdersUpdate(func(orders []wsclient.PrivateOrder) {
// 		for _, order := range tc.GetOrders() {
// 			log.Println(order)
// 		}
// 	})

// 	tc.OnTradesUpdate(func(trades []wsclient.PrivateTrade) {
// 		log.Println("Trades Update", trades)
// 	})

// 	tc.OnBalancesUpdate(func(balances wsclient.Balances) {
// 		log.Println("Balances Update", balances)
// 	})

// 	tc.OnPositionsUpdate(func(positions []wsclient.PrivatePosition) {
// 		log.Println("Positions Update", positions)
// 	})

// 	return tc
// }

// func handleStreamData() {
// 	log.Println("Starting data stream")

// 	// Create a new stream connection instance. Note that the actual connection
// 	// will happen later.
// 	sc, err := wsclient.NewStreamClient(&wsclient.WSParams{
// 		Subscriptions: []string{
// 			fmt.Sprintf("markets:%s:book:spread", coinbaseETHBTC),
// 			fmt.Sprintf("markets:%s:book:spread", bitfinexETHBTC),
// 			fmt.Sprintf("markets:%s:book:spread", bitfinexBTCUSD),
// 		},

// 		APIKey:    *apiKey,
// 		SecretKey: *secretKey,
// 	})
// 	if err != nil {
// 		log.Fatalf("%s", err)
// 	}

// 	// Listen for received market messages and print them
// 	sc.AddMarketListener(
// 		func(msg *pbm.MarketUpdateMessage) {
// 			spreadUpdate := msg.GetOrderBookSpreadUpdate()
// 			if spreadUpdate == nil {
// 				// Got some market update other than trades, we're not interested in it
// 				return
// 			}

// 			if msg.Market.MarketId == 1 {
// 				// Update BTC price (USD)
// 				select {
// 				case priceUpdates <- spreadUpdate:
// 					// no-op
// 				default:
// 					// ignore
// 				}
// 			} else {
// 				// Handle trading
// 				select {
// 				case spreadUpdates <- spreadUpdate:
// 					// no-op
// 				default:
// 					// ignore
// 				}
// 			}
// 		},
// 	)

// 	// Log state transitions
// 	sc.AddStateListener(
// 		wsclient.ConnStateAny,
// 		func(oldState, state wsclient.ConnState, cause error) {
// 			causeStr := ""
// 			if cause != nil {
// 				causeStr = fmt.Sprintf(" (%s)", cause)
// 			}
// 			log.Printf(
// 				"State updated: %s -> %s%s",
// 				wsclient.ConnStateNames[oldState],
// 				wsclient.ConnStateNames[state],
// 				causeStr,
// 			)
// 		},
// 	)

// 	sc.Connect()
// }

// // Update our BTC/USD price every 15 seconds
// func priceUpdateLoop() {
// 	for {
// 		pu := <-priceUpdates
// 		USDPrice = (formatFloat(pu.Bid.PriceStr) + formatFloat(pu.Ask.PriceStr)) / 2
// 		time.Sleep(15 * time.Second)
// 	}
// }

// func makeTrades(tc *wsclient.TradeClient) {
// 	// for {
// 	su := <-spreadUpdates

// 	price := formatFloat(su.Bid.PriceStr)
// 	// Make a new buy price which is 20% less than the current bid (won't fill immediately)
// 	newPrice := floatToString(price - price*0.2)

// 	log.Println("Placing order...")

// 	_, err := tc.PlaceOrder(wsclient.OrderParams{
// 		Amount: "0.1",
// 		PriceParams: wsclient.PriceParams{
// 			&wsclient.PriceParam{
// 				Value: newPrice,
// 				Type:  wsclient.AbsoluteValuePrice,
// 			},
// 		},
// 		OrderSide:   wsclient.BuyOrder,
// 		Type:        wsclient.LimitOrder,
// 		FundingType: wsclient.SpotFunding,
// 	})

// 	if err != nil {
// 		log.Fatal("Order failed: ", err)
// 	}

// 	log.Println("Order placed! Waiting 5 seconds...")
// 	time.Sleep(5 * time.Second)

// 	orders := tc.GetOrders()

// 	if len(orders) > 3 {
// 		log.Println("Cancelling open orders...")
// 		for _, o := range orders {
// 			go func(o wsclient.PrivateOrder) {
// 				err := tc.CancelOrder(o)
// 				if err != nil {
// 					log.Fatalf("CancelOrder error: %v", err)
// 				}
// 			}(o)
// 		}
// 	}
// }

// func formatFloat(floatStr string) float64 {
// 	f, e := strconv.ParseFloat(floatStr, 64)
// 	if e != nil {
// 		log.Fatalf("ParseFLoat error: %v", e)
// 	}
// 	return f
// }

// func floatToString(f float64) string {
// 	return strconv.FormatFloat(f, 'f', -1, 64)
// }
