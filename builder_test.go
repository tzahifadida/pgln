package pgln_test

import (
	"context"
	"database/sql"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/tzahifadida/pgln"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var sharedConnectionString string

func TestMain(m *testing.M) {
	// Set up the shared test container
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "postgres:13",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "testdb",
			"POSTGRES_USER":     "testuser",
			"POSTGRES_PASSWORD": "testpass",
		},
		Cmd: []string{
			"postgres",
			"-c", "max_connections=200",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").WithStartupTimeout(time.Minute),
	}

	postgresC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		fmt.Printf("Failed to start container: %s", err)
		os.Exit(1)
	}

	// Clean up the container after all tests are done
	defer func() {
		if err := postgresC.Terminate(ctx); err != nil {
			fmt.Printf("Failed to terminate container: %s", err)
		}
	}()

	port, err := postgresC.MappedPort(ctx, "5432")
	if err != nil {
		fmt.Printf("Failed to get container port: %s", err)
		os.Exit(1)
	}

	sharedConnectionString = fmt.Sprintf("postgres://testuser:testpass@localhost:%s/testdb", port.Port())

	// Implement retry mechanism with exponential backoff
	maxRetries := 5
	var db *sql.DB
	for i := 0; i < maxRetries; i++ {
		db, err = sql.Open("pgx", sharedConnectionString)
		if err == nil {
			// Ensure connection is working
			err = db.Ping()
			if err == nil {
				break
			}
		}
		if i < maxRetries-1 { // Don't sleep on the last attempt
			sleepDuration := time.Duration(1<<uint(i)) * time.Second
			fmt.Printf("Failed to connect to database, retrying in %v...\n", sleepDuration)
			time.Sleep(sleepDuration)
		}
	}
	if err != nil {
		fmt.Printf("Failed to connect to database after multiple retries: %s", err)
		os.Exit(1)
	}
	defer db.Close()

	// Run the tests
	exitCode := m.Run()

	// Exit with the same code as the tests
	os.Exit(exitCode)
}

// Use this function in your tests to get a new database connection
func getTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", sharedConnectionString)
	db.SetMaxOpenConns(200)
	require.NoError(t, err)
	return db
}

func TestPGListenNotify(t *testing.T) {
	t.Run("Basic Listen and Notify", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		db := getTestDB(t)
		defer db.Close()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.SetDB(db).
			SetContext(ctx).
			SetHealthCheckTimeout(2 * time.Second).
			Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		testChannel := "test_channel"
		notificationReceived := make(chan string, 1)

		err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
			NotificationCallback: func(channel string, payload string) {
				select {
				case notificationReceived <- payload:
				default:
					t.Log("Notification channel full, discarding payload")
				}
			},
		})
		require.NoError(t, err)

		testPayload := "test_payload"
		err = ln.Notify(testChannel, testPayload)
		require.NoError(t, err)

		select {
		case received := <-notificationReceived:
			assert.Equal(t, testPayload, received)
		case <-ctx.Done():
			t.Fatal("Timed out waiting for notification")
		}
	})

	t.Run("Multiple Channels", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		db := getTestDB(t)
		defer db.Close()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.SetDB(db).SetContext(ctx).Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		channels := []string{"channel1", "channel2", "channel3"}
		receivedNotifications := make(map[string]string)
		var mu sync.Mutex
		wg := sync.WaitGroup{}
		wg.Add(len(channels))

		for _, channel := range channels {
			err := ln.ListenAndWaitForListening(channel, pgln.ListenOptions{
				NotificationCallback: func(channel string, payload string) {
					mu.Lock()
					receivedNotifications[channel] = payload
					mu.Unlock()
					wg.Done()
				},
			})
			require.NoError(t, err)
		}

		for _, channel := range channels {
			err = ln.Notify(channel, fmt.Sprintf("payload_%s", channel))
			require.NoError(t, err)
		}

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			mu.Lock()
			defer mu.Unlock()
			for _, channel := range channels {
				assert.Equal(t, fmt.Sprintf("payload_%s", channel), receivedNotifications[channel])
			}
		case <-ctx.Done():
			t.Fatal("Timed out waiting for notifications")
		}
	})

	t.Run("UnlistenAndWaitForUnlistening", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		db := getTestDB(t)
		defer db.Close()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.SetDB(db).SetContext(ctx).Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		testChannel := "unlisten_wait_channel"
		notificationReceived := make(chan string, 1)

		err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
			NotificationCallback: func(channel string, payload string) {
				select {
				case notificationReceived <- payload:
				default:
					t.Log("Notification channel full, discarding payload")
				}
			},
		})
		require.NoError(t, err)

		err = ln.UnlistenAndWaitForUnlistening(testChannel)
		require.NoError(t, err)

		err = ln.Notify(testChannel, "should_not_receive")
		require.NoError(t, err)

		select {
		case <-notificationReceived:
			t.Fatal("Received notification after UnlistenAndWaitForUnlistening")
		case <-time.After(2 * time.Second):
			// Test passed, no notification received
		}
	})

	t.Run("Concurrent Listen and Unlisten", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		db := getTestDB(t)
		defer db.Close()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.SetDB(db).SetContext(ctx).Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		channels := make([]string, 100)
		for i := range channels {
			channels[i] = fmt.Sprintf("concurrent_channel_%d", i)
		}

		var wg sync.WaitGroup
		wg.Add(len(channels) * 2) // For both Listen and Unlisten operations

		for _, channel := range channels {
			go func(ch string) {
				defer wg.Done()
				err := ln.ListenAndWaitForListening(ch, pgln.ListenOptions{
					NotificationCallback: func(channel, payload string) {},
				})
				assert.NoError(t, err)
			}(channel)
		}

		// Wait a bit before starting Unlisten operations
		time.Sleep(1 * time.Second)

		for _, channel := range channels {
			go func(ch string) {
				defer wg.Done()
				err := ln.UnlistenAndWaitForUnlistening(ch)
				assert.NoError(t, err)
			}(channel)
		}

		wg.Wait()
	})
}

func TestResilienceToConnectionDrops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := getTestDB(t)
	defer db.Close()

	builder := pgln.NewPGListenNotifyBuilder()
	ln, err := builder.SetDB(db).
		SetContext(ctx).
		SetReconnectInterval(1 * time.Second).
		Build()
	require.NoError(t, err)
	err = ln.Start()
	require.NoError(t, err)
	defer ln.Shutdown()

	testChannel := "resilience_channel"
	notificationsReceived := make(chan string, 10)
	reconnected := make(chan struct{})

	err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			select {
			case notificationsReceived <- payload:
			default:
				t.Log("Notification channel full, discarding payload")
			}
		},
		OutOfSyncBlockingCallback: func(channel string) error {
			close(reconnected)
			return nil
		},
	})
	require.NoError(t, err)
	// Send a notification
	err = ln.Notify(testChannel, "before_disconnect")
	require.NoError(t, err)

	// Wait for the notification
	select {
	case received := <-notificationsReceived:
		assert.Equal(t, "before_disconnect", received)
	case <-ctx.Done():
		t.Fatal("Timed out waiting for first notification")
	}

	// Simulate a connection drop by forcibly closing all connections
	err = dropAllConnections(sharedConnectionString)
	require.NoError(t, err)

	// Wait for reconnection
	select {
	case <-reconnected:
		// Reconnected successfully
	case <-time.After(10 * time.Second):
		t.Fatal("Timed out waiting for reconnection")
	}

	time.Sleep(4 * time.Second)
	// Send another notification
	err = ln.Notify(testChannel, "after_reconnect")
	require.NoError(t, err)

	// Wait for the notification
	select {
	case received := <-notificationsReceived:
		assert.Equal(t, "after_reconnect", received)
	case <-ctx.Done():
		t.Fatal("Timed out waiting for second notification")
	}
}

func TestNotifyQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := getTestDB(t)
	defer db.Close()

	builder := pgln.NewPGListenNotifyBuilder()
	ln, err := builder.SetDB(db).SetContext(ctx).Build()
	require.NoError(t, err)
	err = ln.Start()
	require.NoError(t, err)
	defer ln.Shutdown()

	testChannel := "notify_query_channel"
	testPayload := "test_payload"

	result := ln.NotifyQuery(testChannel, testPayload)
	assert.Equal(t, "SELECT pg_notify($1, $2)", result.Query)
	assert.Equal(t, []any{testChannel, testPayload}, result.Params)

	notificationReceived := make(chan string, 1)
	err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			select {
			case notificationReceived <- payload:
			default:
				t.Log("Notification channel full, discarding payload")
			}
		},
	})
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, result.Query, result.Params[0], result.Params[1])
	require.NoError(t, err)

	select {
	case received := <-notificationReceived:
		assert.Equal(t, testPayload, received)
	case <-ctx.Done():
		t.Fatal("Timed out waiting for notification")
	}
}

func TestReadmeExample(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	connectionString := sharedConnectionString

	// Open a database connection using pgx driver
	db, err := sql.Open("pgx", connectionString)
	require.NoError(t, err, "Failed to open database")
	defer db.Close()

	builder := pgln.NewPGListenNotifyBuilder().
		SetContext(ctx).
		SetReconnectInterval(5 * time.Second).
		SetHealthCheckTimeout(2 * time.Second).
		SetDB(db)

	r, err := builder.Build()
	require.NoError(t, err, "Build error")
	err = r.Start()
	require.NoError(t, err, "Start error")

	defer func() {
		_, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		err := r.UnlistenAndWaitForUnlistening("pgln_foo")
		if err != nil {
			if err == context.DeadlineExceeded {
				t.Log("UnListen timed out")
			} else if err != context.Canceled {
				t.Logf("UnListen error: %v\n", err)
			}
		}
		r.Shutdown()
	}()

	notificationReceived := make(chan string, 1)

	err = r.ListenAndWaitForListening("pgln_foo", pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			// This callback is blocking. For long-running operations, we use a goroutine:
			go func() {
				t.Logf("Notification received: %s - %s\n", channel, payload)
				select {
				case notificationReceived <- payload:
				default:
					t.Log("Notification channel full, discarding payload")
				}
			}()
		},
		ErrorCallback: func(channel string, err error) {
			if !strings.Contains(err.Error(), "context canceled") {
				t.Logf("Error: %s - %s\n", channel, err)
				cancel()
			}
		},
		OutOfSyncBlockingCallback: func(channel string) error {
			// This callback is intentionally blocking to ensure sync before proceeding
			t.Logf("Out-of-sync: %s\n", channel)
			return nil
		},
	})
	require.NoError(t, err, "Listen error")

	// Start a transaction
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err, "Failed to begin transaction")
	defer tx.Rollback() // Rollback if not committed

	// Use NotifyQuery to get the notification query
	notifyQuery := r.NotifyQuery("pgln_foo", "Transaction notification")

	// Execute the notification query within the transaction
	_, err = tx.ExecContext(ctx, notifyQuery.Query, notifyQuery.Params...)
	require.NoError(t, err, "Failed to execute notify query")

	// Commit the transaction
	err = tx.Commit()
	require.NoError(t, err, "Failed to commit transaction")

	// Wait for the notification or timeout
	select {
	case payload := <-notificationReceived:
		t.Logf("Received notification payload: %s\n", payload)
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for notification")
	case <-ctx.Done():
		t.Fatal("Context cancelled")
	}
}

// Helper function to drop all connections
func dropAllConnections(connectionString string) error {
	ctx := context.Background()
	db, err := sql.Open("pgx", connectionString)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		AND datname = current_database()
	`)
	if err != nil {
		return fmt.Errorf("failed to terminate connections: %w", err)
	}

	return nil
}

func TestDelayedNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db := getTestDB(t)
	defer db.Close()

	builder := pgln.NewPGListenNotifyBuilder()
	ln, err := builder.SetDB(db).SetContext(ctx).Build()
	require.NoError(t, err)
	err = ln.Start()
	require.NoError(t, err)
	defer ln.Shutdown()

	testChannel := "delayed_notification_channel"
	notificationReceived := make(chan string, 1)

	// Start listening
	err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			select {
			case notificationReceived <- payload:
			default:
				t.Log("Notification channel full, discarding payload")
			}
		},
	})
	require.NoError(t, err)

	// Record the start time
	startTime := time.Now()

	// Send a delayed notification
	go func() {
		time.Sleep(10 * time.Second)
		err := ln.Notify(testChannel, "delayed_payload")
		if err != nil {
			t.Errorf("Failed to send delayed notification: %v", err)
		}
	}()

	// Wait for the notification
	select {
	case received := <-notificationReceived:
		elapsedTime := time.Since(startTime)
		assert.Equal(t, "delayed_payload", received)
		assert.GreaterOrEqual(t, elapsedTime.Seconds(), 10.0, "Notification received too early")
		assert.Less(t, elapsedTime.Seconds(), 11.0, "Notification received too late")
	case <-ctx.Done():
		t.Fatal("Timed out waiting for delayed notification")
	}
}
