package codex

import (
	"context"
	"sync"
)

// activePrompt 保存一个 session 唯一 pending/active turn 的取消身份。
// 字段对应 upstream ActivePrompt 与 PendingTurnStart，合并后仍保持 turn-start 前后语义。
type activePrompt struct {
	// mu 保护 turnID、cancelRequested 和 finished。
	mu sync.Mutex
	// generation 是 Adapter 为每个 turn 分配的单调 runtime generation。
	generation uint64
	// eventRouter 把当前 turn 的 typed 通知映射到 ACP connection。
	eventRouter *eventRouter
	// runCtx 只约束当前 RunTurn；普通 ACP 取消保留它以观察迟到 start，session close 则显式终止它。
	runCtx context.Context
	// runCancel 在 session/Adapter close 时解除 pending turn/start 或 completion waiter。
	runCancel context.CancelFunc
	// turnID 在 turn/start 响应前为空，迟到响应仍会填入以便 interrupt。
	turnID string
	// cancelRequested 表示 ACP cancel、请求 context 或 close 已到达。
	cancelRequested bool
	// finished 表示前台 Prompt 已决定结果；迟到 start 必须视为 stale。
	finished bool
	// cancelSignal 在首次取消时关闭，解除 turn-start 前的前台等待。
	cancelSignal chan struct{}
	// cancelOnce 保证 cancelSignal 只关闭一次。
	cancelOnce sync.Once
	// interruptOnce 保证同一 turn 最多发送一次 turn/interrupt。
	interruptOnce sync.Once
	// interruptDone 在 interrupt 请求结束时关闭；尚未触发时保持开放。
	interruptDone chan struct{}
	// turnStarted 在 turn/start 返回身份后关闭，steering 可等待 pending start 而不制造 rival turn。
	turnStarted chan struct{}
	// turnStartedOnce 保证迟到回调或异常重复回调不会重复关闭。
	turnStartedOnce sync.Once
	// foregroundDone 在 ACP Prompt 或 steering 启动调用完成其前台返回后关闭。
	foregroundDone chan struct{}
	// foregroundDoneOnce 保证前台返回信号只关闭一次。
	foregroundDoneOnce sync.Once
	// backgroundDone 在 RunTurn 完整结束、迟到身份已处理后关闭。
	backgroundDone chan struct{}
	// backgroundDoneOnce 保证清理路径幂等。
	backgroundDoneOnce sync.Once
}

// newActivePrompt 创建尚未取消、尚无 turn ID 的 prompt 身份与独立 RunTurn 生命周期。
func newActivePrompt(parent context.Context, generation uint64) *activePrompt {
	runCtx, runCancel := context.WithCancel(parent)
	return &activePrompt{
		generation:     generation,
		runCtx:         runCtx,
		runCancel:      runCancel,
		cancelSignal:   make(chan struct{}),
		interruptDone:  make(chan struct{}),
		turnStarted:    make(chan struct{}),
		foregroundDone: make(chan struct{}),
		backgroundDone: make(chan struct{}),
	}
}

// markForegroundDone 通知 close 路径前台调用已执行完 finally 清理。
func (p *activePrompt) markForegroundDone() {
	p.foregroundDoneOnce.Do(func() { close(p.foregroundDone) })
}

// cancelRun 终止底层 pending turn/start 或 completion waiter；普通请求取消不会调用它，以保留迟到观察者。
func (p *activePrompt) cancelRun() {
	p.runCancel()
}

// requestCancel 幂等记录取消，并返回当前已知 turn ID。
func (p *activePrompt) requestCancel() string {
	p.mu.Lock()
	p.cancelRequested = true
	turnID := p.turnID
	p.mu.Unlock()
	p.cancelOnce.Do(func() { close(p.cancelSignal) })
	return turnID
}

// setTurn 记录 turn/start 结果，并返回它是否已经属于取消/结束后的迟到 turn。
func (p *activePrompt) setTurn(turnID string) bool {
	p.mu.Lock()
	p.turnID = turnID
	late := p.cancelRequested || p.finished
	p.mu.Unlock()
	p.turnStartedOnce.Do(func() { close(p.turnStarted) })
	return late
}

// markBackgroundDone 通知 steering/close 当前 prompt 已完全释放 session 槽位。
func (p *activePrompt) markBackgroundDone() {
	p.backgroundDoneOnce.Do(func() { close(p.backgroundDone) })
}

// currentTurn 返回当前 turn ID 与取消状态快照。
func (p *activePrompt) currentTurn() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnID, p.cancelRequested
}

// setEventRouter 安装与已知 turn ID/generation 绑定的事件路由器。
func (p *activePrompt) setEventRouter(router *eventRouter) {
	p.mu.Lock()
	p.eventRouter = router
	p.mu.Unlock()
}

// currentEventRouter 返回当前 prompt 的事件路由器快照。
func (p *activePrompt) currentEventRouter() *eventRouter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eventRouter
}

// markForegroundFinished 标记 ACP Prompt 已返回；后台只负责 late interrupt/completion 清理。
func (p *activePrompt) markForegroundFinished() {
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
}
