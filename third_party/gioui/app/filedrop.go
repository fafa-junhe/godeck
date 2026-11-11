// SPDX-License-Identifier: Unlicense OR MIT

// GoDeck local patch: native OS file drops (Windows WM_DROPFILES and Wayland
// text/uri-list drag-and-drop). Not part of upstream Gio.

package app

import "sync"

var fileDropHandlers sync.Map // *Window -> func([]string)

// SetFileDropHandler registers a function called with the absolute paths of
// files and folders dropped onto the window from the OS file manager. The
// handler runs on the window's event thread and must return quickly. Pass nil
// to stop accepting drops. Supported on Windows and Wayland.
func (w *Window) SetFileDropHandler(h func(paths []string)) {
	if h == nil {
		fileDropHandlers.Delete(w)
		return
	}
	fileDropHandlers.Store(w, h)
}

func (c *callbacks) dropFiles(paths []string) {
	if c == nil || c.w == nil || len(paths) == 0 {
		return
	}
	if h, ok := fileDropHandlers.Load(c.w); ok {
		h.(func([]string))(paths)
	}
}
