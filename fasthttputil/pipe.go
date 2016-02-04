package fasthttputil

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

func newPipeConns() *pipeConns {
	ch1 := acquirePipeChan()
	ch2 := acquirePipeChan()

	pc := &pipeConns{}
	pc.c1.r = ch1
	pc.c1.w = ch2
	pc.c2.r = ch2
	pc.c2.w = ch1
	pc.c1.parent = pc
	pc.c2.parent = pc
	return pc
}

type pipeConns struct {
	c1 pipeConn
	c2 pipeConn
}

func (pc *pipeConns) release() {
	pc.c1.wlock.Lock()
	pc.c2.wlock.Lock()
	mustRelease := pc.c1.wclosed && pc.c2.wclosed
	pc.c1.wlock.Unlock()
	pc.c2.wlock.Unlock()

	if mustRelease {
		pc.c1.release()
		pc.c2.release()
	}
}

type pipeConn struct {
	r  *pipeChan
	w  *pipeChan
	b  *byteBuffer
	bb []byte

	rclosed bool

	wlock   sync.Mutex
	wclosed bool

	parent *pipeConns
}

func (c *pipeConn) Write(p []byte) (int, error) {
	b := acquireByteBuffer()
	b.b = append(b.b[:0], p...)

	c.wlock.Lock()
	if c.wclosed {
		c.wlock.Unlock()
		releaseByteBuffer(b)
		return 0, errors.New("connection closed for writing")
	}
	c.w.ch <- b
	c.wlock.Unlock()

	return len(p), nil
}

func (c *pipeConn) Read(p []byte) (int, error) {
	mayBlock := true
	nn := 0
	for len(p) > 0 {
		n, err := c.read(p, mayBlock)
		nn += n
		if err != nil {
			if !mayBlock && err == errWouldBlock {
				err = nil
			} else {
			}
			return nn, err
		}
		p = p[n:]
		mayBlock = false
	}

	return nn, nil
}

func (c *pipeConn) read(p []byte, mayBlock bool) (int, error) {
	if len(c.bb) == 0 {
		releaseByteBuffer(c.b)
		c.b = nil

		if c.rclosed {
			return 0, io.EOF
		}

		if mayBlock {
			c.b = <-c.r.ch
		} else {
			select {
			case c.b = <-c.r.ch:
			default:
				return 0, errWouldBlock
			}
		}

		if c.b == nil {
			c.rclosed = true
			return 0, io.EOF
		}
		c.bb = c.b.b
	}
	n := copy(p, c.bb)
	c.bb = c.bb[n:]

	return n, nil
}

var errWouldBlock = errors.New("would block")

func (c *pipeConn) Close() error {
	c.wlock.Lock()
	c.wclosed = true
	c.w.ch <- nil
	c.wlock.Unlock()

	c.parent.release()
	return nil
}

func (c *pipeConn) release() {
	releaseByteBuffer(c.b)
	releasePipeChan(c.r)

	c.r = nil
	c.w = nil
	c.b = nil
	c.bb = nil
}

func (p *pipeConn) LocalAddr() net.Addr {
	return pipeAddr(0)
}

func (p *pipeConn) RemoteAddr() net.Addr {
	return pipeAddr(0)
}

func (p *pipeConn) SetDeadline(t time.Time) error {
	return errors.New("deadline not supported")
}

func (p *pipeConn) SetReadDeadline(t time.Time) error {
	return p.SetDeadline(t)
}

func (p *pipeConn) SetWriteDeadline(t time.Time) error {
	return p.SetDeadline(t)
}

type pipeAddr int

func (pipeAddr) Network() string {
	return "pipe"
}

func (pipeAddr) String() string {
	return "pipe"
}

type byteBuffer struct {
	b []byte
}

func acquireByteBuffer() *byteBuffer {
	return byteBufferPool.Get().(*byteBuffer)
}

func releaseByteBuffer(b *byteBuffer) {
	if b != nil {
		byteBufferPool.Put(b)
	}
}

var byteBufferPool = &sync.Pool{
	New: func() interface{} {
		return &byteBuffer{}
	},
}

func acquirePipeChan() *pipeChan {
	ch := pipeChanPool.Get().(*pipeChan)
	if len(ch.ch) > 0 {
		panic("BUG: non-empty pipeChan acquired")
	}
	return ch
}

func releasePipeChan(ch *pipeChan) {
	for b := range ch.ch {
		releaseByteBuffer(b)
	}
	pipeChanPool.Put(ch)
}

var pipeChanPool = &sync.Pool{
	New: func() interface{} {
		return &pipeChan{
			ch: make(chan *byteBuffer, 4),
		}
	},
}

type pipeChan struct {
	ch chan *byteBuffer
}
