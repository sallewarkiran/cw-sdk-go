# Stream Client in Golang

This library provides a Golang API to talk to Cryptowatch Stream WebSocket
API.

## Overview

There are two layers:

  - Stream connection itself: provides connection and automatic reconnection,
    allows to listen for state transitions (disconnected, connecting, etc), and
    to subscribe to and unsubscribe from topics. It doesn't interpret received
    data in any way (other than the keepalive "heartbeat" messages);
  - Format-aware wrapper connections: e.g. `MarketConn` is able to decode
    Market Update messages: order book updates, trade updates, etc; and it
    provides an API for the clients to subscribe to parsed messages.

One possible workflow is as follows:

```golang

  // Create a format-aware connection instance (in this case, MarketConn).
  // Note that the actual connection will happen later.
	c, err := streamclient.NewMarketConn(&streamclient.MarketParams{
		StreamParams: streamclient.StreamParams{
			URL: "wss://sb.cryptowat.ch",

			Reconnect:        true,
			ReconnectTimeout: 1 * time.Second,
			Subscriptions:    []string{
				"market:bitfinex:btcusd:orderbook:deltas",
				"market:bitfinex:btceur:orderbook:deltas",
			},
		},
	})
	if err != nil {
		log.Fatal("%s", err)
	}

	// Ask for the state transition updates, and present them to the user somehow
	c.AddStateListener(
		streamclient.StateAny,
		func(conn *streamclient.StreamConn, oldState, state streamclient.State, cause error) {
			fmt.Printf("State updated: %s -> %s", streamclient.StateNames[oldState], streamclient.StateNames[state])
			// If there is a non-nil cause, print it as well
			if cause != nil {
				fmt.Printf(" (%s)", cause)
			}
			fmt.Printf("\n")
		},
	)

	// Listen for received parsed market messages and print them
	c.AddMessageListener(func(conn *streamclient.MarketConn, msg *ProtobufMarkets.MarketUpdateMessage) {
		fmt.Printf("Message received: %s\n\n", proto.CompactTextString(msg))
	})

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

Note that from the above, only `AddMessageListener` is provided by (and is
specific to) the wrapper `MarketConn`; everything else is provided by the
generic Stream connection.

Other types of wrapper connections (say, `BrokerConn`) will have
`AddMessageListener` which would call the callback accepting different type of
message, and that's the only difference.

The above design means that for different kinds of subscriptions (e.g. market
updates and broker requests) one would have to create two wrapper connections,
each wrapping a separate Stream connection. Currently there's no way to share a
single Stream connection between wrappers.

## Concurrency

All methods of the `StreamConn` and wrappers can be called concurrently from
any number of goroutines.

State listeners (registered with `AddStateListener` and `AddStateListenerOpt`)
are called by the same internal goroutine (unique to each connection); that is,
they are never called concurrently with each other.

Message listeners (registered with `AddMessageListener`) are called from
another internal goroutine. So, message listeners might be called concurrently
with state listeners.
