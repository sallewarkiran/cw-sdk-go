package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	pbm "code.cryptowat.ch/stream-client-go/proto/markets"
	streamclient "code.cryptowat.ch/stream-client-go"
	"code.cryptowat.ch/stream-client-go/examples/kraken-trades/cwrest"
)

var (
	apiKey    = flag.String("apikey", "", "API key to use.")
	secretKey = flag.String("secretkey", "", "Secret key to use.")

	exchange = flag.String("exchange", "kraken", "")
	pair     = flag.String("pair", "btceur", "")
)

const (
	apiURL    = "https://api.cryptowat.ch"
	streamURL = "wss://stream.cryptowat.ch"
)

func init() {
	flag.Parse()
}

// We'll use this instead of fmt.Println to add timestamps to our printed messages
var outlog = log.New(os.Stdout, "", log.LstdFlags)

func main() {
	rest := cwrest.NewCWRESTClient(apiURL)

	// Get market description, in particular we'll need the ID to use it
	// in the stream subscription.
	market, err := rest.GetMarketDescr(*exchange, *pair)
	if err != nil {
		log.Fatalf("failed to get market %s/%s: %s", *exchange, *pair, err)
	}

	// Get a snapshot of the order book on which we'll apply deltas that we
	// receive from the stream.
	initialBook, err := rest.GetOrderBook(*exchange, *pair)
	if err != nil {
		log.Fatalf("failed to get orderbook of %s/%s: %s",
			*exchange, *pair, err)
	}
	orderbook := NewOrderBookMirror(initialBook)
	outlog.Println(orderbook.PrettyString())

	// Create a new stream connection instance. Note that this does not yet
	// initiate the actual connection.
	c, err := streamclient.NewStreamConn(&streamclient.StreamParams{
		URL: streamURL,

		Subscriptions: []string{
			fmt.Sprintf("markets:%d:book:deltas", market.ID),
		},

		APIKey:    *apiKey,
		SecretKey: *secretKey,
	})
	if err != nil {
		log.Fatalf("%s", err)
	}

	// Ask for the state transition updates and print them
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

	// Listen for market changes
	c.AddMarketListener(
		func(conn *streamclient.StreamConn, msg *pbm.MarketUpdateMessage) {

			// We're only interested in order book deltas.
			// Let's ignore other types of market updates.
			deltaUpdate := msg.GetOrderBookDeltaUpdate()
			if deltaUpdate == nil {
				return
			}

			// First, pretty-print the delta object.
			outlog.Println(PrettyDeltaUpdate{deltaUpdate}.String())

			// If we update the order book, we print it too.
			var updated = false
			defer func() {
				if updated {
					outlog.Println(orderbook.PrettyString())
				}
			}()

			// To ensure our order book object mirrors the true value exactly,
			// the delta updates must always be applied in the order denoted
			// by SeqNum.
			if deltaUpdate.GetSeqNum()-orderbook.SeqNum != 1 {
				// There's a gap in the sequence!
				msg := "SeqNum out of sync. Tried to update " +
					"OrderBook (SeqNum: %d) with OrderBookDeltaUpdate " +
					"(SeqNum: %d)\n"
				log.Printf(msg, orderbook.SeqNum, deltaUpdate.GetSeqNum())

				// For this example it's enough to get a more up-to-date
				// snapshot of the order book to close the gap.
				initialBook, err := rest.GetOrderBook(*exchange, *pair)
				if err != nil {
					log.Fatalf("failed to get orderbook of %s/%s: %s",
						*exchange, *pair, err)
				}
				orderbook = NewOrderBookMirror(initialBook)

				log.Printf("Fetching full orderbook... (SeqNum: %d)\n", orderbook.SeqNum)
				updated = true
			}

			// Update the order book, but only if SeqNum fits.
			if deltaUpdate.GetSeqNum()-orderbook.SeqNum == 1 {
				err := orderbook.Update(deltaUpdate)
				if err != nil {
					log.Fatal(err)
				}
				updated = true
			} else {
				log.Printf("Ignoring OrderBookDeltaUpdate (SeqNum: %d)\n",
					deltaUpdate.GetSeqNum())
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
