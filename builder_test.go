package pgln_test

import (
	"context"
	"fmt"
	"github.com/tzahifadida/pgln"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupTestContainer(t *testing.T) (string, func()) {
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "postgres:13",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "testdb",
			"POSTGRES_USER":     "testuser",
			"POSTGRES_PASSWORD": "testpass",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").WithStartupTimeout(time.Minute),
	}

	postgresC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)

	port, err := postgresC.MappedPort(ctx, "5432")
	require.NoError(t, err)

	connectionString := fmt.Sprintf("postgres://testuser:testpass@localhost:%s/testdb", port.Port())

	// Implement retry mechanism with exponential backoff
	maxRetries := 5
	var pool *pgxpool.Pool
	for i := 0; i < maxRetries; i++ {
		pool, err = pgxpool.New(ctx, connectionString)
		if err == nil {
			// Ensure connection is working
			err = pool.Ping(ctx)
			if err == nil {
				break
			}
		}
		if i < maxRetries-1 { // Don't sleep on the last attempt
			sleepDuration := time.Duration(1<<uint(i)) * time.Second
			t.Logf("Failed to connect to database, retrying in %v...", sleepDuration)
			time.Sleep(sleepDuration)
		}
	}
	require.NoError(t, err, "Failed to connect to database after multiple retries")

	return connectionString, func() {
		pool.Close()
		assert.NoError(t, postgresC.Terminate(ctx))
	}
}

func TestPGListenNotify(t *testing.T) {
	connectionString, cleanup := setupTestContainer(t)
	defer cleanup()

	t.Run("Basic Listen and Notify", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.UseConnectionString(connectionString).
			SetContext(ctx).
			SetConnectTimeout(5 * time.Second).
			SetHealthCheckTimeout(2 * time.Second).
			Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		testChannel := "test_channel"
		notificationReceived := make(chan string)

		err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
			NotificationCallback: func(channel string, payload string) {
				notificationReceived <- payload
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.UseConnectionString(connectionString).SetContext(ctx).Build()
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

	t.Run("UnListen", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.UseConnectionString(connectionString).SetContext(ctx).Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		testChannel := "unlisten_channel"
		notificationReceived := make(chan string)

		err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
			NotificationCallback: func(channel string, payload string) {
				notificationReceived <- payload
			},
		})
		require.NoError(t, err)

		err = ln.UnListen(testChannel)
		require.NoError(t, err)

		err = ln.Notify(testChannel, "should_not_receive")
		require.NoError(t, err)

		select {
		case <-notificationReceived:
			t.Fatal("Received notification after UnListen")
		case <-time.After(2 * time.Second):
			// Test passed, no notification received
		}
	})

	t.Run("Custom Pool", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		customPool, err := pgxpool.New(ctx, connectionString)
		require.NoError(t, err)
		defer customPool.Close()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.SetPool(customPool).SetContext(ctx).Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		testChannel := "custom_pool_channel"
		notificationReceived := make(chan string)

		err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
			NotificationCallback: func(channel string, payload string) {
				notificationReceived <- payload
			},
		})
		require.NoError(t, err)

		testPayload := "custom_pool_payload"
		err = ln.Notify(testChannel, testPayload)
		require.NoError(t, err)

		select {
		case received := <-notificationReceived:
			assert.Equal(t, testPayload, received)
		case <-ctx.Done():
			t.Fatal("Timed out waiting for notification")
		}
	})

	t.Run("Custom Timeouts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		builder := pgln.NewPGListenNotifyBuilder()
		ln, err := builder.UseConnectionString(connectionString).
			SetContext(ctx).
			SetConnectTimeout(3 * time.Second).
			SetHealthCheckTimeout(1 * time.Second).
			SetReconnectInterval(500 * time.Millisecond).
			Build()
		require.NoError(t, err)
		err = ln.Start()
		require.NoError(t, err)
		defer ln.Shutdown()

		testChannel := "custom_timeout_channel"
		notificationReceived := make(chan string)

		err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
			NotificationCallback: func(channel string, payload string) {
				notificationReceived <- payload
			},
		})
		require.NoError(t, err)

		testPayload := "custom_timeout_payload"
		err = ln.Notify(testChannel, testPayload)
		require.NoError(t, err)

		select {
		case received := <-notificationReceived:
			assert.Equal(t, testPayload, received)
		case <-ctx.Done():
			t.Fatal("Timed out waiting for notification")
		}
	})
}

func TestNotifyQuery(t *testing.T) {
	connectionString, cleanup := setupTestContainer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	builder := pgln.NewPGListenNotifyBuilder()
	ln, err := builder.UseConnectionString(connectionString).SetContext(ctx).Build()
	require.NoError(t, err)
	err = ln.Start()
	require.NoError(t, err)
	defer ln.Shutdown()

	testChannel := "notify_query_channel"
	testPayload := "test_payload"

	result := ln.NotifyQuery(testChannel, testPayload)
	assert.Equal(t, "SELECT pg_notify($1, $2)", result.Query)
	assert.Equal(t, []string{testChannel, testPayload}, result.Params)

	// Test executing the query
	pool, err := pgxpool.New(ctx, connectionString)
	require.NoError(t, err)
	defer pool.Close()

	notificationReceived := make(chan string)
	err = ln.ListenAndWaitForListening(testChannel, pgln.ListenOptions{
		NotificationCallback: func(channel string, payload string) {
			notificationReceived <- payload
		},
	})
	require.NoError(t, err)

	_, err = pool.Exec(ctx, result.Query, result.Params[0], result.Params[1])
	require.NoError(t, err)

	select {
	case received := <-notificationReceived:
		assert.Equal(t, testPayload, received)
	case <-ctx.Done():
		t.Fatal("Timed out waiting for notification")
	}
}

func TestReadmeExample(t *testing.T) {
	connectionString, cleanup := setupTestContainer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	builder := pgln.NewPGListenNotifyBuilder().
		SetContext(ctx).
		SetReconnectInterval(5 * time.Second).
		UseConnectionString(connectionString)

	r, err := builder.Build()
	require.NoError(t, err)
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
	require.NoError(t, err)
	if err != nil {
		fmt.Printf("Listen error: %v\n", err)
		return
	}

	<-ctx.Done()
	require.NoError(t, err)
}
