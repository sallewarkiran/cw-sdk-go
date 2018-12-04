package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"code.cryptowat.ch/ws-client-go"
	"github.com/golang/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"
)

var (
	subs stringSlice

	verbose = flag.Bool("verbose", false, "Prints all debug messages to stdout.")
	format  = flag.String("format", "json", "Data output format")

	credsFilename = flag.String("creds", "", "JSON file with credentials: the file must contain an object with two properties: \"api_key\" and \"secret_key\".")

	apiKey    = flag.String("apikey", "", "API key to use. Consider using -creds instead.")
	secretKey = flag.String("secretkey", "", "Secret key to use. Consider using -creds instead.")
)

func init() {
	flag.Var(&subs, "sub", "Subscription key. This flag can be given multiple times")
	flag.Parse()

	if *format != "json" {
		log.Fatalf("Invalid data format '%v'", *format)
	}
}

func main() {
	// Setup OS signal handler
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	args := flag.Args()

	// Get address to connect to
	u := ""
	if len(args) >= 1 {
		u = args[0]
	}

	// If --creds was given, use it; otherwise use --api-key and --secret-key.
	var cr *creds
	if *credsFilename != "" {
		var err error
		cr, err = parseCreds(*credsFilename)
		if err != nil {
			panic(err)
		}
	} else {
		cr = &creds{
			APIKey:    *apiKey,
			SecretKey: *secretKey,
		}
	}

	// Setup market connection (but don't connect just yet)
	c, err := wsclient.NewStreamClient(&wsclient.WSParams{
		URL: u,

		Subscriptions: subs,

		APIKey:    cr.APIKey,
		SecretKey: cr.SecretKey,
	})

	if err != nil {
		log.Fatalf("%s", err)
	}

	// Will print state changes to the user
	if *verbose {
		c.AddStateListener(
			wsclient.ConnStateAny,
			func(oldState, state wsclient.ConnState, cause error) {
				fmt.Printf("State updated: %s -> %s", wsclient.ConnStateNames[oldState], wsclient.ConnStateNames[state])
				if cause != nil {
					fmt.Printf(" (%s)", cause)
				}
				fmt.Printf("\n")
			},
		)
	}

	// Will print received market update messages
	// TODO: implement generic listeners to avoid subscribing separately to each
	// kind of message.
	c.OnOrderBookDeltaUpdate(func(market wsclient.Market, update wsclient.OrderBookDeltaUpdate) {
		fmt.Printf("Delta update on market %s: %+v\n", market.ID, update)
	})

	c.OnPairPerformanceUpdate(func(pair wsclient.Pair, update wsclient.PerformanceUpdate) {
		fmt.Printf("Pair performance update on pair %s: %+v\n", pair.ID, update)
	})

	// Start connection loop
	if *verbose {
		fmt.Printf("Connecting to %s ...\n", c.URL())
	}
	c.Connect()

	// Wait until the OS signal is received, at which point we'll close the
	// connection and quit
	<-interrupt
	fmt.Printf("Closing connection...\n")

	if err := c.Close(); err != nil {
		fmt.Printf("Failed to close connection: %s", err)
	}
}

// Output a protobuf message as a JSON string to stdout
func outputProtoJSON(msg interface{}) {
	m := &jsonpb.Marshaler{EmitDefaults: true}
	if pMsg, ok := msg.(proto.Message); ok {
		str, err := m.MarshalToString(pMsg)
		if err != nil {
			panic(err)
		}
		fmt.Println(str)
	} else {
		fmt.Println(msg)
		panic("Error: bad data received")
	}
}
