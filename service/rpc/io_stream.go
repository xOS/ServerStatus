package rpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type ioStreamContext struct {
	userIo           io.ReadWriteCloser
	agentIo          io.ReadWriteCloser
	userIoConnectCh  chan struct{}
	agentIoConnectCh chan struct{}
}

type bp struct {
	buf []byte
}

var bufPool = sync.Pool{
	New: func() any {
		return &bp{
			buf: make([]byte, 1024*1024),
		}
	},
}

func (s *ServerHandler) CreateStream(streamId string) {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	s.ioStreams[streamId] = &ioStreamContext{
		userIoConnectCh:  make(chan struct{}),
		agentIoConnectCh: make(chan struct{}),
	}
}

func (s *ServerHandler) GetStream(streamId string) (*ioStreamContext, error) {
	s.ioStreamMutex.RLock()
	defer s.ioStreamMutex.RUnlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		return ctx, nil
	}

	return nil, errors.New("stream not found")
}

func (s *ServerHandler) CloseStream(streamId string) error {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	if ctx, ok := s.ioStreams[streamId]; ok {
		if ctx.userIo != nil {
			ctx.userIo.Close()
		}
		if ctx.agentIo != nil {
			ctx.agentIo.Close()
		}
		delete(s.ioStreams, streamId)
	}

	return nil
}

func (s *ServerHandler) UserConnected(streamId string, userIo io.ReadWriteCloser) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	stream.userIo = userIo
	close(stream.userIoConnectCh)

	return nil
}

func (s *ServerHandler) AgentConnected(streamId string, agentIo io.ReadWriteCloser) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	stream.agentIo = agentIo
	close(stream.agentIoConnectCh)

	return nil
}

func (s *ServerHandler) StartStream(streamId string, timeout time.Duration) error {
	stream, err := s.GetStream(streamId)
	if err != nil {
		return err
	}

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop() // 确保 timer 总是被正确清理

	// 等待连接建立，使用 select 避免死循环
	for {
		select {
		case <-stream.userIoConnectCh:
			if stream.agentIo != nil {
				goto CONNECTED
			}
		case <-stream.agentIoConnectCh:
			if stream.userIo != nil {
				goto CONNECTED
			}
		case <-timeoutTimer.C:
			goto TIMEOUT
		}
		time.Sleep(time.Millisecond * 100) // 减少轮询间隔
	}

TIMEOUT:
	if stream.userIo == nil && stream.agentIo == nil {
		return errors.New("timeout: no connection established")
	}
	if stream.userIo == nil {
		return errors.New("timeout: user connection not established")
	}
	if stream.agentIo == nil {
		return errors.New("timeout: agent connection not established")
	}

CONNECTED:
	isDone := new(atomic.Bool)
	endCh := make(chan struct{})

	// 添加流复制超时控制，防止长时间阻塞
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour*24) // 24小时超时
	defer cancel()

	go func() {
		defer func() {
			if isDone.CompareAndSwap(false, true) {
				close(endCh)
			}
		}()

		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)

		// 使用带超时的复制
		done := make(chan error, 1)
		go func() {
			_, innerErr := io.CopyBuffer(stream.userIo, stream.agentIo, bp.buf)
			done <- innerErr
		}()

		select {
		case innerErr := <-done:
			if innerErr != nil {
				err = innerErr
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
	}()

	go func() {
		defer func() {
			if isDone.CompareAndSwap(false, true) {
				close(endCh)
			}
		}()

		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)

		// 使用带超时的复制
		done := make(chan error, 1)
		go func() {
			_, innerErr := io.CopyBuffer(stream.agentIo, stream.userIo, bp.buf)
			done <- innerErr
		}()

		select {
		case innerErr := <-done:
			if innerErr != nil {
				err = innerErr
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
	}()

	<-endCh
	return err
}
