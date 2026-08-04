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
