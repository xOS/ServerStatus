package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
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

// 优化缓冲池配置，减少内存占用
var bufPool = sync.Pool{
	New: func() any {
		return &bp{
			buf: make([]byte, 32*1024), // 降低到32KB，减少内存占用
		}
	},
}

// 清理函数，定期清理缓冲池中的大缓冲区
func cleanupBufferPool() {
	// 强制回收所有缓冲区，让sync.Pool重新创建
	bufPool = sync.Pool{
		New: func() any {
			return &bp{
				buf: make([]byte, 32*1024),
			}
		},
	}
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

// CleanupStaleStreams 清理过期的流连接，防止内存泄漏
func (s *ServerHandler) CleanupStaleStreams() {
	s.ioStreamMutex.Lock()
	defer s.ioStreamMutex.Unlock()

	staleStreams := make([]string, 0)

	for streamId, ctx := range s.ioStreams {
		// 检查流是否已经超时未使用
		if ctx.userIo == nil && ctx.agentIo == nil {
			// 简单的启发式检查：如果通道都已关闭但流仍存在，可能是泄漏
			select {
			case <-ctx.userIoConnectCh:
			case <-ctx.agentIoConnectCh:
			default:
				// 通道都未关闭，说明可能是新创建的流，给更多时间
				continue
			}
			staleStreams = append(staleStreams, streamId)
		}
	}

	// 清理过期流
	for _, streamId := range staleStreams {
		if ctx, ok := s.ioStreams[streamId]; ok {
			if ctx.userIo != nil {
				ctx.userIo.Close()
			}
			if ctx.agentIo != nil {
				ctx.agentIo.Close()
			}
			delete(s.ioStreams, streamId)
		}
	}

	if len(staleStreams) > 0 {
		log.Printf("清理了 %d 个过期的IO流连接", len(staleStreams))
	}
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
	// 根本修复：使用短超时context，确保goroutine能正确退出
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute) // 缩短到10分钟
	defer cancel()

	// 根本修复：使用sync.WaitGroup确保所有goroutine正确退出
	var wg sync.WaitGroup
	errCh := make(chan error, 2) // 缓冲channel，防止阻塞

	// 根本修复：简化为2个goroutine，移除嵌套goroutine
	wg.Add(2)

	// Agent -> User 复制
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("IO stream copy panic恢复: %v", r)
				errCh <- fmt.Errorf("copy panic: %v", r)
			}
		}()

		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)

		// 直接复制，不再创建子goroutine
		_, copyErr := io.CopyBuffer(stream.userIo, stream.agentIo, bp.buf)
		if copyErr != nil {
			select {
			case errCh <- copyErr:
			default:
			}
		}
	}()

	// User -> Agent 复制
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("IO stream copy panic恢复: %v", r)
				errCh <- fmt.Errorf("copy panic: %v", r)
			}
		}()

		bp := bufPool.Get().(*bp)
		defer bufPool.Put(bp)

		// 直接复制，不再创建子goroutine
		_, copyErr := io.CopyBuffer(stream.agentIo, stream.userIo, bp.buf)
		if copyErr != nil {
			select {
			case errCh <- copyErr:
			default:
			}
		}
	}()

	// 等待所有goroutine完成或超时
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 所有goroutine正常完成
		return nil
	case err := <-errCh:
		// 有错误发生，取消context让其他goroutine退出
		cancel()
		return err
	case <-ctx.Done():
		// 超时，context会自动取消所有goroutine
		return ctx.Err()
	}
}

// 启动定期IO流清理任务
func (s *ServerHandler) StartIOStreamCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute) // 每5分钟清理一次
		defer ticker.Stop()

		for range ticker.C {
			s.CleanupStaleStreams()
			cleanupBufferPool() // 清理缓冲池
		}
	}()
}
