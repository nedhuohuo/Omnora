package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BindController owns the process HTTP listener so admin network-entry updates
// can rebind lan_http without requiring a full process restart.
type BindController struct {
	mu       sync.Mutex
	addr     string
	handler  http.Handler
	server   *http.Server
	listener net.Listener
}

func NewBindController() *BindController {
	return &BindController{}
}

func (c *BindController) ActiveAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addr
}

// ListenAndServe binds addr and serves handler until Shutdown.
func (c *BindController) ListenAndServe(addr string, handler http.Handler) error {
	if err := c.bind(addr, handler); err != nil {
		return err
	}
	c.mu.Lock()
	srv := c.server
	ln := c.listener
	c.mu.Unlock()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Start binds addr and serves handler in a background goroutine. Bind errors
// are returned synchronously; runtime serving errors surface via the process
// health endpoints rather than here.
func (c *BindController) Start(addr string, handler http.Handler) error {
	if err := c.bind(addr, handler); err != nil {
		return err
	}
	c.mu.Lock()
	srv := c.server
	ln := c.listener
	c.mu.Unlock()
	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

// Rebind switches the active listener to addr. Existing connections on the
// previous listener are drained with a short shutdown timeout.
func (c *BindController) Rebind(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("bind address is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handler == nil {
		return fmt.Errorf("listener has not started")
	}
	if addr == c.addr {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	oldSrv := c.server
	oldLn := c.listener
	c.listener = ln
	c.addr = addr
	c.server = &http.Server{
		Handler:           c.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func(srv *http.Server, listener net.Listener) {
		_ = srv.Serve(listener)
	}(c.server, ln)
	if oldSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = oldSrv.Shutdown(ctx)
	}
	if oldLn != nil {
		_ = oldLn.Close()
	}
	return nil
}

func (c *BindController) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	srv := c.server
	c.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// Stop shuts down the listener and clears its state so ActiveAddr reports the
// controller as inactive.
func (c *BindController) Stop(ctx context.Context) error {
	c.mu.Lock()
	srv := c.server
	ln := c.listener
	c.mu.Unlock()
	if srv == nil {
		return nil
	}
	err := srv.Shutdown(ctx)
	if ln != nil {
		_ = ln.Close()
	}
	c.mu.Lock()
	c.server = nil
	c.listener = nil
	c.handler = nil
	c.addr = ""
	c.mu.Unlock()
	return err
}

func (c *BindController) bind(addr string, handler http.Handler) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if handler == nil {
		return fmt.Errorf("handler is required")
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("bind address is required")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	c.handler = handler
	c.listener = ln
	c.addr = addr
	c.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return nil
}

// ListenerManager owns one BindController per named network entry so entries
// can be started, stopped, or rebound independently at runtime.
type ListenerManager struct {
	mu    sync.Mutex
	ctrls map[string]*BindController
}

func NewListenerManager() *ListenerManager {
	return &ListenerManager{ctrls: make(map[string]*BindController)}
}

func (m *ListenerManager) controller(name string) *BindController {
	c := m.ctrls[name]
	if c == nil {
		c = NewBindController()
		m.ctrls[name] = c
	}
	return c
}

// Start binds addr for name and serves handler in the background. When the
// entry is already active it is rebound to addr instead; a no-op when the
// address is unchanged.
func (m *ListenerManager) Start(name, addr string, handler http.Handler) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("bind address for %q is required", name)
	}
	if handler == nil {
		return fmt.Errorf("handler for %q is required", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.controller(name)
	if active := c.ActiveAddr(); active != "" {
		if active == addr {
			return nil
		}
		return c.Rebind(addr)
	}
	return c.Start(addr, handler)
}

// Rebind swaps the active listener for name to addr. The listener must have
// been started (via Start) first.
func (m *ListenerManager) Rebind(name, addr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.ctrls[name]
	if c == nil {
		return fmt.Errorf("listener %q has not started", name)
	}
	return c.Rebind(addr)
}

// Stop shuts down the listener for name and clears its state.
func (m *ListenerManager) Stop(name string) error {
	m.mu.Lock()
	c := m.ctrls[name]
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Stop(ctx)
}

// ActiveAddr reports the currently bound address for name, or "" when the
// entry has no active listener.
func (m *ListenerManager) ActiveAddr(name string) string {
	m.mu.Lock()
	c := m.ctrls[name]
	m.mu.Unlock()
	if c == nil {
		return ""
	}
	return c.ActiveAddr()
}

// Active reports whether name has a running listener.
func (m *ListenerManager) Active(name string) bool {
	return m.ActiveAddr(name) != ""
}

// Shutdown drains every active listener.
func (m *ListenerManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	ctrls := make([]*BindController, 0, len(m.ctrls))
	for _, c := range m.ctrls {
		ctrls = append(ctrls, c)
	}
	m.mu.Unlock()
	var firstErr error
	for _, c := range ctrls {
		if err := c.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
