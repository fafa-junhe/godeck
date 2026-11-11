package desktop

import (
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"godeck/native/client"
	"godeck/native/rtc"
)

func (u *UI) resetTouchState() {
	u.activeTouches = nil
	u.primaryTouch = 0
	u.hasPrimaryTouch = false
	u.touchButtonDown = false
	u.twoFingerActive = false
	u.twoFingerMoved = false
}

func (u *UI) stopInput() {
	if u.pump != nil {
		u.pump.Stop()
		u.pump = nil
	}
	u.operating = false
	u.mouseButtons = 0
	u.resetTouchState()
	u.pressed = map[key.Name]int32{}
}

func (u *UI) startInput() {
	if u.pump != nil {
		u.pump.Stop()
	}
	u.pump = client.NewInputPump(u.m.controller)
	u.operating = true
	u.mouseButtons = 0
	u.resetTouchState()
	u.pressed = map[key.Name]int32{}
}

func (u *UI) handleInputEvents(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(
			pointer.Filter{
				Target:  &u.surface,
				Kinds:   pointer.Press | pointer.Release | pointer.Move | pointer.Drag | pointer.Scroll | pointer.Cancel,
				ScrollY: pointer.ScrollRange{Min: -1000, Max: 1000},
			},
			key.Filter{
				Focus:    &u.surface,
				Optional: key.ModCtrl | key.ModShift | key.ModAlt | key.ModSuper,
			},
			key.FocusFilter{Target: &u.surface},
		)
		if !ok {
			return
		}

		// Global F2 toggle for statistics overlay
		if ke, ok := ev.(key.Event); ok && ke.Name == key.NameF2 && ke.State == key.Press {
			u.m.mu.Lock()
			u.m.showStats = !u.m.showStats
			u.m.mu.Unlock()
			continue
		}

		if !u.operating || u.pump == nil {
			if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Press {
				c := u.m.controller
				if c != nil && c.RemoteControlAllowed() {
					u.startInput()
					gtx.Execute(key.FocusCmd{Tag: &u.surface})
				}
			}
			continue
		}

		switch e := ev.(type) {
		case key.FocusEvent:
			if !e.Focus {
				u.stopInput()
			}
		case key.Event:
			// Local Esc exits operating mode and releases focus
			if e.Name == key.NameEscape && e.State == key.Press {
				u.stopInput()
				continue
			}
			code, exists := u.pressed[e.Name]
			if e.State == key.Press {
				if exists {
					continue
				}
				code = evdevKey(e.Name)
				if code == 0 {
					continue
				}
				u.pressed[e.Name] = code
			} else {
				if !exists {
					continue
				}
				delete(u.pressed, e.Name)
			}
			if u.pump.Send(rtc.Input{Type: "key", Code: code, Down: e.State == key.Press}) != nil {
				u.stopInput()
			}
		case pointer.Event:
			if e.Source == pointer.Touch {
				u.handleTouchEvent(gtx, e)
				continue
			}
			if e.Source != pointer.Mouse {
				continue
			}
			if e.Kind == pointer.Cancel {
				u.stopInput()
				continue
			}
			x, y, inside := u.mapSurfacePoint(e.Position)
			if !inside && e.Kind != pointer.Release && u.mouseButtons == 0 {
				continue
			}

			if e.Kind == pointer.Press && inside {
				gtx.Execute(key.FocusCmd{Tag: &u.surface})
			}
			if inside && (e.Kind == pointer.Move || e.Kind == pointer.Drag || e.Kind == pointer.Press) {
				_ = u.pump.Send(rtc.Input{Type: "move", X: x, Y: y})
			}
			if inside && e.Kind == pointer.Scroll {
				_ = u.pump.Send(rtc.Input{Type: "wheel", Axis: float64(e.Scroll.Y)})
			}

			// Mouse buttons
			for _, b := range []struct {
				mask pointer.Buttons
				code int32
			}{
				{pointer.ButtonPrimary, 272},
				{pointer.ButtonSecondary, 273},
				{pointer.ButtonTertiary, 274},
			} {
				was := u.mouseButtons&b.mask != 0
				now := e.Buttons&b.mask != 0
				if was && !now {
					if u.pump.Send(rtc.Input{Type: "button", Code: b.code, Down: false}) != nil {
						u.stopInput()
						break
					}
				} else if !was && now && inside {
					if u.pump.Send(rtc.Input{Type: "button", Code: b.code, Down: true}) != nil {
						u.stopInput()
						break
					}
				}
			}
			if u.operating {
				u.mouseButtons = e.Buttons
			}
		}
	}
}

func (u *UI) mapSurfacePoint(pos f32.Point) (x, y float64, inside bool) {
	u.m.mu.Lock()
	frame := u.m.frame
	u.m.mu.Unlock()
	if frame == nil || u.screenSize.X <= 0 || u.screenSize.Y <= 0 {
		return 0, 0, false
	}

	// Proportional Letterbox / Pillarbox coordinate calculation
	fW := float64(frame.Rect.Dx())
	fH := float64(frame.Rect.Dy())
	if fW <= 0 || fH <= 0 {
		return 0, 0, false
	}
	scale := math.Min(float64(u.screenSize.X)/fW, float64(u.screenSize.Y)/fH)
	renderedW := fW * scale
	renderedH := fH * scale
	offsetX := (float64(u.screenSize.X) - renderedW) / 2
	offsetY := (float64(u.screenSize.Y) - renderedH) / 2

	x = (float64(pos.X) - offsetX) / renderedW
	y = (float64(pos.Y) - offsetY) / renderedH
	return x, y, x >= 0 && x <= 1 && y >= 0 && y <= 1
}

func (u *UI) touchStats() (avgY float32, center f32.Point) {
	if len(u.activeTouches) == 0 {
		return 0, f32.Point{}
	}
	var sumX, sumY float32
	for _, pt := range u.activeTouches {
		sumX += pt.X
		sumY += pt.Y
	}
	n := float32(len(u.activeTouches))
	return sumY / n, f32.Point{X: sumX / n, Y: sumY / n}
}

func (u *UI) releaseTouchButton() {
	if u.touchButtonDown {
		if u.pump != nil && u.pump.Send(rtc.Input{Type: "button", Code: 272, Down: false}) != nil {
			u.stopInput()
			return
		}
		u.touchButtonDown = false
	}
}

func (u *UI) handleTouchEvent(gtx layout.Context, e pointer.Event) {
	if e.Kind == pointer.Cancel {
		u.releaseTouchButton()
		u.resetTouchState()
		return
	}
	if u.activeTouches == nil {
		u.activeTouches = make(map[pointer.ID]f32.Point, 4)
	}

	switch e.Kind {
	case pointer.Press:
		u.activeTouches[e.PointerID] = e.Position
		if len(u.activeTouches) == 1 {
			u.primaryTouch = e.PointerID
			u.hasPrimaryTouch = true
			u.twoFingerActive = false
			u.twoFingerMoved = false
			x, y, inside := u.mapSurfacePoint(e.Position)
			if inside {
				gtx.Execute(key.FocusCmd{Tag: &u.surface})
				_ = u.pump.Send(rtc.Input{Type: "move", X: x, Y: y})
				if u.pump.Send(rtc.Input{Type: "button", Code: 272, Down: true}) != nil {
					u.stopInput()
					return
				}
				u.touchButtonDown = true
			}
		} else if len(u.activeTouches) >= 2 && !u.twoFingerActive {
			u.releaseTouchButton()
			if !u.operating {
				return
			}
			avgY, center := u.touchStats()
			u.twoFingerActive = true
			u.twoFingerMoved = false
			u.twoFingerStart = time.Now()
			u.twoFingerLastY = avgY
			u.twoFingerCenter = center
		}

	case pointer.Drag, pointer.Move:
		if _, tracked := u.activeTouches[e.PointerID]; !tracked {
			return
		}
		u.activeTouches[e.PointerID] = e.Position
		if u.twoFingerActive {
			avgY, center := u.touchStats()
			dy := u.twoFingerLastY - avgY
			if !u.twoFingerMoved && math.Abs(float64(dy)) >= float64(gtx.Dp(Dp(4))) {
				u.twoFingerMoved = true
			}
			if u.twoFingerMoved && math.Abs(float64(dy)) >= 1.0 {
				step := float32(gtx.Dp(Dp(18)))
				if step < 12 {
					step = 12
				}
				axis := float64(dy / step)
				if axis > 20 {
					axis = 20
				} else if axis < -20 {
					axis = -20
				}
				_ = u.pump.Send(rtc.Input{Type: "wheel", Axis: axis})
				u.twoFingerLastY = avgY
				u.twoFingerCenter = center
			}
			return
		}
		if u.hasPrimaryTouch && e.PointerID == u.primaryTouch {
			x, y, inside := u.mapSurfacePoint(e.Position)
			if inside {
				_ = u.pump.Send(rtc.Input{Type: "move", X: x, Y: y})
			}
		}

	case pointer.Release:
		if _, tracked := u.activeTouches[e.PointerID]; !tracked {
			return
		}
		u.activeTouches[e.PointerID] = e.Position
		delete(u.activeTouches, e.PointerID)

		if u.hasPrimaryTouch && e.PointerID == u.primaryTouch {
			u.releaseTouchButton()
			u.hasPrimaryTouch = false
		}
		if len(u.activeTouches) == 0 {
			if u.twoFingerActive && !u.twoFingerMoved && time.Since(u.twoFingerStart) < 350*time.Millisecond {
				if x, y, inside := u.mapSurfacePoint(u.twoFingerCenter); inside && u.pump != nil {
					_ = u.pump.Send(rtc.Input{Type: "move", X: x, Y: y})
					_ = u.pump.Send(rtc.Input{Type: "button", Code: 273, Down: true})
					_ = u.pump.Send(rtc.Input{Type: "button", Code: 273, Down: false})
				}
			}
			u.twoFingerActive = false
			u.twoFingerMoved = false
		} else if u.twoFingerActive {
			u.twoFingerLastY, u.twoFingerCenter = u.touchStats()
		}
	}
}

func evdevKey(n key.Name) int32 {
	m := map[key.Name]int32{
		"A": 30, "B": 48, "C": 46, "D": 32, "E": 18, "F": 33, "G": 34, "H": 35,
		"I": 23, "J": 36, "K": 37, "L": 38, "M": 50, "N": 49, "O": 24, "P": 25,
		"Q": 16, "R": 19, "S": 31, "T": 20, "U": 22, "V": 47, "W": 17, "X": 45,
		"Y": 21, "Z": 44,
		"1": 2, "2": 3, "3": 4, "4": 5, "5": 6, "6": 7, "7": 8, "8": 9, "9": 10, "0": 11,
		"-": 12, "=": 13, "+": 13, "[": 26, "]": 27, ";": 39, "'": 40, "`": 41, "\\": 43,
		",": 51, ".": 52, "/": 53, "*": 55,
		key.NameReturn: 28, key.NameEnter: 28, key.NameSpace: 57, key.NameTab: 15,
		key.NameDeleteBackward: 14, key.NameDeleteForward: 111,
		key.NameLeftArrow: 105, key.NameRightArrow: 106, key.NameUpArrow: 103, key.NameDownArrow: 108,
		key.NameCtrl: 29, key.NameShift: 42, key.NameAlt: 56, key.NameSuper: 125, key.NameCommand: 125,
		key.NameHome: 102, key.NameEnd: 107, key.NamePageUp: 104, key.NamePageDown: 109,
		key.NameF1: 59, key.NameF2: 60, key.NameF3: 61, key.NameF4: 62,
		key.NameF5: 63, key.NameF6: 64, key.NameF7: 65, key.NameF8: 66,
		key.NameF9: 67, key.NameF10: 68, key.NameF11: 87, key.NameF12: 88,
	}
	return m[n]
}
