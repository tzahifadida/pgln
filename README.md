# pgln - PostgreSQL Listen/Notify Library

A robust PostgreSQL Listen/Notify library built on top of [pgx](https://github.com/jackc/pgx).

⭐️ Please Star This Project

If you find this project useful, please consider giving it a star ⭐️ on GitHub. It helps others find the project and shows your support!

## Table of Contents
- [Motivation](#motivation)
- [Use Case](#use-case)
- [Installation](#installation)
- [Features](#features)
- [Important Note on Callbacks](#important-note-on-callbacks)
- [Major Methods and Usage](#major-methods-and-usage)
    - [NewPGListenNotifyBuilder()](#newpglistennotifybuilder)
    - [Builder Methods](#builder-methods)
    - [PGListenNotify Methods](#pglistennotify-methods)
- [Example Usage](#example-usage)
    - [Important Note on NotifyQuery](#important-note-on-notifyquery)
- [Testing](#testing)
- [Status and Support](#status-and-support)
- [Contributing](#contributing)
- [License](#license)

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

- Supports `*sql.DB` connections using the pgx driver in stdlib mode
- Automatic reconnection
- Single connection for multiple Listen channels (Notify operations acquire, use, and release an additional connection)
- Out-of-sync callback for reconnects, allowing cache rebuilding without losing notifications
- Support for `LISTEN` and `NOTIFY` operations
- Transaction-safe notify operations with `NotifyQuery`
- Safe unlisten operations with completion signaling
- Blocking callbacks, giving users full control over concurrency management

## Important Note on Callbacks

All callbacks provided to this library (NotificationCallback, DoneCallback, ErrorCallback, and OutOfSyncBlockingCallback) are **BLOCKING**. This means that when a callback is invoked, it will block the library's internal operations until the callback completes.

It is the responsibility of the library user to decide whether to perform operations synchronously within the callback or to use goroutines for concurrent execution. If you need to perform long-running or potentially blocking operations in a callback, consider wrapping the operation in a goroutine to avoid blocking the library's internal processes.

Example of non-blocking callback usage:

```go
NotificationCallback: func(channel string, payload string) {
go func() {
// Perform potentially long-running operations here
processNotification(channel, payload)
}()
},
```

By making callbacks blocking, this library provides you with full control over concurrency management and the ability to ensure operations are completed before proceeding, if necessary.

## Major Methods and Usage

### NewPGListenNotifyBuilder()

Creates a new builder for configuring the PGListenNotify instance.

```go
builder := pgln.NewPGListenNotifyBuilder()
```

### Builder Methods

- `SetContext(ctx context.Context)`: Sets the context for the PGListenNotify instance.
- `SetDB(db *sql.DB)`: Sets the database connection (must be a *sql.DB using pgx driver).
- `SetReconnectInterval(reconnectInterval time.Duration)`: Sets the interval for reconnection attempts.
- `SetHealthCheckTimeout(timeout time.Duration)`: Sets the timeout for health checks.
- `Build()`: Builds and returns the PGListenNotify instance.

### PGListenNotify Methods

- `Start()`: Starts the listening process. Must be called before any Listen operations.
- `Shutdown()`: Gracefully shuts down the PGListenNotify instance.
- `Listen(channel string, options ListenOptions) (chan error, error)`: Starts listening on a channel (non-blocking).
- `ListenAndWaitForListening(channel string, options ListenOptions) error`: Starts listening on a channel and waits for it to be ready (blocking).
- `UnListen(channel string) (chan struct{}, error)`: Stops listening on a channel and returns a channel that will be closed when the unlisten operation is complete.
- `UnlistenAndWaitForUnlistening(channel string) error`: Stops listening on a channel and waits until it's completely removed (blocking).
- `Notify(channel string, payload string) error`: Sends a notification to a channel.
- `NotifyQuery(channel string, payload string) NotifyQueryResult`: Returns a query and parameters for sending a notification within a transaction.

## Example Usage

This example demonstrates how to use the pgln library, including the use of `NotifyQuery` for transaction-safe notifications and proper error handling. It also shows how to use callbacks safely.

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

	// Open a database connection using pgx driver
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		return
	}
	defer db.Close()

	builder := pgln.NewPGListenNotifyBuilder().
		SetContext(ctx).
		SetReconnectInterval(5 * time.Second).
		SetHealthCheckTimeout(2 * time.Second).
		SetDB(db)

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

	defer func() {
		_, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		err := r.UnlistenAndWaitForUnlistening("pgln_foo")
		if err != nil {
			if err == context.DeadlineExceeded {
				fmt.Println("UnListen timed out")
			} else if err != context.Canceled {
				fmt.Printf("UnListen error: %v\n", err)
			}
		}
		r.Shutdown()
	}()

	notificationReceived := make(chan string, 1)

	err = r.ListenAndWaitForListening("pgln_foo", pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			// This callback is blocking. For long-running operations, consider using a goroutine:
			go func() {
				fmt.Printf("Notification received: %s - %s\n", channel, payload)
				select {
				case notificationReceived <- payload:
				default:
					fmt.Println("Notification channel full, discarding payload")
				}
			}()
		},
		ErrorCallback: func(channel string, err error) {
			if !strings.Contains(err.Error(), "context canceled") {
				fmt.Printf("Error: %s - %s\n", channel, err)
			}
		},
		OutOfSyncBlockingCallback: func(channel string) error {
			// This callback is intentionally blocking to ensure sync before proceeding
			fmt.Printf("Out-of-sync: %s\n", channel)
			return nil
		},
	})
	if err != nil {
		fmt.Printf("Listen error: %v\n", err)
		return
	}

	// Start a transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Printf("Failed to begin transaction: %v\n", err)
		return
	}
	defer tx.Rollback() // Rollback if not committed

	// Use NotifyQuery to get the notification query
	notifyQuery := r.NotifyQuery("pgln_foo", "Transaction notification")

	// Execute the notification query within the transaction
	_, err = tx.ExecContext(ctx, notifyQuery.Query, notifyQuery.Params...)
	if err != nil {
		fmt.Printf("Failed to execute notify query: %v\n", err)
		return
	}

	// Commit the transaction
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

This library uses the pgx driver in stdlib mode. For issues related to the driver itself, please contact the pgx maintainers.

For questions or issues specific to pgln that are not related to the pgx driver, feel free to open an issue in this repository.

Community contributions and help with reported issues are welcome and encouraged.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

We hope you find pgln useful for your PostgreSQL Listen/Notify needs. If you have any questions, suggestions, or encounter any issues, please don't hesitate to open an issue or contribute to the project.