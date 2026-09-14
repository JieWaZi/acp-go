package pi

import (
	"context"
	"sync"
)

// contextLock 允许排队中的操作及时响应 Context 取消。
type contextLock struct {
	// once 只初始化一次互斥令牌。
	once sync.Once
	// token 表示当前是否有操作占用会话。
	token chan struct{}
}

// Lock 等待会话空闲，取消的排队任务不占用执行线程。
func (l *contextLock) Lock(ctx context.Context) error {
	l.once.Do(func() { l.token = make(chan struct{}, 1) })
	select {
	case l.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unlock 释放会话令牌。
func (l *contextLock) Unlock() { <-l.token }
