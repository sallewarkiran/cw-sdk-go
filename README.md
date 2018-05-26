# Stream Client in Golang

This library provides a Golang API to talk to the Cryptowatch Websocket
Streaming API. This product is in alpha.

The Cryptowatch Websocket Streaming API is not public like our [REST API](https://cryptowat.ch/docs/api).
Please [click here](https://docs.google.com/forms/d/e/1FAIpQLSdhv_ceVtKA0qQcW6zQzBniRBaZ_cC4al31lDCeZirntkmWQw/viewform?c=0&w=1)
to inquire about getting access to it.

## Using the library

The typical workflow is to create an instance of the connection, setup some
state- and message-listeners, and then kick off the connection. As events
happen, registered listeners will be called (see the note below on
concurrency).


### Code Sample

```golang
// Create a new stream connection instance. Note that the actual connection
// will happen later.
c, err := streamclient.NewStreamConn(&streamclient.StreamParams{
	URL: "wss://sb.cryptowat.ch",

	Reconnect:        true,
	ReconnectTimeout: 1 * time.Second,
	Backoff:          true,
	Subscriptions:    []string{
		"market:86:trades", // Trade feed for Kraken BTCEUR
		"market:87:trades", // Trade feed for Kraken BTCUSD
	},

	APIKey:    "myapikey",
	SecretKey: "mysecretkey",
})
if err != nil {
	log.Fatal("%s", err)
}

// Ask for the state transition updates, and present them to the user somehow
c.AddStateListener(
	streamclient.StateAny,
	func(conn *streamclient.StreamConn, oldState, state streamclient.State, cause error) {
		fmt.Printf(
		  "State updated: %s -> %s",
		  streamclient.StateNames[oldState],
		  streamclient.StateNames[state],
		)
		// If there is a non-nil cause, print it as well
		if cause != nil {
			fmt.Printf(" (%s)", cause)
		}
		fmt.Printf("\n")
	},
)

// Listen for received market messages and print them
c.AddMarketListener(
	func(conn *streamclient.StreamConn, msg *ProtobufMarkets.MarketUpdateMessage) {
		fmt.Printf("Message received: %s\n\n", proto.CompactTextString(msg))
	},
)

// Finally, connect.
c.Connect()

// NOTE: by the time Connect() returns, the connection is not yet
// established. Connect() merely starts the connection loop (which will
// handle connection, receiving data, and reconnection if requested), and
// returns.

// Wait for some external event, such as OS signal.
WaitForSomething()

// Close the connection. It will cause the reconnection to stop, and
// currently active websocket connection (if any) to close with the status
// 1000 (normal closure).
if err := c.Close(); err != nil {
	log.Fatal("Failed to close the connection: %s", err)
}
```

### Concurrency

All methods of the `StreamConn` and wrappers can be called concurrently from
any number of goroutines.

State listeners (registered with `AddStateListener` and `AddStateListenerOpt`)
and message listeners (registered with `AddMarketListener`) are called by the
same internal goroutine (unique to each connection); that is, they are never
called concurrently with each other.

## Command line tool

Use the command line tool `stream-client` to subscribe to live data feeds from
the command line. To install the tool, run `make` from the root of the repo.
This will create the executable `bin/stream-client`, which can be used as
follows:

```bash
./stream-client \
    -apikey=your_api_key \
    -secretkey=your_secret_key \
    -sub subscription_key \
    -sub subscription_key2
```

