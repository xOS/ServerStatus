package grpcx

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/xos/serverstatus/proto"
)

var _ io.ReadWriteCloser = &IOStreamWrapper{}

type IOStream interface {
	Recv() (*proto.IOStreamData, error)
	Send(*proto.IOStreamData) error
	Context() context.Context
}

type IOStreamWrapper struct {
	IOStream
	dataBuf []byte
	closed  *atomic.Bool
	closeCh chan struct{}
}

func NewIOStreamWrapper(stream IOStream) *IOStreamWrapper {
	return &IOStreamWrapper{
		IOStream: stream,
		closeCh:  make(chan struct{}),
		closed:   new(atomic.Bool),
	}
}

func (iw *IOStreamWrapper) Read(p []byte) (n int, err error) {
	if len(iw.dataBuf) > 0 {
		n := copy(p, iw.dataBuf)
		iw.dataBuf = iw.dataBuf[n:]
		// 如果dataBuf已空，清理引用以便GC回收
		if len(iw.dataBuf) == 0 {
			iw.dataBuf = nil
		}
		return n, nil
	}
	var data *proto.IOStreamData
	if data, err = iw.Recv(); err != nil {
		return 0, err
	}
	n = copy(p, data.Data)
	if n < len(data.Data) {
		// 只在必要时保存剩余数据
		remaining := len(data.Data) - n
		if remaining > 0 {
			iw.dataBuf = make([]byte, remaining)
			copy(iw.dataBuf, data.Data[n:])
		}
	}
	return n, nil
}

func (iw *IOStreamWrapper) Write(p []byte) (n int, err error) {
	// 限制单次写入的数据大小，防止过大的消息
	const maxChunkSize = 64 * 1024 // 64KB chunks
	
	written := 0
	for written < len(p) {
		end := written + maxChunkSize
		if end > len(p) {
			end = len(p)
		}
		
		chunk := p[written:end]
		if err := iw.Send(&proto.IOStreamData{Data: chunk}); err != nil {
			return written, err
		}
		written += len(chunk)
	}
	
	return len(p), nil
}

func (iw *IOStreamWrapper) Close() error {
	if iw.closed.CompareAndSwap(false, true) {
		// 清理缓冲区
		iw.dataBuf = nil
		close(iw.closeCh)
	}
	return nil
}

func (iw *IOStreamWrapper) Wait() {
	<-iw.closeCh
}
