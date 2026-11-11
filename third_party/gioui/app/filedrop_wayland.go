// SPDX-License-Identifier: Unlicense OR MIT

//go:build ((linux && !android) || freebsd) && !nowayland
// +build linux,!android freebsd
// +build !nowayland

// GoDeck local patch: accept text/uri-list drops from the desktop file manager.

package app

/*
#cgo freebsd CFLAGS: -I/usr/local/include

#include <stdlib.h>
#include <wayland-client.h>
*/
import "C"

import (
	"io"
	"net/url"
	"os"
	"strings"
	"time"
	"unsafe"
)

const uriListMime = "text/uri-list"

// dragEnter accepts the offer when it carries file URIs and the window has a
// drop handler; otherwise it tells the source the drop would be refused.
func (s *wlSeat) dragEnter(serial C.uint32_t, w *window, id *C.struct_wl_data_offer) {
	s.dragRelease()
	if id == nil {
		return
	}
	accept := false
	if w != nil && w.w != nil {
		if _, ok := fileDropHandlers.Load(w.w.w); ok {
			for _, m := range s.offers[id] {
				if m == uriListMime {
					accept = true
					break
				}
			}
		}
	}
	if !accept {
		C.wl_data_offer_accept(id, serial, nil)
		return
	}
	s.dragOffer = id
	s.dragWindow = w
	mime := C.CString(uriListMime)
	C.wl_data_offer_accept(id, serial, mime)
	C.free(unsafe.Pointer(mime))
	C.wl_data_offer_set_actions(id, C.WL_DATA_DEVICE_MANAGER_DND_ACTION_COPY, C.WL_DATA_DEVICE_MANAGER_DND_ACTION_COPY)
}

// dragRelease destroys the pending drag offer, if any.
func (s *wlSeat) dragRelease() {
	o := s.dragOffer
	s.dragOffer = nil
	s.dragWindow = nil
	if o == nil || o == s.clipboard {
		return
	}
	delete(s.offers, o)
	callbackDelete(unsafe.Pointer(o))
	C.wl_data_offer_destroy(o)
}

// dragDrop reads the dropped URI list. The read happens on the display thread
// with a short deadline; the source is another process, so it cannot deadlock.
func (s *wlSeat) dragDrop() {
	o, w := s.dragOffer, s.dragWindow
	if o == nil || w == nil {
		s.dragRelease()
		return
	}
	r, wp, err := os.Pipe()
	if err != nil {
		s.dragRelease()
		return
	}
	mime := C.CString(uriListMime)
	C.wl_data_offer_receive(o, mime, C.int(wp.Fd()))
	C.free(unsafe.Pointer(mime))
	C.wl_display_flush(s.disp.disp)
	wp.Close() // wl_data_offer_receive dup'ed the write end on flush

	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	r.Close()

	C.wl_data_offer_finish(o)
	s.dragRelease()

	if paths := parseURIList(string(data)); len(paths) > 0 {
		w.w.dropFiles(paths)
	}
}

// parseURIList extracts local paths from an RFC 2483 text/uri-list.
func parseURIList(list string) []string {
	var paths []string
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.Path == "" {
			continue
		}
		paths = append(paths, u.Path)
	}
	return paths
}
