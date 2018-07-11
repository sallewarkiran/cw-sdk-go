package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	pbm "code.cryptowat.ch/stream-client-go/proto/markets"
	streamclient "code.cryptowat.ch/stream-client-go"
	"code.cryptowat.ch/stream-client-go/examples/kraken-trades/cwrest"
)

const (
	exchangeSymbol = "kraken"
)

var (
	apiKey    = flag.String("apikey", "", "API key to use.")
	secretKey = flag.String("secretkey", "", "Secret key to use.")
)

func init() {
	flag.Parse()
}

func main() {
	rest := cwrest.NewCWRESTClient("https://api.cryptowat.ch")

	// Get exchange description, in particular we'll need the ID to use it
	// in stream subscriptions
	exchange, err := rest.GetExchangeDescr(exchangeSymbol)
	if err != nil {
		log.Fatalf("failed to get details of %s: %s", exchangeSymbol, err)
	}

	// Get market descriptions, to know symbols (like "btcusd") by integer ID
	marketsSlice, err := rest.GetExchangeMarketsDescr(exchangeSymbol)
	if err != nil {
		log.Fatalf("failed to get markets of %s: %s", exchangeSymbol, err)
	}

	markets := map[int]cwrest.MarketDescr{}
	for _, market := range marketsSlice {
		markets[market.ID] = market
	}

	// Create a new stream connection instance. Note that the actual connection
	// will happen later.
	c, err := streamclient.NewStreamConn(&streamclient.StreamParams{
		URL: "wss://stream.cryptowat.ch",

		Reconnect:        true,
		ReconnectTimeout: 1 * time.Second,
		Backoff:          true,
		Subscriptions: []string{
			fmt.Sprintf("exchanges:%d:trades", exchange.ID),
		},

		APIKey:    *apiKey,
		SecretKey: *secretKey,
	})
	if err != nil {
		log.Fatalf("%s", err)
	}

	// Ask for the state transition updates, and present them to the user somehow
	c.AddStateListener(
		streamclient.StateAny,
		func(conn *streamclient.StreamConn, oldState, state streamclient.State, cause error) {
			causeStr := ""
			if cause != nil {
				causeStr = fmt.Sprintf(" (%s)", cause)
			}
			log.Printf(
				"State updated: %s -> %s%s",
				streamclient.StateNames[oldState],
				streamclient.StateNames[state],
				causeStr,
			)
		},
	)

	// Listen for received market messages and print them
	c.AddMarketListener(
		func(conn *streamclient.StreamConn, msg *pbm.MarketUpdateMessage) {
			market := markets[int(msg.Market.MarketId)]

			tradesUpdate := msg.GetTradesUpdate()
			if tradesUpdate == nil {
				// Got some market update other than trades, we're not interested in it
				return
			}

			for _, trade := range tradesUpdate.Trades {
				log.Printf(
					"Trade: %s: price: %s, amount: %s",
					market.Pair, trade.PriceStr, trade.AmountStr,
				)
			}
		},
	)

	// Finally, connect.
	c.Connect()

	// Setup OS signal handler
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// Wait until the OS signal is received, at which point we'll close the
	// connection and quit
	<-interrupt
	log.Printf("Closing connection...\n")

	if err := c.Close(); err != nil {
		log.Fatalf("Failed to close connection: %s", err)
	}
}
