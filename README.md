# Stream Client in Golang

This library provides a Golang API to talk to the Cryptowatch Websocket
Streaming API. This product is in alpha.

The Cryptowatch Websocket Streaming API is not public like our [REST API](https://cryptowat.ch/docs/api).
Please [click here](https://docs.google.com/forms/d/e/1FAIpQLSdhv_ceVtKA0qQcW6zQzBniRBaZ_cC4al31lDCeZirntkmWQw/viewform?c=0&w=1)
to inquire about getting access to it.

## Install

```
go get code.cryptowat.ch/stream-client-go
```

## Usage
The typical workflow is to create an instance of the connection, setup listeners, then initiate the connection. As events happen (such as state changes or data received), registered listeners will be called (see the note below on concurrency). 

The following code connects to the stream api and listens on market data for `btc:usd` and `btc:eur`.

```golang
import streamclient "code.cryptowat.ch/stream-client-go"

// Create a new stream connection instance. Note that the actual connection
// will happen later.
c, err := streamclient.NewStreamConn(&streamclient.StreamParams{
	APIKey:    "myapikey",
	SecretKey: "mysecretkey",
	Subscriptions:    []string{
		"markets:86:trades", // Trade feed for Kraken BTCEUR
		"markets:87:trades", // Trade feed for Kraken BTCUSD
	},
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

## Settings
The following struct `StreamParams` is used as the settings parameter for `streamclient.NewStreamConn(params)`. The only required values are `APIKey` and `SecretKey`; all other settings have sensible defaults.

```golang
type StreamParams struct {
	// Required. APIKey and SecretKey are stream credentials
	APIKey    string
	SecretKey string

	// Stream url to connect to; default is wss://stream.cryptowat.ch
	URL string

	// Initial set of subscription keys. Client will automatically subscribe to
	// those every time it's connected or reconnected.
	Subscriptions []string

	// If not supplied, sensible defaults will be used.
	ReconnectOpts *ReconnectOpts
}

type ReconnectOpts struct {
	// Reconnect switch; if true, the client will attempt to reconnect to the
	// stream if it gets disconnected. Defaults to true.
	Reconnect bool
	// Reconnection backoff: if true, then the reconnection time will be
	// initially ReconnectTimeout, then will grow by 500ms on each unsuccessful
	// connection attempt; but it won't be longer than MaxReconnectTimeout.
	// Defaults to true.
	Backoff bool
	// Initial reconnection timeout: defaults to 0 seconds. If backoff=false,
	// a minimum reconnectTimeout of 1 second will be used. Defaults to 0s.
	ReconnectTimeout time.Duration
	// Max reconnect timeout. Defaults to 30s.
	MaxReconnectTimeout time.Duration
}
```

## Methods
The following methods are available on an instance of `StreamConn`.

#### Connect()
Initiates connection to the stream api.

#### Close()
Stops the current connection (or any attempts to reconnect).

#### Subscribe(keys []string)
Subscribes the client to the given keys.

#### Unsubscribe(keys []string)
Unsubscribes the client from the given keys.

#### AddStateListener(state State, cb StateCallback)
Registers a new listener for the given state. Listener is registered with the default options (zero values of all fields in StateListenerOpt). All registered callbacks for all states (and all messages, see AddMarketListener) will be called by the same internal goroutine, i.e. they are never called concurrently with each other.

#### AddStateListenerOpt(state State, cb StateCallback, opt StateListenerOpt)
AddStateListenerOpt is like AddStateListener, but also takes additional options `OneOff` and `CallImmediately` (see definition for details).

#### AddMarketListener(cb OnMarketUpdateCallback)
Registers a new listener for all received market update messages.

#### AddPairListener(cb OnPairUpdateCallback)
Registers a new listener for all received pair update messages.

#### State() State
Returns the current connection state.

#### URL() string
Returns the url used for connecting.

## States
The `streamclient` package exports the following states which can be used for listeners with `AddStateListener(state, cb)`.
```golang
const (
	// StateDisconnected means we're disconnected and not trying to connect.
	// connLoop is not running.
	StateDisconnected State = iota

	// StateWaitBeforeReconnect means we already tried to connect, but then
	// either the connection failed, or succeeded but later disconnected for some
	// reason (see stateCause), and now we're waiting for a timeout before
	// connecting again. wsConn is nil, but connCtx and connCtxCancel are not,
	// and connLoop is running.
	StateWaitBeforeReconnect

	// StateConnecting means we're calling websocket.DefaultDialer.Dial() right
	// now.
	StateConnecting

	// StateAuthenticating means the transport (websocket) connection is
	// established, and right now we're exchanging the authentication and
	// identification messages
	StateAuthenticating

	// StateEstablished means the connection is ready
	StateEstablished

	// StatesCnt is a number of states
	StatesCnt

	// StateAny can be used with AddStateListener() and AddStateListenerOpt()
	// in order to listen for all states.
	StateAny = -1
)
```

## Concurrency

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

## License
[BSD-2-Clause](LICENSE)
