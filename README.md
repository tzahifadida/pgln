# pgln - A Postgresql Listen/Notify library that uses [pgx](https://github.com/jackc/pgx) as the underline driver

## Motivation

  Postgresql Listen/Notify is a kind of rudimentary pub/sub.
  One such usage is a simple cache synchronization to downstream services without adding more services like redis/rabbitmq.
  The problem is that when the connection disconnects you lose notifications because it does not store notifications.
  Therefor the technique we use is to rebuild the cache when it happens while holding the connection in listening mode so we do not lose any new notifications.
  The library has an out-of-sync BLOCKING callback to rebuild your state while not losing new notifications.

## Install

	go get github.com/tzahifadida/pgln

## Example

```
ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
defer cancel()
builder := pgln.NewPGListenNotifyBuilder().
    SetContext(ctx).
    SetReconnectInterval(5000).
    UseConnectionString(os.Getenv("PGLN_CONNECTION_STRING"))

r, err := builder.Build()
if err != nil {
    return
}
defer r.Close()
err = r.Listen("pgln_foo", pgln.ListenOptions{
    NotificationCallback: func(channel string, payload string) {
        fmt.Printf("notification: %s - %s\n", channel, payload)
        cancel()
    },
    DoneCallback: func(channel string) {
        fmt.Printf("done: %s\n", channel)
    },
    ErrorCallback: func(channel string, err error) {
        if !strings.Contains(err.Error(), "context canceled") {
            fmt.Printf("error: %s - %s\n", channel, err)
            cancel()
        }
    },
    OutOfSyncBlockingCallback: func(channel string) error {
        fmt.Printf("out-of-sync: %s\n", channel)
        err = r.Notify("pgln_foo", "working fine")
        if err != nil {
            cancel()
        }
        return nil
    },
})
if err != nil {
    return
}

select {
case <-ctx.Done():
    return
}
```
## Features

* Any connection string that pgxpool supports and you can set your custom pgxpool directly.
* Reconnects automatically
* Only 1 connection is held for all the Listen channels (notify acquires an additional connection, sends and releases)
* Have an out-of-sync callback for reconnects while holding a listener, so you can rebuild caches without losing notifications. 
* Notifications: `LISTEN`/`NOTIFY`

## Usage

See the builder_test.go for an example

## Tests

`go test` is used for testing. Please note that the connection string is provided as an environment variable: PGLN_CONNECTION_STRING

## Status

Because this library uses pgxpool as the underlying driver, you can contact pgx for any issue with the driver.
Feel free to contact for anything related to the pgln that is not related to the pgxpool driver.
Community members are encouraged to help each other with reported issues.