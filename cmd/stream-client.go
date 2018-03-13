package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/cryptowatch/proto/markets"
	"github.com/cryptowatch/stream-client-go"
)

func main() {
	flag.Parse()

	c, err := client.NewMarketConn(&client.MarketParams{
		StreamParams: client.StreamParams{
			Host:      "localhost:8090",
			NoTLS:     true,
			Reconnect: true,
			Subscriptions: []string{
				"market:bitfinex:btcusd:orderbook:deltas",
			},
		},
	})
	if err != nil {
		log.Fatal("%s", err)
	}

	c.AddStateListener(client.StateAny, func(conn *client.StreamConn, oldState, state client.State) {
		log.Printf("State updated: %s -> %s", client.StateNames[oldState], client.StateNames[state])
	})

	c.AddMessageListener(func(conn *client.MarketConn, msg *ProtobufMarkets.MarketUpdateMessage) {
		fmt.Printf("read msg: %+v\n", *msg)
	})

	fmt.Println("conn...")
	c.Connect()

	for {
		time.Sleep(1 * time.Second)
	}
}
