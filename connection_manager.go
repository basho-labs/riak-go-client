package riak

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// Constants identifying connectionManager state
const (
	cmCreated state = iota
	cmRunning
	cmShuttingDown
	cmShutdown
	cmError
)

type connectionManagerOptions struct {
	addr                   *net.TCPAddr
	minConnections         uint16
	maxConnections         uint16
	idleExpirationInterval time.Duration
	idleTimeout            time.Duration
	connectTimeout         time.Duration
	requestTimeout         time.Duration
	authOptions            *AuthOptions
}

type connectionManager struct {
	addr                   *net.TCPAddr
	minConnections         uint16
	maxConnections         uint16
	idleExpirationInterval time.Duration
	idleTimeout            time.Duration
	connectTimeout         time.Duration
	requestTimeout         time.Duration
	authOptions            *AuthOptions
	stopChan               chan struct{}
	q                      *queue
	expireTicker           *time.Ticker
	connectionCount        uint16
	sync.RWMutex
	stateData
}

var (
	ErrConnectionManagerRequiresOptions         = newClientError("[connectionManager] new manager requires options", nil)
	ErrConnectionManagerRequiresAddress         = newClientError("[connectionManager] new manager requires non-nil address", nil)
	ErrConnectionManagerMaxMustBeGreaterThanMin = newClientError("[connectionManager] new connection manager maxConnections must be greater than minConnections", nil)
	ErrConnMgrAllConnectionsInUse               = newClientError("[connectionManager] all connections in use / max connections reached", nil)
)

func newConnectionManager(options *connectionManagerOptions) (*connectionManager, error) {
	if options == nil {
		return nil, ErrConnectionManagerRequiresOptions
	}
	if options.addr == nil {
		return nil, ErrConnectionManagerRequiresAddress
	}
	if options.minConnections == 0 {
		options.minConnections = defaultMinConnections
	}
	if options.maxConnections == 0 {
		options.maxConnections = defaultMaxConnections
	}
	if options.minConnections > options.maxConnections {
		return nil, ErrConnectionManagerMaxMustBeGreaterThanMin
	}
	if options.idleExpirationInterval == 0 {
		options.idleExpirationInterval = defaultIdleExpirationInterval
	}
	if options.idleTimeout == 0 {
		options.idleTimeout = defaultIdleTimeout
	}
	if options.connectTimeout == 0 {
		options.connectTimeout = defaultConnectTimeout
	}
	if options.requestTimeout == 0 {
		options.requestTimeout = defaultRequestTimeout
	}
	cm := &connectionManager{
		addr:                   options.addr,
		minConnections:         options.minConnections,
		maxConnections:         options.maxConnections,
		idleExpirationInterval: options.idleExpirationInterval,
		idleTimeout:            options.idleTimeout,
		connectTimeout:         options.connectTimeout,
		requestTimeout:         options.requestTimeout,
		authOptions:            options.authOptions,
		stopChan:               make(chan struct{}),
		q:                      newQueue(options.maxConnections),
	}
	cm.initStateData("connMgrError", "connMgrCreated", "connMgrRunning", "connMgrShuttingDown", "connMgrShutdown")
	cm.setState(cmCreated)
	return cm, nil
}

func (cm *connectionManager) String() string {
	return fmt.Sprintf("%v", cm.addr)
}

func (cm *connectionManager) start() error {
	if err := cm.stateCheck(cmCreated); err != nil {
		return err
	}
	for i := uint16(0); i < cm.minConnections; i++ {
		conn, err := cm.create()
		if err == nil {
			cm.put(conn)
		} else {
			logErr("[connectionManager]", err)
		}
	}
	cm.expireTicker = time.NewTicker(cm.idleExpirationInterval)
	go cm.expireConnections()
	cm.setState(cmRunning)
	return nil
}

func (cm *connectionManager) stop() error {
	if err := cm.stateCheck(cmRunning); err != nil {
		return err
	}

	logDebug("[connectionManager]", "shutting down")

	cm.setState(cmShuttingDown)
	close(cm.stopChan)
	cm.expireTicker.Stop()

	if cm.count() != cm.q.count() {
		logError("[connectionManager]", "stop: current connection count '%d' does NOT equal q count '%d'", cm.count(), cm.q.count())
	}

	cm.Lock()
	defer cm.Unlock()

	var f = func(v interface{}) (bool, bool) {
		if v == nil {
			return true, false
		}
		conn := v.(*connection)
		if err := conn.close(); err != nil {
			logErr("[connectionManager] error when closing connection in stop()", err)
		}
		cm.connectionCount--
		if cm.connectionCount == 0 {
			return true, false
		} else {
			return false, false
		}
	}
	err := cm.q.iterate(f)
	cm.q.destroy()

	if err == nil {
		cm.setState(cmShutdown)
	} else {
		cm.setState(cmError)
	}

	return err
}

func (cm *connectionManager) count() uint16 {
	cm.RLock()
	defer cm.RUnlock()
	return cm.connectionCount
}

func (cm *connectionManager) create() (*connection, error) {
	if !cm.isStateLessThan(cmShuttingDown) {
		return nil, nil
	}

	cm.Lock()
	defer cm.Unlock()

	if cm.connectionCount >= cm.maxConnections {
		return nil, ErrConnMgrAllConnectionsInUse
	}

	conn, err := cm.createConnection()
	if err != nil {
		return nil, err
	}

	cm.connectionCount++
	return conn, nil
}

func (cm *connectionManager) createConnection() (*connection, error) {
	opts := &connectionOptions{
		remoteAddress:  cm.addr,
		connectTimeout: cm.connectTimeout,
		requestTimeout: cm.requestTimeout,
		authOptions:    cm.authOptions,
	}
	conn, err := newConnection(opts)
	if err != nil {
		return nil, err
	}
	err = conn.connect()
	return conn, err
}

func (cm *connectionManager) get() (*connection, error) {
	var conn *connection
	var f = func(v interface{}) (bool, bool) {
		if v == nil {
			// connection pool is empty
			return true, false
		}
		conn = v.(*connection)
		if conn.available() {
			// we found our connection, don't re-queue
			return true, false
		} else {
			// keep going and re-queue conn
			return false, true
		}
	}
	err := cm.q.iterate(f)
	if err != nil {
		return nil, err
	}

	if conn != nil {
		return conn, nil
	}

	// NB: if we get here, there were no available connections
	return cm.create()
}

func (cm *connectionManager) put(conn *connection) error {
	if cm.isStateLessThan(cmShuttingDown) {
		return cm.q.enqueue(conn)
	} else {
		// shutting down
		logDebug("[connectionManager]", "(%v)|Connection returned during shutdown.", cm)
		cm.Lock()
		defer cm.Unlock()
		cm.connectionCount--
		conn.close() // NB: discard error
	}
	return nil
}

func (cm *connectionManager) remove(conn *connection) error {
	if cm.isStateLessThan(cmShuttingDown) {
		cm.Lock()
		defer cm.Unlock()
		cm.connectionCount--
		return conn.close()
	}
	return nil
}

func (cm *connectionManager) expireConnections() {
	logDebug("[connectionManager]", "connection expiration routine is starting")
	for {
		select {
		case <-cm.stopChan:
			logDebug("[connectionManager]", "connection expiration routine is quitting")
			return
		case t := <-cm.expireTicker.C:
			if !cm.isStateLessThan(cmShuttingDown) {
				logDebug("[connectionManager]", "(%v) connection expiration routine is quitting.", cm)
			}

			logDebug("[connectionManager]", "(%v) expiring connections at %v", cm, t)

			expiredCount := uint16(0)
			now := time.Now()

			var f = func(v interface{}) (bool, bool) {
				if v == nil {
					// connection pool is empty
					return true, false
				}
				if !cm.isStateLessThan(cmShuttingDown) {
					return true, true
				}
				conn := v.(*connection)
				cm.Lock()
				defer cm.Unlock()
				if cm.connectionCount > cm.minConnections {
					// expire connection if not available or if it has passed idle timeout
					if !conn.available() || (now.Sub(conn.lastUsed) >= cm.idleTimeout) {
						cm.connectionCount--
						if err := conn.close(); err != nil {
							logErr("[connectionManager]", err)
						}
						expiredCount++
						return false, false // don't break, don't re-enqueue
					} else {
						return false, true // don't break, re-enqueue
					}
				}
				return true, true // break, re-enqueue
			}

			if err := cm.q.iterate(f); err != nil {
				logErr("[connectionManager]", err)
			}

			logDebug("[connectionManager]", "(%v) expired %d connections.", cm, expiredCount)

			for cm.connectionCount < cm.minConnections {
				conn, err := cm.create()
				if err == nil {
					cm.put(conn)
				} else {
					logErr("[connectionManager]", err)
				}
			}

			if !cm.isStateLessThan(cmShuttingDown) {
				logDebug("[connectionManager]", "(%v) connection expiration routine is quitting.", cm)
			}
		}
	}
}
