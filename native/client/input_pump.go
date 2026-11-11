package client

import (
	"errors"
	"sync"

	"godeck/native/rtc"
	"godeck/native/session"
)

// InputPump preserves key/button ordering while coalescing pointer moves.
type InputPump struct {
	c        *Controller
	viewer   *session.Viewer
	reliable chan rtc.Input
	motion   chan rtc.Input
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func NewInputPump(c *Controller) *InputPump {
	c.mu.Lock()
	viewer := c.outgoing
	c.mu.Unlock()
	p := &InputPump{
		c:        c,
		viewer:   viewer,
		reliable: make(chan rtc.Input, 128),
		motion:   make(chan rtc.Input, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *InputPump) Send(e rtc.Input) error {
	select {
	case <-p.stop:
		return errors.New("操作已停止")
	default:
	}
	if e.Type == "move" {
		select {
		case <-p.motion:
		default:
		}
		select {
		case p.motion <- e:
		default:
		}
		return nil
	}
	select {
	case p.reliable <- e:
		return nil
	case <-p.stop:
		return errors.New("操作已停止")
	default:
		return errors.New("输入队列已满，操作已停止")
	}
}

func (p *InputPump) run() {
	defer close(p.done)
	keys := map[int32]bool{}
	buttons := map[int32]bool{}
	defer func() {
		_ = p.c.inputFor(p.viewer, rtc.Input{Type: "release"})
		for k := range keys {
			_ = p.c.inputFor(p.viewer, rtc.Input{Type: "key", Code: k})
		}
		for k := range buttons {
			_ = p.c.inputFor(p.viewer, rtc.Input{Type: "button", Code: k})
		}
	}()

	send := func(e rtc.Input) {
		select {
		case <-p.stop:
			return
		default:
		}
		if p.c.inputFor(p.viewer, e) != nil {
			return
		}
		if e.Type == "key" {
			if e.Down {
				keys[e.Code] = true
			} else {
				delete(keys, e.Code)
			}
		}
		if e.Type == "button" {
			if e.Down {
				buttons[e.Code] = true
			} else {
				delete(buttons, e.Code)
			}
		}
	}

	for {
		select {
		case <-p.stop:
			return
		default:
		}
		select {
		case <-p.stop:
			return
		case e := <-p.reliable:
			send(e)
		default:
			select {
			case <-p.stop:
				return
			case e := <-p.reliable:
				send(e)
			case e := <-p.motion:
				send(e)
			}
		}
	}
}

func (p *InputPump) Stop() {
	p.once.Do(func() {
		close(p.stop)
	})
}

func (p *InputPump) Done() <-chan struct{} {
	return p.done
}
