package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/cryptowatch/proto/markets"
	"github.com/cryptowatch/stream-client-go"
	"github.com/golang/protobuf/proto"
)

const (
	defaultURL = "wss://sb.cryptowat.ch"
)

var (
	subs stringSlice

	detailed = flag.Bool("detailed", false, "Print detailed contents of received messages")
)

func main() {
	flag.Var(&subs, "sub", "Subscription key. This flag can be given multiple times")
	flag.Parse()

	// Setup OS signal handler
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// Get address to connect to
	u := defaultURL

	args := flag.Args()
	if len(args) >= 1 {
		u = args[0]
	}

	// Setup market connection (but don't connect just yet)
	c, err := streamclient.NewMarketConn(&streamclient.MarketParams{
		StreamParams: streamclient.StreamParams{
			URL: u,

			Reconnect:        true,
			ReconnectTimeout: 1 * time.Second,
			Subscriptions:    subs,
		},
	})
	if err != nil {
		log.Fatal("%s", err)
	}

	// Will print state changes to the user
	c.AddStateListener(
		streamclient.StateAny,
		func(conn *streamclient.StreamConn, oldState, state streamclient.State, cause error) {
			fmt.Printf("State updated: %s -> %s", streamclient.StateNames[oldState], streamclient.StateNames[state])
			if cause != nil {
				fmt.Printf(" (%s)", cause)
			}
			fmt.Printf("\n")
		},
	)

	// Will print received market update messages
	c.AddMessageListener(func(conn *streamclient.MarketConn, msg *ProtobufMarkets.MarketUpdateMessage) {
		str := ""
		if *detailed {
			str = proto.MarshalTextString(msg)
		} else {
			str = proto.CompactTextString(msg)
		}

		fmt.Printf("Received message: %s\n\n", str)
	})

	// Start connection loop
	fmt.Printf("Connecting to %s ...\n", u)
	c.Connect()

	// Wait until the OS signal is received, at which point we'll close the
	// connection and quit
	<-interrupt
	fmt.Printf("Closing connection...\n")

	if err := c.Close(); err != nil {
		fmt.Printf("Failed to close connection: %s", err)
	}
}
