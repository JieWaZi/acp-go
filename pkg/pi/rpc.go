package pi

// Pi RPC 进程层对照 svkozak/pi-acp src/pi-rpc/process.ts 移植。
// MIT Copyright (c) 2025 Sergii Kozak；完整许可见 UPSTREAM-LICENSE。
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// rpcProcess 拥有一个直接启动的 Pi CLI，不启动 ACP 桥进程。
type rpcProcess struct {
	// command 是 Pi 的唯一子进程。
	command *exec.Cmd
	// input 发送官方 RPC 命令和扩展 UI 响应。
	input io.WriteCloser
	// output 接收 Pi JSON 行与事件。
	output io.ReadCloser
	// cancel 回收进程及其后代。
	cancel context.CancelFunc
	// mutex 保护请求关联与关闭状态。
	mutex sync.Mutex
	// writeMutex 保证每个 JSON 命令整体写入。
	writeMutex sync.Mutex
	// pending 保存尚未完成的官方 RPC 请求。
	pending map[string]chan rpcResponse
	// sequence 为请求分配唯一标识。
	sequence atomic.Uint64
	// events 按 Pi 发出顺序交付事件，不静默丢弃。
	events chan map[string]any
	// done 在进程完全回收后关闭。
	done chan struct{}
	// stop 通知读取循环终止。
	stop chan struct{}
	// once 保证关闭幂等。
	once sync.Once
}

// rpcResponse 是 Pi 官方 response 信封，不能当作 ACP JSON-RPC。
type rpcResponse struct {
	// ID 关联客户端发送的命令。
	ID string `json:"id"`
	// Type 区分请求结果和流式事件。
	Type string `json:"type"`
	// Success 表示命令是否执行成功。
	Success bool `json:"success"`
	// Data 保留官方结果内容。
	Data json.RawMessage `json:"data"`
	// Error 是 Pi 返回的错误说明。
	Error string `json:"error"`
}

// startRPC 按 upstream 的 rpc/no-themes 参数直接启动 Pi。
func startRPC(config Config, cwd, extension, sessionPath string) (*rpcProcess, error) {
	ctx, cancel := context.WithCancel(context.Background())
	args := append([]string(nil), config.PrefixArgs...)
	args = append(args, "--mode", "rpc", "--no-themes", "--extension", extension)
	if sessionPath != "" {
		args = append(args, "--session", sessionPath)
	}
	cmd := exec.CommandContext(ctx, config.PiPath, args...)
	prepareProcess(cmd)
	cmd.Dir, cmd.Env, cmd.WaitDelay = cwd, config.Environment, 2*time.Second
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	p := &rpcProcess{command: cmd, input: input, output: output, cancel: cancel, pending: map[string]chan rpcResponse{}, events: make(chan map[string]any, 256), done: make(chan struct{}), stop: make(chan struct{})}
	go p.read()
	return p, nil
}

// read 关联响应并按序投递事件，退出时解除所有等待。
func (p *rpcProcess) read() {
	defer func() { p.cancel(); _ = p.command.Wait(); close(p.events); close(p.done) }()
	scanner := bufio.NewScanner(p.output)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var response rpcResponse
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		} // Pi 启动前导文本不是协议结果。
		if response.Type == "response" && response.ID != "" {
			p.mutex.Lock()
			ch := p.pending[response.ID]
			delete(p.pending, response.ID)
			p.mutex.Unlock()
			if ch != nil {
				ch <- response
			}
			continue
		}
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		select {
		case p.events <- event:
		case <-p.stop:
			return
		}
	}
}

// write 原子写入一条官方 RPC 消息。
func (p *rpcProcess) write(value map[string]any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	p.writeMutex.Lock()
	defer p.writeMutex.Unlock()
	_, err = p.input.Write(append(data, '\n'))
	return err
}

// rpcCall 保存已经写入 Pi 的请求，允许发送顺序与等待响应分离。
type rpcCall struct {
	// process 持有响应关联表。
	process *rpcProcess
	// id 是本次请求的关联标识。
	id string
	// command 用于响应错误说明。
	command string
	// response 接收对应请求的唯一结果。
	response chan rpcResponse
}

// begin 原子登记并发送命令；调用者可用会话锁保证 prompt/abort 的发送顺序。
func (p *rpcProcess) begin(ctx context.Context, command string, params map[string]any) (*rpcCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := strconv.FormatUint(p.sequence.Add(1), 10)
	request := map[string]any{"id": id, "type": command}
	for key, value := range params {
		request[key] = value
	}
	call := &rpcCall{process: p, id: id, command: command, response: make(chan rpcResponse, 1)}
	p.mutex.Lock()
	p.pending[id] = call.response
	p.mutex.Unlock()
	// 管道阻塞时取消可回收失去响应的进程，避免持锁等待不可中断的 Write。
	stop := context.AfterFunc(ctx, func() { _ = p.input.Close(); p.cancel() })
	err := p.write(request)
	stop()
	if err != nil || ctx.Err() != nil {
		call.forget()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return call, nil
}

// forget 释放已完成或取消请求的关联记录。
func (call *rpcCall) forget() {
	call.process.mutex.Lock()
	delete(call.process.pending, call.id)
	call.process.mutex.Unlock()
}

// wait 以调用方 Context 限定响应等待，不阻止其他命令发出。
func (call *rpcCall) wait(ctx context.Context, output any) error {
	defer call.forget()
	select {
	case response := <-call.response:
		if !response.Success {
			return fmt.Errorf("Pi %s failed: %s", call.command, response.Error)
		}
		if output != nil && len(response.Data) > 0 {
			return json.Unmarshal(response.Data, output)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-call.process.done:
		return errors.New("Pi process exited")
	}
}

// call 发送命令并等待对应的官方 RPC 响应。
func (p *rpcProcess) call(ctx context.Context, command string, params map[string]any, output any) error {
	call, err := p.begin(ctx, command, params)
	if err != nil {
		return err
	}
	return call.wait(ctx, output)
}

// close 先结束 stdin，超时后回收整个 Pi 进程组。
func (p *rpcProcess) close(ctx context.Context) error {
	p.once.Do(func() { close(p.stop); _ = p.input.Close() })
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	p.cancel()
	_ = p.output.Close()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
