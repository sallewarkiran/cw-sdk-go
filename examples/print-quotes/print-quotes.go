package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"time"

	pbm "github.com/cryptowatch/proto/markets"
	streamclient "github.com/cryptowatch/stream-client-go"
	"github.com/cryptowatch/stream-client-go/examples/kraken-trades/cwrest"
)

var (
	apiKey    = flag.String("apikey", "", "API key to use.")
	secretKey = flag.String("secretkey", "", "Secret key to use.")

	pair = flag.String("pair", "", "Pair to watch")
)

func init() {
	flag.Parse()
}

func main() {
	var (
		quoteVals = make(map[uint64]string)

		rest = cwrest.NewCWRESTClient("https://api.cryptowat.ch")

		marketsIndex, marketsIndexErr = rest.GetMarketsIndex()

		marketSymbols = make(map[uint64]string)
	)

	if *pair == "" {
		panic("Need -pair arg")
	}

	pairDescr, pairDescrErr := rest.GetPairDescr(*pair)
	if pairDescrErr != nil {
		panic(pairDescrErr)
	}

	marketsIndex, marketsIndexErr = rest.GetMarketsIndex()

	if marketsIndexErr != nil {
		panic(marketsIndexErr)
	}

	for _, market := range marketsIndex {
		marketSymbols[uint64(market.ID)] = market.Exchange + " " + market.Pair
	}

	c, err := streamclient.NewStreamConn(&streamclient.StreamParams{
		URL: "wss://stream.cryptowat.ch",

		Reconnect:        true,
		ReconnectTimeout: 1 * time.Second,
		Backoff:          true,
		Subscriptions: []string{
			fmt.Sprintf("pairs:%d:trades", pairDescr.ID),
		},

		APIKey:    *apiKey,
		SecretKey: *secretKey,
	})

	if err != nil {
		panic(err)
	}

	c.AddMarketListener(
		func(conn *streamclient.StreamConn, msg *pbm.MarketUpdateMessage) {

			update := msg.GetTradesUpdate()
			if update == nil {
				return
			}

			trades := update.Trades

			quoteVals[msg.Market.MarketId] = trades[len(trades)-1].PriceStr

			printQuotes(marketSymbols, quoteVals)

		},
	)

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

// Simple struct type quote for sorting quotes
type quote struct {
	symbol, value string
}

type quotes []quote

func (qs quotes) Equal(i, j int) bool {
	return qs[i].value == qs[j].value
}

func (qs quotes) Less(i, j int) bool {
	ival, _ := strconv.ParseFloat(qs[i].value, 32)
	jval, _ := strconv.ParseFloat(qs[j].value, 32)

	if ival == jval {
		return qs[i].symbol < qs[j].symbol
	}

	return ival < jval
}

func (qs quotes) Len() int {
	return len(qs)
}

func (qs quotes) Swap(i, j int) {
	qs[i], qs[j] = qs[j], qs[i]
}

// printQuotes draws all of the quotes we have received to the screen
func printQuotes(marketSymbols, quoteVals map[uint64]string) {
	var lines quotes

	for k, v := range quoteVals {
		lines = append(lines, quote{marketSymbols[k], v})
	}

	sort.Sort(sort.Reverse(lines))

	// Clear screen
	print("\033[H\033[2J")

	for _, line := range lines {
		fmt.Printf("%20s %s\n", line.symbol, line.value)
	}

}
