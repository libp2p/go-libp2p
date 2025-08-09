package itest

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	logging "github.com/libp2p/go-libp2p/gologshim"
)

const (
	EchoService = "test.echo"
	EchoProtoID = "/test/echo"
)

var (
	echoLog = logging.Logger("echo")
)

type Echo struct {
	Host host.Host

	mx     sync.Mutex
	status EchoStatus

	beforeReserve, beforeRead, beforeWrite, beforeDone func() error
	done                                               func()
}

type EchoStatus struct {
	StreamsIn                 int
	EchosIn, EchosOut         int
	IOErrors                  int
	ResourceServiceErrors     int
	ResourceReservationErrors int
}

func NewEcho(h host.Host) *Echo {
	e := &Echo{Host: h}
	h.SetStreamHandler(EchoProtoID, e.handleStream)
	return e
}

func (e *Echo) Status() EchoStatus {
	e.mx.Lock()
	defer e.mx.Unlock()

	return e.status
}

func (e *Echo) BeforeReserve(f func() error) {
	e.mx.Lock()
	defer e.mx.Unlock()

	e.beforeReserve = f
}

func (e *Echo) BeforeRead(f func() error) {
	e.mx.Lock()
	defer e.mx.Unlock()

	e.beforeRead = f
}

func (e *Echo) BeforeWrite(f func() error) {
	e.mx.Lock()
	defer e.mx.Unlock()

	e.beforeWrite = f
}

func (e *Echo) BeforeDone(f func() error) {
	e.mx.Lock()
	defer e.mx.Unlock()

	e.beforeDone = f
}

func (e *Echo) Done(f func()) {
	e.mx.Lock()
	defer e.mx.Unlock()

	e.done = f
}

func (e *Echo) getBeforeReserve() func() error {
	e.mx.Lock()
	defer e.mx.Unlock()

	return e.beforeReserve
}

func (e *Echo) getBeforeRead() func() error {
	e.mx.Lock()
	defer e.mx.Unlock()

	return e.beforeRead
}

func (e *Echo) getBeforeWrite() func() error {
	e.mx.Lock()
	defer e.mx.Unlock()

	return e.beforeWrite
}

func (e *Echo) getBeforeDone() func() error {
	e.mx.Lock()
	defer e.mx.Unlock()

	return e.beforeDone
}

func (e *Echo) getDone() func() {
	e.mx.Lock()
	defer e.mx.Unlock()

	return e.done
}

func (e *Echo) handleStream(s network.Stream) {
	defer s.Close()

	if done := e.getDone(); done != nil {
		defer done()
	}

	e.mx.Lock()
	e.status.StreamsIn++
	e.mx.Unlock()

	if beforeReserve := e.getBeforeReserve(); beforeReserve != nil {
		if err := beforeReserve(); err != nil {
			echoLog.Debug("error syncing before reserve", "err", err)

			s.Reset()
			return
		}
	}

	if err := s.Scope().SetService(EchoService); err != nil {
		echoLog.Debug("error attaching stream to echo service", "err", err)

		e.mx.Lock()
		e.status.ResourceServiceErrors++
		e.mx.Unlock()

		s.Reset()
		return
	}

	if err := s.Scope().ReserveMemory(4096, network.ReservationPriorityAlways); err != nil {
		echoLog.Debug("error reserving memory", "err", err)

		e.mx.Lock()
		e.status.ResourceReservationErrors++
		e.mx.Unlock()

		s.Reset()
		return
	}

	if beforeRead := e.getBeforeRead(); beforeRead != nil {
		if err := beforeRead(); err != nil {
			echoLog.Debug("error syncing before read", "err", err)

			s.Reset()
			return
		}
	}

	buf := make([]byte, 4096)

	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := s.Read(buf)
	switch {
	case err == io.EOF:
		if n == 0 {
			return
		}

	case err != nil:
		echoLog.Debug("I/O error", "err", err)

		e.mx.Lock()
		e.status.IOErrors++
		e.mx.Unlock()

		s.Reset()
		return
	}

	e.mx.Lock()
	e.status.EchosIn++
	e.mx.Unlock()

	if beforeWrite := e.getBeforeWrite(); beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			echoLog.Debug("error syncing before write", "err", err)

			s.Reset()
			return
		}
	}

	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = s.Write(buf[:n])
	if err != nil {
		echoLog.Debug("I/O error", "err", err)

		e.mx.Lock()
		e.status.IOErrors++
		e.mx.Unlock()

		s.Reset()
		return
	}

	e.mx.Lock()
	e.status.EchosOut++
	e.mx.Unlock()

	s.CloseWrite()

	if beforeDone := e.getBeforeDone(); beforeDone != nil {
		if err := beforeDone(); err != nil {
			echoLog.Debug("error syncing before done", "err", err)

			s.Reset()
		}
	}
}

func (e *Echo) Echo(p peer.ID, what string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	s, err := e.Host.NewStream(ctx, p, EchoProtoID)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Scope().SetService(EchoService); err != nil {
		echoLog.Debug("error attaching stream to echo service", "err", err)

		s.Reset()
		return err
	}

	if err := s.Scope().ReserveMemory(4096, network.ReservationPriorityAlways); err != nil {
		echoLog.Debug("error reserving memory", "err", err)

		s.Reset()
		return err
	}

	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = s.Write([]byte(what))
	if err != nil {
		return err
	}
	s.CloseWrite()

	buf := make([]byte, 4096)

	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := s.Read(buf)
	switch {
	case err == io.EOF:
		if n == 0 {
			return err
		}

	case err != nil:
		echoLog.Debug("I/O error", "err", err)

		s.Reset()
		return err
	}

	if what != string(buf[:n]) {
		return fmt.Errorf("echo output doesn't match input")
	}

	return nil
}
