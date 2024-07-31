# pgln - PostgreSQL Listen/Notify Library

A robust PostgreSQL Listen/Notify library built on top of [pgx](https://github.com/jackc/pgx).

⭐️ Please Star This Project

If you find this project useful, please consider giving it a star ⭐️ on GitHub. It helps others find the project and shows your support!

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
- Transaction-safe notify operations with `NotifyQuery`

## Major Methods and Usage

### NewPGListenNotifyBuilder()

Creates a new builder for configuring the PGListenNotify instance.

```go
builder := pgln.NewPGListenNotifyBuilder()
```

### Builder Methods

- `SetContext(ctx context.Context)`: Sets the context for the PGListenNotify instance.
- `UseConnectionString(connectionString string)`: Sets the PostgreSQL connection string.
- `SetPool(pool *pgxpool.Pool)`: Sets a custom connection pool.
- `SetReconnectInterval(reconnectInterval time.Duration)`: Sets the interval for reconnection attempts.
- `SetConnectTimeout(timeout time.Duration)`: Sets the timeout for connection attempts.
- `SetHealthCheckTimeout(timeout time.Duration)`: Sets the timeout for health checks.
- `Build()`: Builds and returns the PGListenNotify instance.

### PGListenNotify Methods

- `Start()`: Starts the listening process. Must be called before any Listen operations.
- `Shutdown()`: Gracefully shuts down the PGListenNotify instance.
- `Listen(channel string, options ListenOptions)`: Starts listening on a channel (non-blocking).
- `ListenAndWaitForListening(channel string, options ListenOptions)`: Starts listening on a channel and waits for it to be ready (blocking).
- `UnListen(channel string)`: Stops listening on a channel.
- `Notify(channel string, payload string)`: Sends a notification to a channel.
- `NotifyQuery(channel string, payload string)`: Returns a query and parameters for sending a notification within a transaction.

## Example Usage

This example demonstrates how to use the pgln library, including the use of `NotifyQuery` for transaction-safe notifications.

```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/tzahifadida/pgln"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
	"os"
	"strings"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	connectionString := os.Getenv("PGLN_CONNECTION_STRING")

	builder := pgln.NewPGListenNotifyBuilder().
		SetContext(ctx).
		SetReconnectInterval(5 * time.Second).
		SetConnectTimeout(10 * time.Second).
		SetHealthCheckTimeout(2 * time.Second).
		UseConnectionString(connectionString)

	r, err := builder.Build()
	if err != nil {
		fmt.Printf("Build error: %v\n", err)
		return
	}
	err = r.Start()
	if err != nil {
		fmt.Printf("Start error: %v\n", err)
		return
	}

	defer r.Shutdown()

	notificationReceived := make(chan string, 1)

	err = r.ListenAndWaitForListening("pgln_foo", pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			fmt.Printf("Notification received: %s - %s\n", channel, payload)
			select {
			case notificationReceived <- payload:
			case <-ctx.Done():
			}
		},
		ErrorCallback: func(channel string, err error) {
			if !strings.Contains(err.Error(), "context canceled") {
				fmt.Printf("Error: %s - %s\n", channel, err)
			}
		},
	})
	if err != nil {
		fmt.Printf("Listen error: %v\n", err)
		return
	}

	// Open a database connection
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		return
	}
	defer db.Close()

	// Start a transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Printf("Failed to begin transaction: %v\n", err)
		return
	}

	// Use NotifyQuery to get the notification query
	notifyQuery := r.NotifyQuery("pgln_foo", "Transaction notification")

	// Execute the notification query within the transaction
	// This is crucial: the notification will only be sent if the transaction is committed
	_, err = tx.ExecContext(ctx, notifyQuery.Query, notifyQuery.Params...)
	if err != nil {
		fmt.Printf("Failed to execute notify query: %v\n", err)
		tx.Rollback()
		return
	}

	// Commit the transaction
	// The notification will be sent only after this commit succeeds
	err = tx.Commit()
	if err != nil {
		fmt.Printf("Failed to commit transaction: %v\n", err)
		return
	}

	// Wait for the notification or timeout
	select {
	case payload := <-notificationReceived:
		fmt.Printf("Received notification payload: %s\n", payload)
	case <-time.After(5 * time.Second):
		fmt.Println("Timed out waiting for notification")
	case <-ctx.Done():
		fmt.Println("Context cancelled")
	}

	// We don't need to explicitly close the channel or wait for ctx.Done() here
}
```

### Important Note on NotifyQuery

The `NotifyQuery` method is particularly useful when you need to ensure that a notification is sent only if a transaction is successfully committed. This is because PostgreSQL executes `NOTIFY` commands at commit time, not at the time they are issued within a transaction.

By using `NotifyQuery`, you can:
1. Include the notification as part of a larger transaction.
2. Ensure that the notification is sent only if all other operations in the transaction succeed.
3. Avoid sending notifications for operations that may be rolled back.

This makes `NotifyQuery` ideal for scenarios where you want to notify other parts of your system about changes, but only if those changes are successfully persisted to the database.

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