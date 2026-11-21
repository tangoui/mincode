package terminal

import (
	"strings"
	"time"
)

// flushSize：缓冲达到 32KB 立即刷新，避免大流量输出合并出过大的单帧。
const flushSize = 32 * 1024

// ptyReadBufferSize：PTY/SSH 单次读取上限。4KB 在刷屏时会切出大量小块。
const ptyReadBufferSize = 32 * 1024

// outputCoalesceWindow：持续输出时两次推送的最小间隔。空闲后的第一块立即推送，
// 交互回显不增加延迟；刷屏时把一个窗口内的块合成一帧，减少 shell-output 事件数。
const outputCoalesceWindow = 4 * time.Millisecond

// outputBatch 终端输出微批合并缓冲。
// 非并发安全，调用方需自行保证单 goroutine 访问。
type outputBatch struct {
	buf strings.Builder
}

func newOutputBatch() *outputBatch {
	return &outputBatch{}
}

func (b *outputBatch) isEmpty() bool {
	return b.buf.Len() == 0
}

// full 判断缓冲是否达到立即刷新阈值。
func (b *outputBatch) full() bool {
	return b.buf.Len() >= flushSize
}

func (b *outputBatch) push(text string) {
	b.buf.WriteString(text)
}

// take 取出全部已缓冲文本并重置批次。
func (b *outputBatch) take() string {
	s := b.buf.String()
	b.buf.Reset()
	return s
}

// coalesceOutput 从 in 读取输出块并合批推送给 emit，直到 in 关闭或 done 触发
// （两种情况都会先推送剩余内容）。规则：
//   - 已就绪的块先并入本批；达到 flushSize 立即推送；
//   - 距上次推送已超过 outputCoalesceWindow（空闲后第一块）立即推送；
//   - 否则等到上次推送后满一个窗口再推送，期间到达的块继续并入。
func coalesceOutput[T any](in <-chan T, done <-chan struct{}, push func(*outputBatch, T), emit func(string), window time.Duration) {
	batch := newOutputBatch()
	var lastFlush time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var timerC <-chan time.Time
	flush := func() {
		if timerC != nil {
			timer.Stop()
			timerC = nil
		}
		if !batch.isEmpty() {
			emit(batch.take())
			lastFlush = time.Now()
		}
	}
	defer timer.Stop()
	for {
		select {
		case item, ok := <-in:
			if !ok {
				flush()
				return
			}
			push(batch, item)
		drain:
			for !batch.full() {
				select {
				case item, ok := <-in:
					if !ok {
						flush()
						return
					}
					push(batch, item)
				default:
					break drain
				}
			}
			if batch.isEmpty() || timerC != nil && !batch.full() {
				continue
			}
			wait := window - time.Since(lastFlush)
			if batch.full() || wait <= 0 {
				flush()
				continue
			}
			timer.Reset(wait)
			timerC = timer.C
		case <-timerC:
			timerC = nil
			flush()
		case <-done:
			flush()
			return
		}
	}
}
