//go:build darwin

package macos

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

const (
	appServicesPath    = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	coreGraphicsPath   = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	appKitPath         = "/System/Library/Frameworks/AppKit.framework/AppKit"
	libObjCPath        = "/usr/lib/libobjc.A.dylib"

	kCFStringEncodingUTF8 = 0x08000100
	kCFNumberSInt64Type   = 4
	kCFNumberDoubleType   = 13

	kAXValueCGPointType = 1
	kAXValueCGSizeType  = 2

	kCGEventSourceStateCombinedSessionState = 0
	kCGAnyInputEventType                    = ^uint32(0)
	kCGHIDEventTap                          = 0
	kCGScrollEventUnitLine                  = 1
	kCGMouseEventClickState                 = 1

	kCGWindowListOptionOnScreenOnly     = 1
	kCGWindowListExcludeDesktopElements = 16
)

type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

var (
	loadOnce sync.Once
	loadErr  error

	axIsProcessTrusted             func() bool
	cgPreflightScreenCaptureAccess func() bool

	axUIElementCreateApplication           func(pid int32) uintptr
	axUIElementCopyAttributeValue          func(el, attr uintptr, out *uintptr) int32
	axUIElementCopyAttributeValues         func(el, attr uintptr, index, max int, out *uintptr) int32
	axUIElementGetAttributeValueCount      func(el, attr uintptr, out *int) int32
	axUIElementCopyMultipleAttributeValues func(el, attrs uintptr, options uint32, out *uintptr) int32
	axUIElementSetAttributeValue           func(el, attr, value uintptr) int32
	axUIElementIsAttributeSettable         func(el, attr uintptr, out *uint8) int32
	axUIElementPerformAction               func(el, action uintptr) int32
	axUIElementCopyActionNames             func(el uintptr, out *uintptr) int32
	axUIElementSetMessagingTimeout         func(el uintptr, seconds float32) int32
	axUIElementGetTypeID                   func() uintptr
	axValueGetType                         func(v uintptr) uint32
	axValueGetValue                        func(v uintptr, typ uint32, ptr unsafe.Pointer) bool
	axValueGetTypeID                       func() uintptr
	axGetWindow                            func(el uintptr, out *uint32) int32 // private; nil when absent

	cfRelease                         func(v uintptr)
	cfRetain                          func(v uintptr) uintptr
	cfGetTypeID                       func(v uintptr) uintptr
	cfStringGetTypeID                 func() uintptr
	cfBooleanGetTypeID                func() uintptr
	cfNumberGetTypeID                 func() uintptr
	cfArrayGetCount                   func(a uintptr) int
	cfArrayGetValueAtIndex            func(a uintptr, i int) uintptr
	cfArrayCreate                     func(alloc uintptr, values *uintptr, n int, callbacks uintptr) uintptr
	cfStringCreateWithCString         func(alloc uintptr, s *byte, enc uint32) uintptr
	cfStringGetLength                 func(s uintptr) int
	cfStringGetMaximumSizeForEncoding func(n int, enc uint32) int
	cfStringGetCString                func(s uintptr, buf *byte, size int, enc uint32) bool
	cfBooleanGetValue                 func(b uintptr) bool
	cfNumberGetValue                  func(n uintptr, typ int, out unsafe.Pointer) bool
	cfDictionaryGetValue              func(d, key uintptr) uintptr

	cgWindowListCopyWindowInfo             func(option, relativeTo uint32) uintptr
	cgRectMakeWithDictionaryRepresentation func(d uintptr, r *cgRect) bool
	cgGetActiveDisplayList                 func(max uint32, displays *uint32, count *uint32) int32
	cgDisplayBounds                        func(d uint32) cgRect
	cgEventSourceSecondsSinceLastEventType func(state int32, typ uint32) float64
	cgEventCreateKeyboardEvent             func(src uintptr, code uint16, down bool) uintptr
	cgEventSetFlags                        func(ev uintptr, flags uint64)
	cgEventKeyboardSetUnicodeString        func(ev uintptr, n uint, s *uint16)
	cgEventPostToPid                       func(pid int32, ev uintptr)
	cgEventCreateMouseEvent                func(src uintptr, typ uint32, pt cgPoint, button uint32) uintptr
	cgEventSetIntegerValueField            func(ev uintptr, field uint32, v int64)
	cgEventPost                            func(tap uint32, ev uintptr)
	cgEventCreateScrollWheelEvent2         func(src uintptr, units, count uint32, w1, w2, w3 int32) uintptr

	kCFTypeArrayCallBacks uintptr // address of the struct
	kCFBooleanTrue        uintptr
	kCFBooleanFalse       uintptr

	typeString, typeBool, typeNumber, typeAXValue, typeElement uintptr

	poolPush, poolPop uintptr

	// the attributes read for every node, as a CFArray, and their order
	readAttrs uintptr
	strCache  sync.Map // string → CFStringRef (kept for the process)
)

var nodeAttrs = []string{
	"AXRole", "AXSubrole", "AXTitle", "AXDescription", "AXLabel", "AXHelp", "AXPlaceholderValue",
	"AXValue", "AXPosition", "AXSize", "AXEnabled", "AXFocused", "AXSelected", "AXMain",
	"AXMenuItemCmdChar", "AXMenuItemCmdModifiers",
}

// valueRoles may have a settable AXValue (checked per node: an extra call).
var valueRoles = map[string]bool{
	"AXTextField": true, "AXTextArea": true, "AXComboBox": true, "AXSlider": true, "AXCheckBox": true,
	"AXDateField": true, "AXTimeField": true, "AXIncrementor": true, "AXColorWell": true, "AXRadioButton": true,
	"AXScrollBar": true, "AXSearchField": true,
}

func load() error {
	loadOnce.Do(func() { loadErr = doLoad() })
	return loadErr
}

func doLoad() error {
	libs := map[string]uintptr{}
	for _, p := range []string{appServicesPath, coreGraphicsPath, coreFoundationPath, appKitPath, libObjCPath} {
		h, err := purego.Dlopen(p, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("computer-macos: %w", err)
		}
		libs[p] = h
	}
	ax, cg, cf := libs[appServicesPath], libs[coreGraphicsPath], libs[coreFoundationPath]
	for _, f := range []struct {
		fn   any
		lib  uintptr
		name string
	}{
		{&axIsProcessTrusted, ax, "AXIsProcessTrusted"},
		{&cgPreflightScreenCaptureAccess, cg, "CGPreflightScreenCaptureAccess"},
		{&axUIElementCreateApplication, ax, "AXUIElementCreateApplication"},
		{&axUIElementCopyAttributeValue, ax, "AXUIElementCopyAttributeValue"},
		{&axUIElementCopyAttributeValues, ax, "AXUIElementCopyAttributeValues"},
		{&axUIElementGetAttributeValueCount, ax, "AXUIElementGetAttributeValueCount"},
		{&axUIElementCopyMultipleAttributeValues, ax, "AXUIElementCopyMultipleAttributeValues"},
		{&axUIElementSetAttributeValue, ax, "AXUIElementSetAttributeValue"},
		{&axUIElementIsAttributeSettable, ax, "AXUIElementIsAttributeSettable"},
		{&axUIElementPerformAction, ax, "AXUIElementPerformAction"},
		{&axUIElementCopyActionNames, ax, "AXUIElementCopyActionNames"},
		{&axUIElementSetMessagingTimeout, ax, "AXUIElementSetMessagingTimeout"},
		{&axUIElementGetTypeID, ax, "AXUIElementGetTypeID"},
		{&axValueGetType, ax, "AXValueGetType"},
		{&axValueGetValue, ax, "AXValueGetValue"},
		{&axValueGetTypeID, ax, "AXValueGetTypeID"},
		{&cfRelease, cf, "CFRelease"},
		{&cfRetain, cf, "CFRetain"},
		{&cfGetTypeID, cf, "CFGetTypeID"},
		{&cfStringGetTypeID, cf, "CFStringGetTypeID"},
		{&cfBooleanGetTypeID, cf, "CFBooleanGetTypeID"},
		{&cfNumberGetTypeID, cf, "CFNumberGetTypeID"},
		{&cfArrayGetCount, cf, "CFArrayGetCount"},
		{&cfArrayGetValueAtIndex, cf, "CFArrayGetValueAtIndex"},
		{&cfArrayCreate, cf, "CFArrayCreate"},
		{&cfStringCreateWithCString, cf, "CFStringCreateWithCString"},
		{&cfStringGetLength, cf, "CFStringGetLength"},
		{&cfStringGetMaximumSizeForEncoding, cf, "CFStringGetMaximumSizeForEncoding"},
		{&cfStringGetCString, cf, "CFStringGetCString"},
		{&cfBooleanGetValue, cf, "CFBooleanGetValue"},
		{&cfNumberGetValue, cf, "CFNumberGetValue"},
		{&cfDictionaryGetValue, cf, "CFDictionaryGetValue"},
		{&cgWindowListCopyWindowInfo, cg, "CGWindowListCopyWindowInfo"},
		{&cgRectMakeWithDictionaryRepresentation, cg, "CGRectMakeWithDictionaryRepresentation"},
		{&cgGetActiveDisplayList, cg, "CGGetActiveDisplayList"},
		{&cgDisplayBounds, cg, "CGDisplayBounds"},
		{&cgEventSourceSecondsSinceLastEventType, cg, "CGEventSourceSecondsSinceLastEventType"},
		{&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent"},
		{&cgEventSetFlags, cg, "CGEventSetFlags"},
		{&cgEventKeyboardSetUnicodeString, cg, "CGEventKeyboardSetUnicodeString"},
		{&cgEventPostToPid, cg, "CGEventPostToPid"},
		{&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent"},
		{&cgEventSetIntegerValueField, cg, "CGEventSetIntegerValueField"},
		{&cgEventPost, cg, "CGEventPost"},
		{&cgEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2"},
	} {
		p, err := purego.Dlsym(f.lib, f.name)
		if err != nil {
			return fmt.Errorf("computer-macos: missing %s: %w", f.name, computer.ErrUnsupported)
		}
		purego.RegisterFunc(f.fn, p)
	}
	if p, err := purego.Dlsym(ax, "_AXUIElementGetWindow"); err == nil && p != 0 {
		purego.RegisterFunc(&axGetWindow, p)
	}
	sym := func(name string) (uintptr, error) {
		p, err := purego.Dlsym(cf, name)
		if err != nil || p == 0 {
			return 0, fmt.Errorf("computer-macos: missing %s: %w", name, computer.ErrUnsupported)
		}
		return p, nil
	}
	var err error
	if kCFTypeArrayCallBacks, err = sym("kCFTypeArrayCallBacks"); err != nil {
		return err
	}
	t, err := sym("kCFBooleanTrue")
	if err != nil {
		return err
	}
	f, err := sym("kCFBooleanFalse")
	if err != nil {
		return err
	}
	kCFBooleanTrue = **(**uintptr)(unsafe.Pointer(&t))
	kCFBooleanFalse = **(**uintptr)(unsafe.Pointer(&f))
	poolPush, _ = purego.Dlsym(libs[libObjCPath], "objc_autoreleasePoolPush")
	poolPop, _ = purego.Dlsym(libs[libObjCPath], "objc_autoreleasePoolPop")
	if poolPush == 0 || poolPop == 0 {
		return fmt.Errorf("computer-macos: no autorelease pools: %w", computer.ErrUnsupported)
	}

	typeString, typeBool, typeNumber = cfStringGetTypeID(), cfBooleanGetTypeID(), cfNumberGetTypeID()
	typeAXValue, typeElement = axValueGetTypeID(), axUIElementGetTypeID()

	vals := make([]uintptr, len(nodeAttrs))
	for i, a := range nodeAttrs {
		vals[i] = cfstr(a)
	}
	readAttrs = cfArrayCreate(0, &vals[0], len(vals), kCFTypeArrayCallBacks)
	return nil
}

// cfstr returns a cached CFString (attribute and action names; never
// released).
func cfstr(s string) uintptr {
	if v, ok := strCache.Load(s); ok {
		return v.(uintptr)
	}
	v := newCFString(s)
	if old, loaded := strCache.LoadOrStore(s, v); loaded {
		cfRelease(v)
		return old.(uintptr)
	}
	return v
}

// newCFString returns an owned CFString.
func newCFString(s string) uintptr {
	b := append([]byte(s), 0)
	for i := 0; i < len(b)-1; i++ {
		if b[i] == 0 {
			b[i] = ' ' // NULs would end the C string
		}
	}
	return cfStringCreateWithCString(0, &b[0], kCFStringEncodingUTF8)
}

func goString(s uintptr) string {
	if s == 0 || cfGetTypeID(s) != typeString {
		return ""
	}
	n := cfStringGetLength(s)
	if n == 0 {
		return ""
	}
	size := cfStringGetMaximumSizeForEncoding(n, kCFStringEncodingUTF8) + 1
	buf := make([]byte, size)
	if !cfStringGetCString(s, &buf[0], size, kCFStringEncodingUTF8) {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func cfNumber(v uintptr) (float64, bool) {
	var f float64
	if cfNumberGetValue(v, kCFNumberDoubleType, unsafe.Pointer(&f)) {
		return f, true
	}
	var i int64
	if cfNumberGetValue(v, kCFNumberSInt64Type, unsafe.Pointer(&i)) {
		return float64(i), true
	}
	return 0, false
}

// axErr maps AXError codes.
func axErr(code int32) error {
	switch code {
	case 0:
		return nil
	case -25202: // kAXErrorInvalidUIElement
		return errInvalid
	case -25211: // kAXErrorAPIDisabled
		return errAPIDisabled
	case -25212, -25205: // kAXErrorNoValue, kAXErrorAttributeUnsupported
		return errNoValue
	case -25204:
		return errors.New("the app did not answer in time (busy or hung)")
	case -25206:
		return errors.New("the element does not support that action")
	case -25201:
		return errors.New("illegal argument")
	case -25200:
		return errors.New("the app refused (accessibility failure)")
	case -25208:
		return errors.New("the app does not implement accessibility for that")
	}
	return fmt.Errorf("accessibility error %d", code)
}

// withPool runs fn in an autorelease pool, on one thread.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, _, _ := purego.SyscallN(poolPush)
	defer purego.SyscallN(poolPop, p)
	fn()
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func nsGoString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := objc.Send[unsafe.Pointer](s, sel("UTF8String"))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

// darwinPlatform is the real platform.
type darwinPlatform struct{}

func newPlatform() (platform, error) {
	if err := load(); err != nil {
		return nil, err
	}
	return darwinPlatform{}, nil
}

func (darwinPlatform) Trusted() bool       { return axIsProcessTrusted() }
func (darwinPlatform) ScreenCapture() bool { return cgPreflightScreenCaptureAccess() }

func (darwinPlatform) IdleSeconds() float64 {
	return cgEventSourceSecondsSinceLastEventType(kCGEventSourceStateCombinedSessionState, kCGAnyInputEventType)
}

// Apps lists the regular apps. NSWorkspace's list is updated by
// notifications on the main run loop; where that loop does not run (a
// headless crewd, tests) it goes stale, so apps that own windows the
// window server lists are looked up by pid too, and dead pids are dropped.
func (darwinPlatform) Apps() ([]computer.App, error) {
	var out []computer.App
	seen := map[int]bool{}
	add := func(a objc.ID) {
		if a == 0 || objc.Send[int](a, sel("activationPolicy")) != 0 { // NSApplicationActivationPolicyRegular
			return
		}
		pid := int(objc.Send[int32](a, sel("processIdentifier")))
		if seen[pid] || objc.Send[bool](a, sel("isTerminated")) || !alive(pid) {
			return
		}
		seen[pid] = true
		out = append(out, computer.App{
			ID:     nsGoString(a.Send(sel("bundleIdentifier"))),
			Name:   nsGoString(a.Send(sel("localizedName"))),
			PID:    pid,
			Active: objc.Send[bool](a, sel("isActive")),
			Hidden: objc.Send[bool](a, sel("isHidden")),
		})
	}
	owners := windowOwners()
	withPool(func() {
		ws := objc.ID(objc.GetClass("NSWorkspace")).Send(sel("sharedWorkspace"))
		arr := ws.Send(sel("runningApplications"))
		n := objc.Send[uint](arr, sel("count"))
		for i := uint(0); i < n; i++ {
			add(arr.Send(sel("objectAtIndex:"), i))
		}
		cls := objc.ID(objc.GetClass("NSRunningApplication"))
		for _, pid := range owners {
			if !seen[pid] {
				add(cls.Send(sel("runningApplicationWithProcessIdentifier:"), int32(pid)))
			}
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Active && !out[j].Active })
	return out, nil
}

// alive: the process exists (EPERM still means it does).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// windowOwners are the pids owning normal windows, in the window server's
// order, each once.
func windowOwners() []int {
	arr := cgWindowListCopyWindowInfo(kCGWindowListExcludeDesktopElements, 0)
	if arr == 0 {
		return nil
	}
	defer cfRelease(arr)
	var out []int
	seen := map[int]bool{}
	for i, n := 0, cfArrayGetCount(arr); i < n; i++ {
		d := cfArrayGetValueAtIndex(arr, i)
		if dictInt(d, "kCGWindowLayer") != 0 {
			continue
		}
		if pid := dictInt(d, "kCGWindowOwnerPID"); pid > 0 && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

func dictInt(d uintptr, key string) int {
	v := cfDictionaryGetValue(d, cfstr(key))
	if v == 0 || cfGetTypeID(v) != typeNumber {
		return 0
	}
	f, _ := cfNumber(v)
	return int(f)
}

func (darwinPlatform) AppElement(pid int) (ref, error) {
	el := axUIElementCreateApplication(int32(pid))
	if el == 0 {
		return 0, computer.ErrNoApp
	}
	return ref(el), nil
}

func (darwinPlatform) SetTimeout(r ref, seconds float64) {
	axUIElementSetMessagingTimeout(uintptr(r), float32(seconds))
}

func (darwinPlatform) Release(r ref) {
	if r != 0 {
		cfRelease(uintptr(r))
	}
}

func (darwinPlatform) Attr(r ref, name string) (ref, error) {
	var v uintptr
	if err := axErr(axUIElementCopyAttributeValue(uintptr(r), cfstr(name), &v)); err != nil {
		return 0, err
	}
	if v == 0 {
		return 0, errNoValue
	}
	if cfGetTypeID(v) != typeElement {
		cfRelease(v)
		return 0, errNoValue
	}
	return ref(v), nil
}

func (darwinPlatform) Elements(r ref, name string, max int) ([]ref, error) {
	refs, _, err := copyElements(uintptr(r), name, max)
	return refs, err
}

func (darwinPlatform) Children(r ref, max int) ([]ref, int, error) {
	return copyElements(uintptr(r), "AXChildren", max)
}

// copyElements reads up to max elements of an array attribute, retained.
func copyElements(el uintptr, name string, max int) ([]ref, int, error) {
	attr := cfstr(name)
	var total int
	if err := axErr(axUIElementGetAttributeValueCount(el, attr, &total)); err != nil {
		if errors.Is(err, errNoValue) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if total == 0 || max <= 0 {
		return nil, total, nil
	}
	var arr uintptr
	if err := axErr(axUIElementCopyAttributeValues(el, attr, 0, min(total, max), &arr)); err != nil {
		return nil, total, err
	}
	if arr == 0 {
		return nil, total, nil
	}
	defer cfRelease(arr)
	n := cfArrayGetCount(arr)
	out := make([]ref, 0, n)
	for i := 0; i < n; i++ {
		v := cfArrayGetValueAtIndex(arr, i)
		if v != 0 && cfGetTypeID(v) == typeElement {
			out = append(out, ref(cfRetain(v)))
		}
	}
	return out, total, nil
}

func (darwinPlatform) Read(r ref) (rawNode, error) {
	el := uintptr(r)
	values := make([]uintptr, len(nodeAttrs))
	var arr uintptr
	err := axErr(axUIElementCopyMultipleAttributeValues(el, readAttrs, 0, &arr))
	switch {
	case err == nil && arr != 0:
		defer cfRelease(arr)
		for i, c := 0, cfArrayGetCount(arr); i < c && i < len(values); i++ {
			values[i] = cfArrayGetValueAtIndex(arr, i)
		}
	case errors.Is(err, errInvalid), errors.Is(err, errAPIDisabled):
		return rawNode{}, err
	default:
		// some apps refuse the batch: one attribute at a time
		for i, a := range nodeAttrs {
			var v uintptr
			if axUIElementCopyAttributeValue(el, cfstr(a), &v) == 0 && v != 0 {
				values[i] = v
				defer cfRelease(v)
			}
		}
	}
	var n rawNode
	got := 0
	for i := range values {
		v := values[i]
		if v == 0 {
			continue
		}
		t := cfGetTypeID(v)
		if t == typeAXValue && axValueGetType(v) == 5 { // kAXValueAXErrorType: missing
			continue
		}
		got++
		str := func() string {
			if t == typeString {
				return goString(v)
			}
			return ""
		}
		boolean := func() bool {
			switch t {
			case typeBool:
				return cfBooleanGetValue(v)
			case typeNumber:
				f, _ := cfNumber(v)
				return f != 0
			}
			return false
		}
		switch nodeAttrs[i] {
		case "AXRole":
			n.Role = str()
		case "AXSubrole":
			n.Subrole = str()
		case "AXTitle":
			n.Title = str()
		case "AXDescription":
			n.Description = str()
		case "AXLabel":
			n.Label = str()
		case "AXHelp":
			n.Help = str()
		case "AXPlaceholderValue":
			n.Placeholder = str()
		case "AXValue":
			switch t {
			case typeString:
				n.Value, n.HasValue = goString(v), true
			case typeNumber:
				f, _ := cfNumber(v)
				n.Value, n.HasValue = fmt.Sprintf("%g", f), true
			case typeBool:
				n.Value, n.HasValue = fmt.Sprint(cfBooleanGetValue(v)), true
			}
		case "AXPosition":
			var p cgPoint
			if t == typeAXValue && axValueGetValue(v, kAXValueCGPointType, unsafe.Pointer(&p)) {
				n.X, n.Y = p.X, p.Y
				n.HasFrame = true
			}
		case "AXSize":
			var s cgSize
			if t == typeAXValue && axValueGetValue(v, kAXValueCGSizeType, unsafe.Pointer(&s)) {
				n.W, n.H = s.W, s.H
			}
		case "AXEnabled":
			n.Enabled, n.HasEnabled = boolean(), true
		case "AXFocused":
			n.Focused = boolean()
		case "AXSelected":
			n.Selected = boolean()
		case "AXMain":
			n.Main = boolean()
		case "AXMenuItemCmdChar":
			n.CmdChar = str()
		case "AXMenuItemCmdModifiers":
			if t == typeNumber {
				f, _ := cfNumber(v)
				n.CmdMods = int(f)
			}
		}
	}
	if got == 0 {
		// a dead element answers nothing; ask once to get the real error
		var v uintptr
		if err := axErr(axUIElementCopyAttributeValue(el, cfstr("AXRole"), &v)); err != nil && !errors.Is(err, errNoValue) {
			return rawNode{}, err
		}
		if v != 0 {
			cfRelease(v)
		}
	}
	var names uintptr
	if axUIElementCopyActionNames(el, &names) == 0 && names != 0 {
		for i, c := 0, cfArrayGetCount(names); i < c; i++ {
			if s := goString(cfArrayGetValueAtIndex(names, i)); s != "" {
				n.Actions = append(n.Actions, s)
			}
		}
		cfRelease(names)
	}
	if valueRoles[n.Role] || valueRoles[n.Subrole] {
		var ok uint8
		if axUIElementIsAttributeSettable(el, cfstr("AXValue"), &ok) == 0 {
			n.Settable = ok != 0
		}
	}
	return n, nil
}

func (darwinPlatform) Bool(r ref, name string) (bool, error) {
	var v uintptr
	if err := axErr(axUIElementCopyAttributeValue(uintptr(r), cfstr(name), &v)); err != nil {
		return false, err
	}
	if v == 0 {
		return false, errNoValue
	}
	defer cfRelease(v)
	switch cfGetTypeID(v) {
	case typeBool:
		return cfBooleanGetValue(v), nil
	case typeNumber:
		f, _ := cfNumber(v)
		return f != 0, nil
	}
	return false, errNoValue
}

func (darwinPlatform) Perform(r ref, action string) error {
	return axErr(axUIElementPerformAction(uintptr(r), cfstr(action)))
}

func (darwinPlatform) SetString(r ref, attr, v string) error {
	s := newCFString(v)
	if s == 0 {
		return errors.New("cannot make a string of the value")
	}
	defer cfRelease(s)
	return axErr(axUIElementSetAttributeValue(uintptr(r), cfstr(attr), s))
}

func (darwinPlatform) SetBool(r ref, attr string, v bool) error {
	b := kCFBooleanFalse
	if v {
		b = kCFBooleanTrue
	}
	return axErr(axUIElementSetAttributeValue(uintptr(r), cfstr(attr), b))
}

func (d darwinPlatform) WindowNumber(win ref, pid int, frame computer.Rect) int {
	if axGetWindow != nil {
		var id uint32
		if axGetWindow(uintptr(win), &id) == 0 && id != 0 {
			return int(id)
		}
	}
	wins, err := d.Windows(pid)
	if err != nil {
		return 0
	}
	if w, ok := matchWindow(wins, frame); ok {
		return w.ID
	}
	return 0
}

func (darwinPlatform) Windows(pid int) ([]cgWindow, error) {
	arr := cgWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly|kCGWindowListExcludeDesktopElements, 0)
	if arr == 0 {
		return nil, errors.New("computer-macos: the window server listed no windows")
	}
	defer cfRelease(arr)
	var out []cgWindow
	for i, n := 0, cfArrayGetCount(arr); i < n; i++ {
		d := cfArrayGetValueAtIndex(arr, i)
		if dictInt(d, "kCGWindowOwnerPID") != pid {
			continue
		}
		w := cgWindow{ID: dictInt(d, "kCGWindowNumber"), PID: pid, Layer: dictInt(d, "kCGWindowLayer")}
		if b := cfDictionaryGetValue(d, cfstr("kCGWindowBounds")); b != 0 {
			var r cgRect
			if cgRectMakeWithDictionaryRepresentation(b, &r) {
				w.Frame = computer.Rect{X: r.Origin.X, Y: r.Origin.Y, W: r.Size.W, H: r.Size.H}
			}
		}
		out = append(out, w)
	}
	return out, nil
}

func (darwinPlatform) Screens() []computer.Rect {
	var ids [16]uint32
	var n uint32
	if cgGetActiveDisplayList(uint32(len(ids)), &ids[0], &n) != 0 {
		return nil
	}
	out := make([]computer.Rect, 0, n)
	for _, id := range ids[:n] {
		b := cgDisplayBounds(id)
		out = append(out, computer.Rect{X: b.Origin.X, Y: b.Origin.Y, W: b.Size.W, H: b.Size.H})
	}
	return out
}

func (darwinPlatform) Capture(windowID int, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/screencapture", "-x", "-o", "-l", fmt.Sprint(windowID), "-t", "png", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screencapture: %v: %s", err, out)
	}
	return nil
}

func (darwinPlatform) Activate(pid int) error {
	ok := false
	withPool(func() {
		a := objc.ID(objc.GetClass("NSRunningApplication")).Send(sel("runningApplicationWithProcessIdentifier:"), int32(pid))
		if a == 0 {
			return
		}
		// NSApplicationActivateAllWindows | NSApplicationActivateIgnoringOtherApps
		ok = objc.Send[bool](a, sel("activateWithOptions:"), uint(3))
	})
	if !ok {
		return errors.New("macOS did not activate the app")
	}
	return nil
}

func (darwinPlatform) Launch(app string) error {
	args := []string{"-g", "-a", app}
	if bundleIDRe.MatchString(app) {
		args = []string{"-g", "-b", app}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/open", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func postKey(pid int, code uint16, flags uint64, text []uint16) error {
	for _, down := range []bool{true, false} {
		ev := cgEventCreateKeyboardEvent(0, code, down)
		if ev == 0 {
			return errors.New("could not create a keyboard event")
		}
		cgEventSetFlags(ev, flags)
		if len(text) > 0 && down {
			cgEventKeyboardSetUnicodeString(ev, uint(len(text)), &text[0])
		}
		cgEventPostToPid(int32(pid), ev)
		cfRelease(ev)
		if down {
			time.Sleep(4 * time.Millisecond)
		}
	}
	return nil
}

func (darwinPlatform) PostKey(pid int, code uint16, flags uint64) error {
	return postKey(pid, code, flags, nil)
}

// PostText types s in chunks (a keyboard event carries at most 20 UTF-16
// units), never splitting a surrogate pair.
func (darwinPlatform) PostText(pid int, s string) error {
	u := utf16.Encode([]rune(s))
	const chunk = 16
	for len(u) > 0 {
		n := min(chunk, len(u))
		if n < len(u) && utf16.IsSurrogate(rune(u[n-1])) && u[n-1] < 0xDC00 {
			n-- // a high surrogate: keep it with its pair
		}
		if err := postKey(pid, 0, 0, u[:n]); err != nil {
			return err
		}
		u = u[n:]
		time.Sleep(8 * time.Millisecond)
	}
	return nil
}

func (darwinPlatform) PostMouse(kind mouseKind, b mouseButton, x, y float64, clicks int) error {
	var typ, button uint32
	switch kind {
	case mouseMove:
		typ = 5 // kCGEventMouseMoved
	case mouseDown:
		typ = 1 // kCGEventLeftMouseDown
		if b == buttonRight {
			typ = 3
		}
	case mouseUp:
		typ = 2
		if b == buttonRight {
			typ = 4
		}
	case mouseDrag:
		typ = 6 // kCGEventLeftMouseDragged
	}
	if b == buttonRight {
		button = 1
	}
	ev := cgEventCreateMouseEvent(0, typ, cgPoint{x, y}, button)
	if ev == 0 {
		return errors.New("could not create a mouse event")
	}
	if clicks > 0 {
		cgEventSetIntegerValueField(ev, kCGMouseEventClickState, int64(clicks))
	}
	cgEventPost(kCGHIDEventTap, ev)
	cfRelease(ev)
	return nil
}

// PostScroll scrolls by lines; dy > 0 scrolls down (content moves up),
// dx > 0 scrolls right.
func (darwinPlatform) PostScroll(x, y float64, dx, dy int) error {
	ev := cgEventCreateScrollWheelEvent2(0, kCGScrollEventUnitLine, 2, int32(-dy), int32(-dx), 0)
	if ev == 0 {
		return errors.New("could not create a scroll event")
	}
	cgEventPost(kCGHIDEventTap, ev)
	cfRelease(ev)
	return nil
}
