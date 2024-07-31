# pgln - PostgreSQL Listen/Notify Library

A robust PostgreSQL Listen/Notify library built on top of [pgx](https://github.com/jackc/pgx).

## Motivation

PostgreSQL's Listen/Notify feature provides a basic pub/sub mechanism. One common use case is simple cache synchronization for downstream services without the need for additional services like Redis or RabbitMQ.

However, when a connection disconnects, notifications are lost as PostgreSQL doesn't store them. This library addresses this issue by providing an out-of-sync BLOCKING callback to rebuild your state while maintaining an active listening connection, ensuring no new notifications are missed.

## Use Case

For a detailed explanation of use cases and implementation details, please read our article on LinkedIn: [PGLN: PostgreSQL Listen/Notify](https://www.linkedin.com/pulse/pgln-postgresql-listennotify-tzahi-fadida--q0nwf)
Please note the examples are more current in this repository, as the code continues to improve.

## Installation

```
go get github.com/tzahifadida/pgln
```

## Features

- Supports any connection string compatible with pgxpool
- Custom pgxpool configuration
- Automatic reconnection
- Single connection for multiple Listen channels (Notify operations acquire, use, and release an additional connection)
- Out-of-sync callback for reconnects, allowing cache rebuilding without losing notifications
- Support for `LISTEN` and `NOTIFY` operations

## Example Usage

```go
package main

import (
    "context"
    "fmt"
    "github.com/tzahifadida/pgln"
    "os"
    "strings"
    "time"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()

    builder := pgln.NewPGListenNotifyBuilder().
        SetContext(ctx).
        SetReconnectInterval(5 * time.Second).
        UseConnectionString(os.Getenv("PGLN_CONNECTION_STRING"))

    r, err := builder.Build()
    if err != nil {
        fmt.Printf("Build error: %v\n", err)
        return
    }
	err = r.Start()
	require.NoError(t, err)
	if err != nil {
		fmt.Printf("Start error: %v\n", err)
		return
	}
	
    defer r.Shutdown()

	// This blocks until listening started. You can opt for Listen() for non-blocking...
	// Look inside ListenAndWaitForListening for usage...
    err = r.ListenAndWaitForListening("pgln_foo", pgln.ListenOptions{
        NotificationCallback: func(channel string, payload string) {
            fmt.Printf("Notification: %s - %s\n", channel, payload)
            cancel()
        },
        DoneCallback: func(channel string) {
            fmt.Printf("Done: %s\n", channel)
        },
        ErrorCallback: func(channel string, err error) {
            if !strings.Contains(err.Error(), "context canceled") {
                fmt.Printf("Error: %s - %s\n", channel, err)
                cancel()
            }
        },
        OutOfSyncBlockingCallback: func(channel string) error {
            fmt.Printf("Out-of-sync: %s\n", channel)
            err = r.Notify("pgln_foo", "working fine")
            if err != nil {
                cancel()
            }
            return nil
        },
    })
    if err != nil {
        fmt.Printf("Listen error: %v\n", err)
        return
    }

    <-ctx.Done()
}
```

## Testing

Run tests using the `go test` command.

For more detailed examples, refer to `builder_test.go` in the repository.

## Status and Support

This library uses pgxpool as the underlying driver. For issues related to the driver itself, please contact the pgx maintainers.

For questions or issues specific to pgln that are not related to the pgxpool driver, feel free to open an issue in this repository.

Community contributions and help with reported issues are welcome and encouraged.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

We hope you find pgln useful for your PostgreSQL Listen/Notify needs. If you have any questions, suggestions, or encounter any issues, please don't hesitate to open an issue or contribute to the project.